package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

const lockStderr = "fatal: Unable to create '/repo/.git/index.lock': File exists.\n\n" +
	"Another git process seems to be running in this repository, or the lock file may be stale"

// fakeSleep records requested delays instead of waiting.
type fakeSleep struct{ total time.Duration }

func (s *fakeSleep) sleep(ctx context.Context, d time.Duration) error {
	s.total += d
	return ctx.Err()
}

func TestGitRetriesWhileTheIndexIsLocked(t *testing.T) {
	f := exectest.New()
	attempts := 0
	f.On("git", "add").Do(func(exec.Cmd) (exec.Result, error) {
		attempts++
		if attempts <= 2 {
			return exec.Result{ExitCode: 128, Stderr: []byte(lockStderr)}, &exec.ExitError{Result: exec.Result{ExitCode: 128, Stderr: []byte(lockStderr)}}
		}
		return exec.Result{}, nil
	})
	sl := &fakeSleep{}
	c := git.New(f, git.WithLockRetry(2*time.Second, sl.sleep))
	if err := c.Add(context.Background(), git.Repo{TopLevel: "/repo"}, git.AddAll); err != nil {
		t.Fatalf("Add should succeed once the lock clears: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if sl.total == 0 || sl.total > 2*time.Second {
		t.Errorf("slept %v in total; want some backoff within the budget", sl.total)
	}
}

func TestGitGivesUpOnTheLockAfterTheBudget(t *testing.T) {
	f := exectest.New()
	attempts := 0
	f.On("git", "add").Do(func(exec.Cmd) (exec.Result, error) {
		attempts++
		return exec.Result{ExitCode: 128, Stderr: []byte(lockStderr)}, &exec.ExitError{Cmd: exec.Cmd{Name: "git", Args: []string{"add", "-A"}}, Result: exec.Result{ExitCode: 128, Stderr: []byte(lockStderr)}}
	})
	sl := &fakeSleep{}
	c := git.New(f, git.WithLockRetry(2*time.Second, sl.sleep))
	err := c.Add(context.Background(), git.Repo{TopLevel: "/repo"}, git.AddAll)
	if err == nil {
		t.Fatal("expected an error once the budget is spent")
	}
	if attempts < 3 {
		t.Errorf("attempts = %d; the budget should allow several retries", attempts)
	}
	if sl.total > 2*time.Second+time.Second {
		t.Errorf("slept %v; should stop around the 2s budget", sl.total)
	}
	msg := err.Error()
	for _, want := range []string{"git add -A", "index.lock", "another git process"} {
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
			t.Errorf("error %q should mention %q", msg, want)
		}
	}
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Errorf("the underlying ExitError should still be reachable: %v", err)
	}
}

func TestGitDoesNotRetryOtherFailures(t *testing.T) {
	f := exectest.New()
	attempts := 0
	f.On("git", "add").Do(func(exec.Cmd) (exec.Result, error) {
		attempts++
		return exec.Result{ExitCode: 128, Stderr: []byte("fatal: pathspec 'x' did not match any files")},
			&exec.ExitError{Result: exec.Result{ExitCode: 128, Stderr: []byte("fatal: pathspec 'x' did not match any files")}}
	})
	sl := &fakeSleep{}
	c := git.New(f, git.WithLockRetry(2*time.Second, sl.sleep))
	if err := c.Add(context.Background(), git.Repo{TopLevel: "/repo"}, git.AddAll); err == nil {
		t.Fatal("expected the failure to be returned")
	}
	if attempts != 1 || sl.total != 0 {
		t.Errorf("attempts = %d, slept %v; a non-lock failure must not be retried", attempts, sl.total)
	}
}

func TestGitLockRetryHonoursCancellation(t *testing.T) {
	f := exectest.New()
	f.On("git", "add").Fail(128, lockStderr)
	ctx, cancel := context.WithCancel(context.Background())
	c := git.New(f, git.WithLockRetry(2*time.Second, func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}))
	err := c.Add(ctx, git.Repo{TopLevel: "/repo"}, git.AddAll)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled during backoff should return the context error, got %v", err)
	}
}

// TestGitLockRetryAgainstRealGit holds .git/index.lock for a moment while
// Add runs, the way an IDE or another agent would.
func TestGitLockRetryAgainstRealGit(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.WriteFile(t, dir, "new.txt", "hello")
	c := git.New(exec.New())
	ctx := context.Background()
	repo, err := c.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(repo.GitDir, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = os.Remove(lock)
	}()
	start := time.Now()
	if err := c.Add(ctx, repo, git.AddAll); err != nil {
		t.Fatalf("Add should wait for the lock to clear: %v", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Errorf("Add returned after %v; it should have waited for the lock", time.Since(start))
	}
	if out := gittest.Run(t, dir, "diff", "--cached", "--name-only"); out != "new.txt" {
		t.Errorf("staged = %q, want new.txt", out)
	}
}
