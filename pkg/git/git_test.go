package git_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func newClient() *git.Client { return git.New(exec.New()) }

func TestDiscoverFromSubdirectoryAndWorktree(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.WriteFile(t, dir, "sub/dir/x", "")
	c := newClient()
	ctx := context.Background()

	repo, err := c.Discover(ctx, filepath.Join(dir, "sub", "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if repo.TopLevel != realPath(t, dir) {
		t.Errorf("TopLevel = %q, want %q", repo.TopLevel, dir)
	}
	if repo.GitDir != repo.CommonDir || repo.IsLinkedWorktree() {
		t.Errorf("main worktree should have GitDir == CommonDir: %+v", repo)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "wt-branch", wt)
	wrepo, err := c.Discover(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !wrepo.IsLinkedWorktree() || wrepo.CommonDir != repo.CommonDir {
		t.Errorf("linked worktree not detected: %+v", wrepo)
	}

	_, err = c.Discover(ctx, t.TempDir())
	if !errors.Is(err, git.ErrNotRepo) {
		t.Errorf("Discover outside repo = %v, want ErrNotRepo", err)
	}
}

func TestBranchesAndSwitch(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := newClient()
	ctx := context.Background()
	repo, err := c.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.CreateBranch(ctx, repo, "feat/a", "main"); err != nil {
		t.Fatal(err)
	}
	if err := c.Switch(ctx, repo, "feat/a"); err != nil {
		t.Fatal(err)
	}
	head := gittest.Commit(t, dir, "a.txt", "a", "add a")

	cur, err := c.CurrentBranch(ctx, repo)
	if err != nil || cur != "feat/a" {
		t.Fatalf("CurrentBranch = %q, %v", cur, err)
	}

	branches, err := c.Branches(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(branches))
	for _, b := range branches {
		names = append(names, b.Name)
	}
	if !slices.Equal(names, []string{"feat/a", "main"}) {
		t.Errorf("Branches = %v", names)
	}
	a := branches[0]
	if a.Head != head || a.Worktree != realPath(t, dir) || a.CommitTime.IsZero() {
		t.Errorf("branch a = %+v, want head %s in worktree %s", a, head, dir)
	}
	if branches[1].Worktree != "" {
		t.Errorf("main should not be checked out: %+v", branches[1])
	}

	if ok, _ := c.BranchExists(ctx, repo, "feat/a"); !ok {
		t.Error("BranchExists(feat/a) = false")
	}
	if ok, _ := c.BranchExists(ctx, repo, "nope"); ok {
		t.Error("BranchExists(nope) = true")
	}

	if err := c.Switch(ctx, repo, "does-not-exist"); err == nil {
		t.Error("Switch to missing branch should fail")
	}

	gittest.Run(t, dir, "checkout", "-q", "--detach")
	if _, err := c.CurrentBranch(ctx, repo); !errors.Is(err, git.ErrDetached) {
		t.Errorf("detached HEAD: got %v", err)
	}
}

func TestAncestryAndCounts(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := newClient()
	ctx := context.Background()
	repo, _ := c.Discover(ctx, dir)

	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	gittest.Commit(t, dir, "f1", "1", "f1")
	gittest.Commit(t, dir, "f2", "2", "f2")

	n, err := c.CountCommits(ctx, repo, "main", "feat")
	if err != nil || n != 2 {
		t.Errorf("CountCommits = %d, %v; want 2", n, err)
	}
	if ok, err := c.IsAncestor(ctx, repo, "main", "feat"); !ok || err != nil {
		t.Errorf("IsAncestor(main, feat) = %v, %v", ok, err)
	}
	if ok, err := c.IsAncestor(ctx, repo, "feat", "main"); ok || err != nil {
		t.Errorf("IsAncestor(feat, main) = %v, %v", ok, err)
	}
	sha, err := c.RevParse(ctx, repo, "feat")
	if err != nil || len(sha) != 40 {
		t.Errorf("RevParse = %q, %v", sha, err)
	}
	if _, err := c.RevParse(ctx, repo, "nope"); err == nil {
		t.Error("RevParse(nope) should fail")
	}
}

func TestCheckRefFormatAndListCmds(t *testing.T) {
	gittest.Isolate(t)
	c := newClient()
	ctx := context.Background()
	if err := c.CheckRefFormat(ctx, "feat/ok-1"); err != nil {
		t.Errorf("valid name rejected: %v", err)
	}
	for _, bad := range []string{"", "feat..x", "-lead", "a b", "x.lock", "a..b/c"} {
		if err := c.CheckRefFormat(ctx, bad); err == nil {
			t.Errorf("CheckRefFormat(%q) accepted", bad)
		}
	}
	builtins, err := c.ListCmds(ctx, "builtins")
	if err != nil || !slices.Contains(builtins, "checkout") {
		t.Errorf("ListCmds(builtins) = %d entries, %v", len(builtins), err)
	}
}

func TestStagingAndCommit(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := newClient()
	ctx := context.Background()
	repo, _ := c.Discover(ctx, dir)

	gittest.WriteFile(t, dir, "README.md", "changed\n")
	gittest.WriteFile(t, dir, "new.txt", "new\n")

	if staged, _ := c.HasStagedChanges(ctx, repo); staged {
		t.Error("nothing should be staged yet")
	}
	if unstaged, _ := c.HasUnstagedChanges(ctx, repo); !unstaged {
		t.Error("README.md change should be unstaged")
	}
	if untracked, _ := c.HasUntrackedFiles(ctx, repo); !untracked {
		t.Error("new.txt should be untracked")
	}

	if err := c.Add(ctx, repo, git.AddUpdate); err != nil {
		t.Fatal(err)
	}
	if untracked, _ := c.HasUntrackedFiles(ctx, repo); !untracked {
		t.Error("add -u must not stage untracked files")
	}
	if err := c.Add(ctx, repo, git.AddAll); err != nil {
		t.Fatal(err)
	}
	if untracked, _ := c.HasUntrackedFiles(ctx, repo); untracked {
		t.Error("add -A should stage untracked files")
	}

	sha, err := c.Commit(ctx, repo, git.CommitOptions{Message: []string{"subject", "body"}, NoVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if subj, _ := c.Subject(ctx, repo, sha); subj != "subject" {
		t.Errorf("Subject = %q", subj)
	}
	if n, _ := c.CountCommits(ctx, repo, "HEAD~1", "HEAD"); n != 1 {
		t.Errorf("expected exactly one new commit, got %d", n)
	}

	sha2, err := c.Commit(ctx, repo, git.CommitOptions{Amend: true, Message: []string{"amended"}})
	if err != nil {
		t.Fatal(err)
	}
	if sha2 == sha {
		t.Error("amend should produce a new commit id")
	}
	if subj, _ := c.Subject(ctx, repo, "HEAD"); subj != "amended" {
		t.Errorf("Subject after amend = %q", subj)
	}

	// Editor path requires a TTY; the plain runner must refuse rather than hang.
	if _, err := c.Commit(ctx, repo, git.CommitOptions{Amend: true, Edit: true}); !errors.Is(err, exec.ErrTTYUnavailable) {
		t.Errorf("editor commit without TTY = %v, want ErrTTYUnavailable", err)
	}
}

func TestConfig(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := newClient()
	ctx := context.Background()
	repo, _ := c.Discover(ctx, dir)

	if _, ok, err := c.ConfigGet(ctx, repo, git.ScopeMerged, "stack.missing"); ok || err != nil {
		t.Errorf("missing key: ok=%v err=%v", ok, err)
	}
	if err := c.ConfigSet(ctx, git.Repo{}, git.ScopeGlobal, "stack.branchPrefix", "dom/"); err != nil {
		t.Fatal(err)
	}
	if err := c.ConfigSet(ctx, repo, git.ScopeLocal, "stack.ai.model", "haiku"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := c.ConfigGet(ctx, repo, git.ScopeMerged, "stack.branchPrefix")
	if err != nil || !ok || v != "dom/" {
		t.Errorf("ConfigGet global via merged = %q, %v, %v", v, ok, err)
	}
	if _, ok, _ := c.ConfigGet(ctx, repo, git.ScopeLocal, "stack.branchPrefix"); ok {
		t.Error("global key should not be visible in local scope")
	}

	for _, a := range []string{"c", "co"} {
		if err := c.ConfigAdd(ctx, git.Repo{}, git.ScopeGlobal, "stack.managedAliases", a); err != nil {
			t.Fatal(err)
		}
	}
	all, err := c.ConfigGetAll(ctx, git.Repo{}, git.ScopeGlobal, "stack.managedAliases")
	if err != nil || !slices.Equal(all, []string{"c", "co"}) {
		t.Errorf("ConfigGetAll = %v, %v", all, err)
	}
	if err := c.ConfigUnsetValue(ctx, git.Repo{}, git.ScopeGlobal, "stack.managedAliases", "c"); err != nil {
		t.Fatal(err)
	}
	all, _ = c.ConfigGetAll(ctx, git.Repo{}, git.ScopeGlobal, "stack.managedAliases")
	if !slices.Equal(all, []string{"co"}) {
		t.Errorf("after unset value: %v", all)
	}

	entries, err := c.ConfigGetRegexp(ctx, repo, git.ScopeMerged, `^stack\.`)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Key+"="+e.Value)
	}
	slices.Sort(keys)
	want := []string{"stack.ai.model=haiku", "stack.branchprefix=dom/", "stack.managedaliases=co"}
	if !slices.Equal(keys, want) {
		t.Errorf("ConfigGetRegexp = %v, want %v", keys, want)
	}

	if err := c.ConfigUnset(ctx, repo, git.ScopeLocal, "stack.ai.model"); err != nil {
		t.Fatal(err)
	}
	if err := c.ConfigUnset(ctx, repo, git.ScopeLocal, "stack.ai.model"); err != nil {
		t.Errorf("unsetting a missing key should not fail: %v", err)
	}
	if _, ok, _ := c.ConfigGet(ctx, repo, git.ScopeMerged, "stack.ai.model"); ok {
		t.Error("key should be gone")
	}
}

// realPath resolves symlinks (macOS /var → /private/var) so paths compare equal
// to git's absolute output.
func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
