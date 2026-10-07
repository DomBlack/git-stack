package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	f := withIndex(exectest.New())
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
	f := withIndex(exectest.New())
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
	f := withIndex(exectest.New())
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
	f := withIndex(exectest.New())
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

// merge --ff-only reports a held lock with exit code 1 and "error:", not
// 128 and "fatal:"; it has to be retried just the same, and a lock that
// stays has to be recognisable without parsing git's text.
func TestGitRetriesALockedMergeAndNamesThePersistentOne(t *testing.T) {
	mergeLocked := "error: Unable to create '/wt/.git/index.lock': File exists.\n\n" +
		"Another git process seems to be running in this repository, or the lock file may be stale"
	locked := func(exec.Cmd) (exec.Result, error) {
		r := exec.Result{ExitCode: 1, Stderr: []byte(mergeLocked)}
		return r, &exec.ExitError{Result: r}
	}
	for _, tc := range []struct {
		name       string
		lockedRuns int
		wantErr    bool
	}{
		{"lock released after two attempts", 2, false},
		{"lock never released", 1000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := withIndex(exectest.New())
			attempts := 0
			f.On("git", "merge").Do(func(c exec.Cmd) (exec.Result, error) {
				attempts++
				if attempts <= tc.lockedRuns {
					return locked(c)
				}
				return exec.Result{}, nil
			})
			sl := &fakeSleep{}
			c := git.New(f, git.WithLockRetry(2*time.Second, sl.sleep))
			err := c.MergeFF(context.Background(), git.Repo{TopLevel: "/wt"}, "abc")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if !errors.Is(err, git.ErrIndexLocked) {
					t.Errorf("a lock that stays must match ErrIndexLocked: %v", err)
				}
				if strings.Contains(err.Error(), "\n") {
					t.Errorf("the error must be one line: %q", err)
				}
				if sl.total > 2*time.Second {
					t.Errorf("slept %v, over the budget", sl.total)
				}
			} else if attempts != 3 {
				t.Errorf("attempts = %d, want 3", attempts)
			}
		})
	}
}

func TestGitTidiesOtherMergeFailures(t *testing.T) {
	f := withIndex(exectest.New())
	f.On("git", "merge").Do(func(exec.Cmd) (exec.Result, error) {
		r := exec.Result{ExitCode: 128, Stderr: []byte("fatal: Not possible to fast-forward, aborting.\nhint: something\n")}
		return r, &exec.ExitError{Result: r}
	})
	err := git.New(f).MergeFF(context.Background(), git.Repo{TopLevel: "/wt"}, "abc")
	if err == nil || err.Error() != "fast forward: Not possible to fast-forward, aborting." {
		t.Errorf("err = %v", err)
	}
}

// git's "would be overwritten" refusal becomes the two file lists.
func TestMergeFFNamesTheFilesInTheWay(t *testing.T) {
	block := func(header string, files ...string) string {
		var b strings.Builder
		b.WriteString("error: " + header + "\n")
		for _, f := range files {
			b.WriteString("\t" + f + "\n")
		}
		b.WriteString("Please commit your changes or stash them before you merge.\n")
		return b.String()
	}
	const local = "Your local changes to the following files would be overwritten by merge:"
	const untracked = "The following untracked working tree files would be overwritten by merge:"
	many := []string{"a1", "a2", "a3", "a4", "a5", "a6"}
	for _, tc := range []struct {
		name               string
		stderr             string
		changed, untracked []string
	}{
		{"one changed", block(local, "a.txt"), []string{"a.txt"}, nil},
		{"three changed", block(local, "a.txt", "dir/b.go", "c.md"), []string{"a.txt", "dir/b.go", "c.md"}, nil},
		{"many changed", block(local, many...), many, nil},
		{"paths with spaces", block(local, "my notes.txt", "dir with space/x y.go"), []string{"my notes.txt", "dir with space/x y.go"}, nil},
		{"quoted path", block(untracked, `"caf\303\251.txt"`), nil, []string{"café.txt"}},
		{"one untracked", block(untracked, "r.txt"), nil, []string{"r.txt"}},
		{"both", block(local, "a.txt", "b c.txt") + block(untracked, "new two.txt", "new.txt") + "Aborting\n",
			[]string{"a.txt", "b c.txt"}, []string{"new two.txt", "new.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := withIndex(exectest.New())
			f.On("git", "merge").Do(func(exec.Cmd) (exec.Result, error) {
				r := exec.Result{ExitCode: 1, Stderr: []byte(tc.stderr)}
				return r, &exec.ExitError{Result: r}
			})
			err := git.New(f).MergeFF(context.Background(), git.Repo{TopLevel: "/wt"}, "abc")
			oe, ok := errors.AsType[*git.OverwriteError](err)
			if !ok {
				t.Fatalf("err = %v, want an OverwriteError", err)
			}
			if !slices.Equal(oe.Changed, tc.changed) || !slices.Equal(oe.Untracked, tc.untracked) {
				t.Errorf("changed %q untracked %q, want %q %q", oe.Changed, oe.Untracked, tc.changed, tc.untracked)
			}
		})
	}
}

