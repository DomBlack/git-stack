package git_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

func TestReflog(t *testing.T) {
	c, repo, dir := remoteFixture(t)
	ctx := context.Background()
	first := gittest.Run(t, dir, "rev-parse", "HEAD")
	second := gittest.Commit(t, dir, "a.txt", "a", "a")
	got, err := c.Reflog(ctx, repo, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0] != second || !slices.Contains(got, first) {
		t.Errorf("reflog = %v, want %s then %s", got, second, first)
	}
	if got, err := c.Reflog(ctx, repo, "refs/heads/nope"); err != nil || got != nil {
		t.Errorf("missing ref: %v %v", got, err)
	}
}

func TestPush(t *testing.T) {
	c, repo, dir := remoteFixture(t)
	ctx := context.Background()
	remoteHead := func(branch string) string {
		out, _ := gittest.RunErr(t, dir, "ls-remote", "origin", "refs/heads/"+branch)
		sha, _, _ := strings.Cut(out, "\t")
		return sha
	}

	// A new branch is leased against not existing yet.
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	a1 := gittest.Commit(t, dir, "a.txt", "a", "a")
	if err := c.Push(ctx, repo, "origin", []git.PushLease{{Branch: "a"}}); err != nil {
		t.Fatal(err)
	}
	if got := remoteHead("a"); got != a1 {
		t.Fatalf("remote a = %q, want %s", got, a1)
	}

	// A rewritten branch is force pushed over the tip it was leased against.
	gittest.Run(t, dir, "commit", "-q", "--amend", "-m", "a amended")
	a2 := gittest.Run(t, dir, "rev-parse", "HEAD")
	if err := c.Push(ctx, repo, "origin", []git.PushLease{{Branch: "a", Expect: a1}}); err != nil {
		t.Fatal(err)
	}
	if got := remoteHead("a"); got != a2 {
		t.Fatalf("remote a = %q, want %s", got, a2)
	}

	// Someone else moves a; the lease on the tip we saw refuses every branch
	// in the push, not just a.
	gittest.Run(t, dir, "fetch", "-q", "origin")
	pushRemote(t, dir, "a", "theirs.txt")
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "b.txt", "b", "b")
	gittest.Run(t, dir, "switch", "-q", "a")
	gittest.Commit(t, dir, "a2.txt", "a2", "a again")
	err := c.Push(ctx, repo, "origin", []git.PushLease{{Branch: "a", Expect: a2}, {Branch: "b"}})
	if err == nil {
		t.Fatal("a stale lease must refuse the push")
	}
	if got := remoteHead("b"); got != "" {
		t.Errorf("b was pushed despite the refusal: %s", got)
	}
}

func TestPointHEADKeepsTheIndexAndWorkingTree(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := git.New(exec.New())
	ctx := context.Background()
	repo, err := c.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "switch", "-q", "-c", "f")
	gittest.Commit(t, dir, "f.txt", "f", "feat: f")
	gittest.WriteFile(t, dir, "untracked.txt", "u")
	if err := c.PointHEAD(ctx, repo, "main"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := c.CurrentBranch(ctx, repo); cur != "main" {
		t.Errorf("current = %q", cur)
	}
	if got := gittest.Run(t, dir, "diff", "--cached", "--name-only"); got != "f.txt" {
		t.Errorf("staged = %q", got)
	}
	for _, f := range []string{"f.txt", "untracked.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if err := c.PointHEAD(ctx, repo, "nope"); err == nil {
		t.Error("PointHEAD must refuse a branch that does not exist")
	}
}

// A rename moves the ref, the checkout and the branch's config with it.
func TestRenameBranch(t *testing.T) {
	c, repo, dir := remoteFixture(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "-c", "f")
	tip := gittest.Commit(t, dir, "f.txt", "f", "f")
	gittest.Run(t, dir, "push", "-q", "-u", "origin", "f")
	if err := c.RenameBranch(ctx, repo, "f", "g"); err != nil {
		t.Fatal(err)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "g" || gittest.Run(t, dir, "rev-parse", "g") != tip {
		t.Errorf("current = %s", cur)
	}
	if _, ok, _ := c.Tip(ctx, repo, "refs/heads/f"); ok {
		t.Error("f should be gone")
	}
	if remote := c.RemoteFor(ctx, repo, "g"); remote != "origin" {
		t.Errorf("g's remote = %s", remote)
	}
	if err := c.RenameBranch(ctx, repo, "g", "main"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("renaming onto main = %v", err)
	}
}
