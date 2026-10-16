package git_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func undoRefsFixture(t *testing.T) (*git.Client, git.Repo, string) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := git.New(exec.New())
	repo, err := c.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, repo, dir
}

func TestEmptyTreeCommit(t *testing.T) {
	c, repo, dir := undoRefsFixture(t)
	ctx := context.Background()
	one := gittest.Run(t, dir, "rev-parse", "HEAD")
	two := gittest.Commit(t, dir, "x.txt", "x", "x")
	sha, err := c.EmptyTreeCommit(ctx, repo, []string{one, two}, "keep these")
	if err != nil {
		t.Fatal(err)
	}
	parents := strings.Fields(gittest.Run(t, dir, "rev-list", "--parents", "-n1", sha))[1:]
	if !slices.Equal(parents, []string{one, two}) {
		t.Errorf("parents = %v", parents)
	}
	if files := gittest.Run(t, dir, "ls-tree", sha); files != "" {
		t.Errorf("tree should be empty: %q", files)
	}
}

func TestUpdateRefsDeletes(t *testing.T) {
	c, repo, dir := undoRefsFixture(t)
	ctx := context.Background()
	head := gittest.Run(t, dir, "rev-parse", "HEAD")
	gittest.Run(t, dir, "branch", "gone")
	if err := c.UpdateRefs(ctx, repo, []git.RefUpdate{{Ref: "refs/heads/gone", Old: head}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Tip(ctx, repo, "refs/heads/gone"); ok {
		t.Error("gone should be deleted")
	}
}

func TestPointHEADAndResetSoft(t *testing.T) {
	c, repo, dir := undoRefsFixture(t)
	ctx := context.Background()
	base := gittest.Run(t, dir, "rev-parse", "HEAD")
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	gittest.Commit(t, dir, "f.txt", "f", "f")
	if err := c.PointHEAD(ctx, repo, "main"); err != nil {
		t.Fatal(err)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "main" {
		t.Errorf("current = %s", cur)
	}
	if st := gittest.Run(t, dir, "status", "--short"); st != "A  f.txt" {
		t.Errorf("status after PointHEAD = %q", st)
	}
	gittest.Run(t, dir, "switch", "-q", "-f", "feat")
	if err := c.ResetSoft(ctx, repo, base); err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, dir, "rev-parse", "feat"); got != base {
		t.Errorf("feat = %s, want %s", got, base)
	}
	if st := gittest.Run(t, dir, "status", "--short"); st != "A  f.txt" {
		t.Errorf("status after ResetSoft = %q", st)
	}
}
