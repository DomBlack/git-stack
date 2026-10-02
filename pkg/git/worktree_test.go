package git_test

import (
	"context"
	"path/filepath"
	"testing"

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
