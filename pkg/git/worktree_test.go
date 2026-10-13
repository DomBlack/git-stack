package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestWorktrees(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := newClient()
	ctx := context.Background()
	repo, err := c.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "side", wt)
	detached := filepath.Join(t.TempDir(), "det")
	gittest.Run(t, dir, "worktree", "add", "-q", "--detach", detached)

	wts, err := c.Worktrees(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 3 {
		t.Fatalf("worktrees = %+v", wts)
	}
	if wts[0].Path != realPath(t, dir) || wts[0].Branch != "main" || wts[0].Head == "" {
		t.Errorf("main worktree = %+v", wts[0])
	}
	if wts[1].Path != realPath(t, wt) || wts[1].Branch != "side" || wts[1].Detached {
		t.Errorf("linked worktree = %+v", wts[1])
	}
	if wts[2].Path != realPath(t, detached) || !wts[2].Detached || wts[2].Branch != "" {
		t.Errorf("detached worktree = %+v", wts[2])
	}
}

func TestWorktreesMarksMissingDirectoriesPrunable(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := newClient()
	ctx := context.Background()
	repo, err := c.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "gone", gone)
	locked := filepath.Join(t.TempDir(), "locked")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "locked", locked)
	gittest.Run(t, dir, "worktree", "lock", "--reason", "on a usb stick", locked)
	for _, p := range []string{gone, locked} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}

	wts, err := c.Worktrees(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	// git never offers a locked worktree for pruning, gone or not.
	if len(wts) != 3 || wts[0].Prunable || !wts[1].Prunable || wts[1].Locked || wts[2].Prunable || !wts[2].Locked {
		t.Fatalf("worktrees = %+v", wts)
	}

	if err := c.PruneWorktrees(ctx, repo); err != nil {
		t.Fatal(err)
	}
	wts, err = c.Worktrees(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 2 || wts[1].Branch != "locked" {
		t.Errorf("after prune = %+v; want the locked one kept", wts)
	}
}

func TestSwitchReportsFilesInTheWay(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := newClient()
	ctx := context.Background()
	repo, err := c.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "switch", "-q", "-c", "side")
	gittest.Commit(t, dir, "f.txt", "side", "side changes f")
	gittest.WriteFile(t, dir, "f.txt", "edited")
	gittest.WriteFile(t, dir, "other.txt", "carried")

	err = c.Switch(ctx, repo, "main")
	oe, ok := errors.AsType[*git.OverwriteError](err)
	if !ok || len(oe.Changed) != 1 || oe.Changed[0] != "f.txt" {
		t.Fatalf("err = %v; want an OverwriteError naming f.txt", err)
	}

	gittest.Run(t, dir, "checkout", "-q", "--", "f.txt")
	if err := c.Switch(ctx, repo, "main"); err != nil {
		t.Fatalf("a change git can carry over should not stop the switch: %v", err)
	}
	if got := gittest.Run(t, dir, "status", "--porcelain"); got != "?? other.txt" {
		t.Errorf("status = %q", got)
	}
}
