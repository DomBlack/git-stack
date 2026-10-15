package git_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func patchFixture(t *testing.T) (*git.Client, git.Repo, string) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Commit(t, dir, "f.txt", "l1\nl2\nl3\nl4\nl5\n", "five lines")
	c := git.New(exec.New())
	repo, err := c.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, repo, dir
}

func TestDiffCommitAndStagedPatchAreZeroContextWithRenames(t *testing.T) {
	c, repo, dir := patchFixture(t)
	ctx := context.Background()
	// A user's diff settings must not leak into what we parse.
	gittest.Run(t, dir, "config", "diff.noprefix", "true")
	gittest.Run(t, dir, "config", "diff.context", "3")
	gittest.Run(t, dir, "mv", "f.txt", "g.txt")
	gittest.WriteFile(t, dir, "g.txt", "l1\nL2\nl3\nl4\nl5\n")
	gittest.Run(t, dir, "add", "g.txt")
	staged, err := c.StagedPatch(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rename from f.txt", "rename to g.txt", "--- a/f.txt", "+++ b/g.txt", "@@ -2 +2 @@", "-l2", "+L2"} {
		if !strings.Contains(staged, want) {
			t.Errorf("staged patch lacks %q:\n%s", want, staged)
		}
	}
	// Git puts a function heading after the @@ (here " l1"), so look for
	// context lines by their leading space rather than for their text.
	for line := range strings.SplitSeq(staged, "\n") {
		if strings.HasPrefix(line, " ") {
			t.Errorf("staged patch has a context line %q:\n%s", line, staged)
		}
	}
	gittest.Run(t, dir, "commit", "-q", "-m", "rename and edit")
	diff, err := c.DiffCommit(ctx, repo, "HEAD")
	if err != nil || !strings.Contains(diff, "rename from f.txt") || !strings.Contains(diff, "+L2") {
		t.Errorf("DiffCommit = %q, %v", diff, err)
	}
}

func TestHasMerges(t *testing.T) {
	c, repo, dir := patchFixture(t)
	ctx := context.Background()
	base := gittest.Run(t, dir, "rev-parse", "HEAD")
	gittest.Run(t, dir, "switch", "-q", "-c", "side")
	gittest.Commit(t, dir, "s.txt", "s", "side")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "m.txt", "m", "main")
	if has, err := c.HasMerges(ctx, repo, base, "HEAD"); has || err != nil {
		t.Errorf("linear: %v %v", has, err)
	}
	gittest.Run(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	if has, err := c.HasMerges(ctx, repo, base, "HEAD"); !has || err != nil {
		t.Errorf("with a merge: %v %v", has, err)
	}
}

const editL2 = "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -2,1 +2,1 @@\n-l2\n+L2\n"

func TestPatchTree(t *testing.T) {
	c, repo, dir := patchFixture(t)
	ctx := context.Background()
	tree := gittest.Run(t, dir, "rev-parse", "HEAD^{tree}")
	index := gittest.Run(t, dir, "rev-parse", "HEAD^{tree}") // unchanged below
	nt, err := c.PatchTree(ctx, repo, tree, editL2)
	if err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, dir, "cat-file", "-p", nt+":f.txt"); got != "l1\nL2\nl3\nl4\nl5" {
		t.Errorf("patched content = %q", got)
	}
	// The real index and working tree are untouched, and the temporary
	// index is gone.
	if gittest.Run(t, dir, "write-tree") != index || gittest.Run(t, dir, "status", "--porcelain") != "" {
		t.Error("the real index or working tree changed")
	}
	if left, _ := filepath.Glob(filepath.Join(repo.GitDir, "git-stack-absorb-*")); len(left) != 0 {
		t.Errorf("temporary index left behind: %v", left)
	}
}

func TestPatchTreeRejectsAPatchThatDoesNotFit(t *testing.T) {
	c, repo, dir := patchFixture(t)
	tree := gittest.Run(t, dir, "rev-parse", "HEAD^{tree}")
	bad := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -2,1 +2,1 @@\n-nope\n+L2\n"
	_, err := c.PatchTree(context.Background(), repo, tree, bad)
	pe, ok := errors.AsType[*git.PatchError](err)
	if !ok || !strings.Contains(pe.Detail, "patch does not apply") {
		t.Errorf("err = %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(repo.GitDir, "git-stack-absorb-*")); len(left) != 0 {
		t.Errorf("temporary index left behind: %v", left)
	}
}

func TestPatchTreeRefusesAnOffsetApply(t *testing.T) {
	c, repo, dir := patchFixture(t)
	tree := gittest.Run(t, dir, "rev-parse", "HEAD^{tree}")
	// The header says line 4 but l2 is on line 2: git would apply it at an
	// offset.
	moved := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -4,1 +4,1 @@\n-l2\n+L2\n"
	_, err := c.PatchTree(context.Background(), repo, tree, moved)
	pe, ok := errors.AsType[*git.PatchError](err)
	if !ok || !strings.Contains(pe.Detail, "offset") {
		t.Errorf("err = %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(repo.GitDir, "git-stack-absorb-*")); len(left) != 0 {
		t.Errorf("temporary index left behind: %v", left)
	}
}

func TestPatchTreeIgnoresWhitespaceConfig(t *testing.T) {
	c, repo, dir := patchFixture(t)
	gittest.Run(t, dir, "config", "apply.whitespace", "fix")
	tree := gittest.Run(t, dir, "rev-parse", "HEAD^{tree}")
	trailing := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -2,1 +2,1 @@\n-l2\n+L2 \n"
	nt, err := c.PatchTree(context.Background(), repo, tree, trailing)
	if err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, dir, "cat-file", "-p", nt+":f.txt"); !strings.Contains(got, "L2 \n") {
		t.Errorf("trailing space was 'fixed': %q", got)
	}
}

func TestPatchTreeOddPath(t *testing.T) {
	c, repo, dir := patchFixture(t)
	gittest.Commit(t, dir, "sp ace/café.txt", "a\nb\n", "odd path")
	tree := gittest.Run(t, dir, "rev-parse", "HEAD^{tree}")
	patch := "diff --git \"a/sp ace/caf\\303\\251.txt\" \"b/sp ace/caf\\303\\251.txt\"\n--- \"a/sp ace/caf\\303\\251.txt\"\n+++ \"b/sp ace/caf\\303\\251.txt\"\n@@ -2,1 +2,1 @@\n-b\n+B\n"
	nt, err := c.PatchTree(context.Background(), repo, tree, patch)
	if err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, dir, "cat-file", "-p", nt+":sp ace/café.txt"); got != "a\nB" {
		t.Errorf("content = %q", got)
	}
}
