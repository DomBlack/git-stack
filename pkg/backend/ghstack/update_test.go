//go:build unix

package ghstack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

const mainFile = `{
  "schemaVersion": 1,
  "repository": "o/r",
  "future": {"keep": [1, 2]},
  "stacks": [
    {
      "id": "S1",
      "number": 7,
      "trunk": {"branch": "main", "head": "t1"},
      "branches": [
        {"branch": "a", "head": "ha", "base": "t1", "pullRequest": {"number": 1, "merged": true}, "extra": "yes"},
        {"branch": "b", "head": "hb", "base": "ha"}
      ]
    },
    {
      "trunk": {"branch": "main"},
      "branches": [{"branch": "done", "pullRequest": {"number": 2, "merged": true}}]
    }
  ]
}
`

const wtFile = `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"w","head":"hw"}]}]}`

func updateFixture(t *testing.T) (*Backend, git.Repo, string, string) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "w", wt)
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(repo.CommonDir, FileName)
	wtPath := filepath.Join(repo.CommonDir, "worktrees", "wt", FileName)
	if err := os.WriteFile(mainPath, []byte(mainFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtPath, []byte(wtFile), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(exec.New(), g), repo, mainPath, wtPath
}

func TestUpdateEditsInPlace(t *testing.T) {
	b, repo, mainPath, wtPath := updateFixture(t)
	if err := os.Chmod(mainPath, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(wtPath)
	err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
		for i := range g.Stacks {
			s := &g.Stacks[i]
			switch s.Bottom() {
			case "a":
				s.Branches = s.Branches[1:] // a pruned
				s.Branches[0].Head = "hb2"
				s.Branches[0].Base = "t2"
			case "done":
				s.Branches = nil // finished stack
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(mainPath); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("the rewritten file must keep its mode: %v %v", fi, err)
	}
	got, _ := os.ReadFile(mainPath)
	s := string(got)
	for _, want := range []string{`"future"`, `"keep"`, `"id": "S1"`, `"number": 7`, `"head": "t1"`, `"branch": "b"`, `"head": "hb2"`, `"base": "t2"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in:\n%s", want, s)
		}
	}
	for _, gone := range []string{`"branch": "a"`, `"extra"`, `"done"`} {
		if strings.Contains(s, gone) {
			t.Errorf("should be gone: %s in:\n%s", gone, s)
		}
	}
	if strings.Index(s, `"schemaVersion"`) > strings.Index(s, `"repository"`) || strings.Index(s, `"repository"`) > strings.Index(s, `"future"`) {
		t.Error("member order must be preserved")
	}
	if after, _ := os.ReadFile(wtPath); string(after) != string(before) {
		t.Error("the untouched worktree file must be byte for byte the same")
	}
	g, err := b.Load(context.Background(), repo)
	if err != nil || len(g.Stacks) != 2 || g.Stacks[0].Bottom() != "b" || g.Stacks[1].Bottom() != "w" {
		t.Errorf("reload = %+v %v", g, err)
	}
}

func TestUpdateWritesEachWorktreeFile(t *testing.T) {
	b, repo, _, wtPath := updateFixture(t)
	err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
		for i := range g.Stacks {
			if g.Stacks[i].Bottom() == "w" {
				g.Stacks[i].Branches[0].Head = "hw2"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(wtPath); !strings.Contains(string(got), `"hw2"`) {
		t.Errorf("worktree file not updated: %s", got)
	}
}

func TestUpdateFnErrorWritesNothing(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	before, _ := os.ReadFile(mainPath)
	err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
		g.Stacks[0].Branches = nil
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("fn's error must be returned")
	}
	if after, _ := os.ReadFile(mainPath); string(after) != string(before) {
		t.Error("nothing may be written when fn fails")
	}
}

func TestUpdateTimesOutOnHeldLock(t *testing.T) {
	b, repo, _, _ := updateFixture(t)
	old := lockWait
	lockWait = 150 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	f, err := os.OpenFile(filepath.Join(repo.CommonDir, LockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	err = b.Update(context.Background(), repo, func(*stack.Graph) error { return nil })
	if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindLocked {
		t.Errorf("want KindLocked, got %v", err)
	}
}
