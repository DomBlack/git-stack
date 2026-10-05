package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

// remoteFixture is a repo with one commit on main, a bare origin holding
// the same, and a client.
func remoteFixture(t *testing.T) (*git.Client, git.Repo, string) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.InitRemote(t, dir)
	c := git.New(exec.New())
	repo, err := c.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, repo, dir
}

// pushRemote commits file on top of origin/<branch> and pushes it, leaving
// the local branch where it was. Returns the new remote tip.
func pushRemote(t *testing.T, dir, branch, file string) string {
	t.Helper()
	cur := gittest.Run(t, dir, "branch", "--show-current")
	gittest.Run(t, dir, "switch", "-q", "--detach", "refs/remotes/origin/"+branch)
	// Content includes the parent id so a second push of the same file is never an empty commit.
	sha := gittest.Commit(t, dir, file, file+" at "+gittest.Run(t, dir, "rev-parse", "HEAD"), "remote: "+file)
	gittest.Run(t, dir, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	gittest.Run(t, dir, "switch", "-q", cur)
	return sha
}

func TestFetchAndTip(t *testing.T) {
	c, repo, dir := remoteFixture(t)
	ctx := context.Background()
	if got := c.RemoteFor(ctx, repo, "main"); got != "origin" {
		t.Errorf("RemoteFor = %q", got)
	}
	sha := pushRemote(t, dir, "main", "r.txt")
	// The push above already updated origin/main locally; reset it so Fetch has work to do.
	gittest.Run(t, dir, "update-ref", "refs/remotes/origin/main", "refs/heads/main")
	if err := c.Fetch(ctx, repo, "origin"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Tip(ctx, repo, "refs/remotes/origin/main")
	if err != nil || !ok || got != sha {
		t.Errorf("Tip(origin/main) = %q %v %v, want %q", got, ok, err, sha)
	}
	if _, ok, err := c.Tip(ctx, repo, "refs/heads/nope"); err != nil || ok {
		t.Errorf("missing ref: ok=%v err=%v", ok, err)
	}
}

func TestFetchFailure(t *testing.T) {
	c, repo, dir := remoteFixture(t)
	gittest.Run(t, dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))
	err := c.Fetch(context.Background(), repo, "origin")
	if err == nil || !strings.Contains(err.Error(), "fetch origin") {
		t.Errorf("want a fetch error naming the remote, got %v", err)
	}
}

func TestUpdateRefsTransaction(t *testing.T) {
	c, repo, dir := remoteFixture(t)
	ctx := context.Background()
	old := gittest.Run(t, dir, "rev-parse", "main")
	gittest.Run(t, dir, "branch", "x", "main")
	newTip := gittest.Commit(t, dir, "n.txt", "n", "n") // on main
	gittest.Run(t, dir, "update-ref", "refs/heads/main", old)
	// Good: both move.
	if err := c.UpdateRefs(ctx, repo, []git.RefUpdate{
		{Ref: "refs/heads/x", New: newTip, Old: old},
		{Ref: "refs/heads/y", New: newTip},
	}); err != nil {
		t.Fatal(err)
	}
	if gittest.Run(t, dir, "rev-parse", "x") != newTip || gittest.Run(t, dir, "rev-parse", "y") != newTip {
		t.Error("refs not moved")
	}
	// Stale old value: nothing moves, including the valid update.
	err := c.UpdateRefs(ctx, repo, []git.RefUpdate{
		{Ref: "refs/heads/x", New: old, Old: newTip},
		{Ref: "refs/heads/y", New: old, Old: old}, // y is at newTip, so this is stale
	})
	if err == nil {
		t.Fatal("stale old value must fail")
	}
	if gittest.Run(t, dir, "rev-parse", "x") != newTip {
		t.Error("transaction must be all or nothing")
	}
}

func TestRevListMergeBaseDelete(t *testing.T) {
	c, repo, dir := remoteFixture(t)
	ctx := context.Background()
	base := gittest.Run(t, dir, "rev-parse", "main")
	gittest.Run(t, dir, "switch", "-q", "-c", "f")
	c1 := gittest.Commit(t, dir, "1.txt", "1", "one")
	c2 := gittest.Commit(t, dir, "2.txt", "2", "two")
	list, err := c.RevList(ctx, repo, "main", "f")
	if err != nil || strings.Join(list, ",") != c1+","+c2 {
		t.Errorf("RevList = %v %v", list, err)
	}
	if mb, err := c.MergeBase(ctx, repo, "main", "f"); err != nil || mb != base {
		t.Errorf("MergeBase = %q %v", mb, err)
	}
	gittest.Run(t, dir, "switch", "-q", "main")
	if err := c.DeleteBranch(ctx, repo, "f"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Tip(ctx, repo, "refs/heads/f"); ok {
		t.Error("f should be gone")
	}
}

func TestMergeFFAndResetHard(t *testing.T) {
	c, repo, dir := remoteFixture(t)
	ctx := context.Background()
	sha := pushRemote(t, dir, "main", "r.txt")
	if err := c.MergeFF(ctx, repo, "refs/remotes/origin/main"); err != nil {
		t.Fatal(err)
	}
	if gittest.Run(t, dir, "rev-parse", "main") != sha {
		t.Error("main not fast forwarded")
	}
	if _, err := os.Stat(filepath.Join(dir, "r.txt")); err != nil {
		t.Error("worktree not updated")
	}
	// A conflicting dirty file makes MergeFF refuse and leaves the file.
	sha2 := pushRemote(t, dir, "main", "r.txt")
	gittest.WriteFile(t, dir, "r.txt", "mine")
	if err := c.MergeFF(ctx, repo, "refs/remotes/origin/main"); err == nil {
		t.Fatal("MergeFF must refuse over a conflicting dirty file")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "r.txt")); string(b) != "mine" {
		t.Error("dirty file must be untouched")
	}
	if err := c.ResetHard(ctx, repo, sha2); err != nil {
		t.Fatal(err)
	}
	if gittest.Run(t, dir, "rev-parse", "main") != sha2 {
		t.Error("ResetHard did not move main")
	}
}

func TestResetKeepKeepsUnrelatedLocalChanges(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	gittest.Commit(t, dir, "shared.txt", "base", "shared")
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	target := gittest.Commit(t, dir, "f.txt", "f", "f")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.WriteFile(t, dir, "shared.txt", "local edit")

	if err := c.ResetKeep(ctx, repo, target); err != nil {
		t.Fatal(err)
	}
	if gittest.Run(t, dir, "rev-parse", "main") != target {
		t.Error("main should have moved")
	}
	if got := gittest.Run(t, dir, "status", "--short"); got != "M shared.txt" {
		t.Errorf("status = %q, want the local edit kept", got)
	}
}

func TestResetKeepKeepsStagedNewFile(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	target := gittest.Commit(t, dir, "f.txt", "f", "f")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.WriteFile(t, dir, "n.txt", "new")
	gittest.Run(t, dir, "add", "n.txt")

	if err := c.ResetKeep(context.Background(), repo, target); err != nil {
		t.Fatal(err)
	}
	if gittest.Run(t, dir, "rev-parse", "main") != target {
		t.Error("main should have moved")
	}
	if got := gittest.Run(t, dir, "status", "--short"); got != "A  n.txt" {
		t.Errorf("status = %q, want n.txt still staged", got)
	}
}

func TestResetKeepRefusesOverlappingChange(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	gittest.Commit(t, dir, "shared.txt", "base", "shared")
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	target := gittest.Commit(t, dir, "shared.txt", "feat", "feat edits shared")
	gittest.Run(t, dir, "switch", "-q", "main")
	before := gittest.Run(t, dir, "rev-parse", "main")
	gittest.WriteFile(t, dir, "shared.txt", "local edit")

	if err := c.ResetKeep(ctx, repo, target); err == nil {
		t.Fatal("reset --keep must refuse when the local change overlaps")
	}
	if gittest.Run(t, dir, "rev-parse", "main") != before {
		t.Error("main must not move on refusal")
	}
}
