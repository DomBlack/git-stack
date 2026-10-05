package git_test

import (
	"context"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func objectsFixture(t *testing.T) (*git.Client, git.Repo, string) {
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

func TestCommitInfo(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	t.Setenv("GIT_AUTHOR_NAME", "Ada")
	t.Setenv("GIT_AUTHOR_EMAIL", "ada@example.com")
	t.Setenv("GIT_AUTHOR_DATE", "2026-03-04T05:06:07Z")
	parent := gittest.Run(t, dir, "rev-parse", "HEAD")
	gittest.WriteFile(t, dir, "a.txt", "a")
	gittest.Run(t, dir, "add", "a.txt")
	gittest.Run(t, dir, "commit", "-q", "-m", "subject line\n\nbody para")
	info, err := c.CommitInfo(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if info.Author != "Ada" || info.Email != "ada@example.com" || !strings.HasPrefix(info.Date, "2026-03-04T05:06:07") {
		t.Errorf("author = %+v", info)
	}
	if info.Message != "subject line\n\nbody para" || info.Parent != parent || info.Tree != gittest.Run(t, dir, "rev-parse", "HEAD^{tree}") {
		t.Errorf("info = %+v", info)
	}
}

func TestMergeTreeCleanAndConflict(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	base := gittest.Run(t, dir, "rev-parse", "HEAD")
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	feat := gittest.Commit(t, dir, "f.txt", "feat", "feat")
	gittest.Run(t, dir, "switch", "-q", "main")
	mainTip := gittest.Commit(t, dir, "m.txt", "main", "main moves")

	res, err := c.MergeTree(ctx, repo, base, mainTip, feat)
	if err != nil || len(res.Conflicts) != 0 || res.Tree == "" {
		t.Fatalf("clean merge: %+v %v", res, err)
	}
	if out := gittest.Run(t, dir, "ls-tree", "--name-only", res.Tree); !strings.Contains(out, "f.txt") || !strings.Contains(out, "m.txt") {
		t.Errorf("merged tree = %q", out)
	}

	// Both sides edit README.md: conflict, named file, no error.
	clash := gittest.Commit(t, dir, "README.md", "theirs", "clash")
	gittest.Run(t, dir, "switch", "-q", "feat")
	feat2 := gittest.Commit(t, dir, "README.md", "ours", "feat clash")
	res, err = c.MergeTree(ctx, repo, feat, clash, feat2)
	if err != nil || len(res.Conflicts) != 1 || res.Conflicts[0] != "README.md" {
		t.Errorf("conflict: %+v %v", res, err)
	}
}

func TestCommitTreeKeepsAuthorAndMessage(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	parent := gittest.Run(t, dir, "rev-parse", "HEAD")
	tree := gittest.Run(t, dir, "rev-parse", "HEAD^{tree}")
	info := git.CommitInfo{Author: "Ada", Email: "ada@example.com", Date: "2026-03-04T05:06:07Z", Message: "replayed\n\nwith body"}
	sha, err := c.CommitTree(ctx, repo, tree, parent, info)
	if err != nil || len(sha) != 40 {
		t.Fatalf("CommitTree = %q %v", sha, err)
	}
	got := gittest.Run(t, dir, "log", "-1", "--format=%an|%ae|%aI|%P|%B", sha)
	if !strings.HasPrefix(got, "Ada|ada@example.com|2026-03-04T05:06:07") || !strings.Contains(got, "|"+parent+"|replayed\n\nwith body") {
		t.Errorf("log = %q", got)
	}
}

func TestPatchIDsSurviveARebase(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	c1 := gittest.Commit(t, dir, "1.txt", "1", "one")
	c2 := gittest.Commit(t, dir, "2.txt", "2", "two")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "m.txt", "m", "main moves")
	gittest.Run(t, dir, "switch", "-q", "-c", "feat2", "main")
	gittest.Run(t, dir, "cherry-pick", c1, c2)

	a, err := c.PatchIDs(ctx, repo, "main", "feat")
	if err != nil || len(a) != 2 {
		t.Fatalf("PatchIDs(feat) = %v %v", a, err)
	}
	b, err := c.PatchIDs(ctx, repo, "main", "feat2")
	if err != nil || len(b) != 2 {
		t.Fatalf("PatchIDs(feat2) = %v %v", b, err)
	}
	for id, sha := range a {
		if _, ok := b[id]; !ok {
			t.Errorf("patch id of %s missing after rebase", sha)
		}
		if sha != c1 && sha != c2 {
			t.Errorf("unexpected commit %s", sha)
		}
	}
}