// git adds "Another git process seems to be running" for every kind of
// lock, so only the file it names decides which lock it was.
func TestLockKindComesFromTheFileGitNames(t *testing.T) {
	refLocked := "error: cannot lock ref 'refs/heads/main': Unable to create '/repo/.git/refs/heads/main.lock': File exists.\n\n" +
		"Another git process seems to be running in this repository, or the lock file may be stale"
	locked := func(stderr string) func(exec.Cmd) (exec.Result, error) {
		return func(exec.Cmd) (exec.Result, error) {
			r := exec.Result{ExitCode: 128, Stderr: []byte(stderr)}
			return r, &exec.ExitError{Result: r}
		}
	}
	t.Run("a ref lock in merge is reported, not retried, and isn't the index", func(t *testing.T) {
		f := withIndex(exectest.New())
		attempts := 0
		f.On("git", "merge").Do(func(c exec.Cmd) (exec.Result, error) { attempts++; return locked(refLocked)(c) })
		sl := &fakeSleep{}
		err := git.New(f, git.WithLockRetry(2*time.Second, sl.sleep)).MergeFF(context.Background(), git.Repo{TopLevel: "/repo"}, "abc")
		if errors.Is(err, git.ErrIndexLocked) || !errors.Is(err, git.ErrRefLocked) {
			t.Errorf("err = %v; want a ref lock, not the index", err)
		}
		if git.LockPath(err) != "/repo/.git/refs/heads/main.lock" {
			t.Errorf("lock = %q", git.LockPath(err))
		}
		if attempts != 1 || sl.total != 0 {
			t.Errorf("attempts %d, slept %v; merge may have moved the worktree before the ref, so no retry", attempts, sl.total)
		}
	})
	t.Run("the index lock still is the index", func(t *testing.T) {
		f := withIndex(exectest.New())
		f.On("git", "merge").Do(locked(lockStderr))
		err := git.New(f, git.WithLockRetry(100*time.Millisecond, (&fakeSleep{}).sleep)).MergeFF(context.Background(), git.Repo{TopLevel: "/repo"}, "abc")
		if !errors.Is(err, git.ErrIndexLocked) || errors.Is(err, git.ErrRefLocked) || git.LockPath(err) != "/repo/.git/index.lock" {
			t.Errorf("err = %v, lock %q", err, git.LockPath(err))
		}
	})
	for _, tc := range []struct {
		name    string
		release int // attempts before the lock goes; 0: never
	}{
		{"a ref transaction waits out a brief ref lock", 2},
		{"a ref transaction gives up on one that stays", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := withIndex(exectest.New())
			attempts := 0
			f.On("git", "update-ref").Do(func(c exec.Cmd) (exec.Result, error) {
				attempts++
				if tc.release == 0 || attempts <= tc.release {
					return locked(refLocked)(c)
				}
				return exec.Result{}, nil
			})
			err := git.New(f, git.WithLockRetry(2*time.Second, (&fakeSleep{}).sleep)).UpdateRefs(context.Background(), git.Repo{TopLevel: "/repo"},
				[]git.RefUpdate{{Ref: "refs/heads/main", New: "b", Old: "a"}})
			if tc.release > 0 {
				if err != nil || attempts != tc.release+1 {
					t.Errorf("err = %v after %d attempts", err, attempts)
				}
				return
			}
			if !errors.Is(err, git.ErrRefLocked) || git.LockPath(err) != "/repo/.git/refs/heads/main.lock" || attempts < 3 {
				t.Errorf("err = %v, lock %q, attempts %d", err, git.LockPath(err), attempts)
			}
		})
	}
}

// withIndex teaches a fake git where each worktree's index is
// (<dir>/.git/index), the way rev-parse --git-path index answers.
func withIndex(f *exectest.Fake) *exectest.Fake {
	f.On("git", "rev-parse", "--path-format=absolute", "--git-path", "index").Do(func(c exec.Cmd) (exec.Result, error) {
		return exec.Result{Stdout: []byte(c.Dir + "/.git/index\n")}, nil
	})
	return f
}

// countMerges counts the merge runs that reach git.
type countMerges struct {
	exec.Runner
	merges int
}

func (c *countMerges) Run(ctx context.Context, cmd exec.Cmd) (exec.Result, error) {
	if len(cmd.Args) > 0 && cmd.Args[0] == "merge" {
		c.merges++
	}
	return c.Runner.Run(ctx, cmd)
}

// ffRepo makes a repository at dir with main one commit ahead of branch,
// which is checked out, and returns the commit to fast forward to.
func ffRepo(t *testing.T, dir, branch string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "init", "-q", "-b", "main")
	gittest.Commit(t, dir, "a.txt", "a", "a")
	gittest.Run(t, dir, "branch", branch)
	tip := gittest.Commit(t, dir, "b.txt", "b", "b")
	gittest.Run(t, dir, "switch", "-q", branch)
	return tip
}

// A lock is the worktree's index lock only when it is that exact file. A
// branch called index (or foo/index) has a ref lock called index.lock too,
// and git takes it after the fast forward has already moved the worktree,
// so it must never be retried.
func TestFastForwardLockClassificationAgainstRealGit(t *testing.T) {
	gittest.Isolate(t)
	realTemp := func() string {
		d, err := filepath.EvalSymlinks(t.TempDir()) // compare like git reports (/private/var on macOS)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, branch := range []string{"index", "foo/index"} {
		t.Run("ref lock on a branch called "+branch, func(t *testing.T) {
			dir := realTemp()
			tip := ffRepo(t, dir, branch)
			lock := filepath.Join(dir, ".git", "refs", "heads", filepath.FromSlash(branch)+".lock")
			if err := os.WriteFile(lock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			r := &countMerges{Runner: exec.New()}
			err := git.New(r, git.WithLockRetry(2*time.Second, nil)).MergeFF(context.Background(), git.Repo{TopLevel: dir}, tip)
			if r.merges != 1 {
				t.Errorf("merge ran %d times; a ref lock must never be retried", r.merges)
			}
			if !errors.Is(err, git.ErrRefLocked) || errors.Is(err, git.ErrIndexLocked) {
				t.Errorf("err = %v; want a ref lock", err)
			}
			if got := git.LockPath(err); got != lock {
				t.Errorf("lock = %q, want %q", got, lock)
			}
		})
	}

	// The worktree's own index lock, released while we wait, is retried and
	// the fast forward goes through: in the main worktree, a linked one,
	// paths with an apostrophe or a newline, and a GIT_INDEX_FILE.
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (dir, lock string)
	}{
		{"linked worktree", func(t *testing.T) (string, string) {
			main := realTemp()
			ffRepo(t, main, "old")
			gittest.Run(t, main, "switch", "-q", "main")
			wt := filepath.Join(realTemp(), "wt")
			gittest.Run(t, main, "worktree", "add", "-q", "-b", "linked", wt, "HEAD~1")
			return wt, filepath.Join(main, ".git", "worktrees", "wt", "index.lock")
		}},
		{"apostrophe in the path", func(t *testing.T) (string, string) {
			dir := filepath.Join(realTemp(), "it's repo")
			ffRepo(t, dir, "old")
			return dir, filepath.Join(dir, ".git", "index.lock")
		}},
		{"newline in the path", func(t *testing.T) (string, string) {
			dir := filepath.Join(realTemp(), "a\nb")
			ffRepo(t, dir, "old")
			return dir, filepath.Join(dir, ".git", "index.lock")
		}},
		{"GIT_INDEX_FILE", func(t *testing.T) (string, string) {
			dir := realTemp()
			ffRepo(t, dir, "old")
			index := filepath.Join(realTemp(), "custom-index")
			gittest.Run(t, dir, "read-tree", "HEAD") // populate the default index
			if b, err := os.ReadFile(filepath.Join(dir, ".git", "index")); err != nil || os.WriteFile(index, b, 0o644) != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_INDEX_FILE", index)
			return dir, index + ".lock"
		}},
	} {
		t.Run("index lock: "+tc.name, func(t *testing.T) {
			dir, lock := tc.setup(t)
			target := gittest.Run(t, dir, "rev-parse", "main")
			if err := os.WriteFile(lock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			time.AfterFunc(300*time.Millisecond, func() { _ = os.Remove(lock) })
			r := &countMerges{Runner: exec.New()}
			if err := git.New(r, git.WithLockRetry(2*time.Second, nil)).MergeFF(context.Background(), git.Repo{TopLevel: dir}, target); err != nil {
				t.Fatalf("the index lock should be waited out: %v", err)
			}
			if r.merges < 2 {
				t.Errorf("merge ran %d times; the held index lock should have been retried", r.merges)
			}
		})
	}
}

// git's message is parsed up to the last "': File exists", so a lock path
// with an apostrophe or a newline in it comes through whole.
func TestLockPathWithAwkwardCharacters(t *testing.T) {
	for _, path := range []string{"/tmp/it's-repo/.git/index.lock", "/tmp/a\nb/.git/refs/heads/x.lock", "/tmp/plain/.git/index.lock"} {
		f := withIndex(exectest.New())
		f.On("git", "merge").Do(func(exec.Cmd) (exec.Result, error) {
			r := exec.Result{ExitCode: 128, Stderr: []byte("fatal: Unable to create '" + path + "': File exists.\n\nAnother git process seems to be running in this repository, or the lock file may be stale\n")}
			return r, &exec.ExitError{Result: r}
		})
		err := git.New(f, git.WithLockRetry(50*time.Millisecond, (&fakeSleep{}).sleep)).MergeFF(context.Background(), git.Repo{TopLevel: "/elsewhere"}, "abc")
		if git.LockPath(err) != path {
			t.Errorf("lock = %q, want %q (err %v)", git.LockPath(err), path, err)
		}
	}
}
