package git_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestDiffsLogsAndRebaseState(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	c := newClient()
	ctx := context.Background()
	repo, _ := c.Discover(ctx, dir)

	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	gittest.Commit(t, dir, "f.txt", "one\n", "feat: one\n\nbody one")
	gittest.Commit(t, dir, "f.txt", "two\n", "feat: two")

	diff, err := c.DiffRange(ctx, repo, "main", "feat")
	if err != nil || !strings.Contains(diff, "+two") {
		t.Errorf("DiffRange = %q, %v", diff, err)
	}
	gittest.WriteFile(t, dir, "f.txt", "three\n")
	gittest.Run(t, dir, "add", "f.txt")
	staged, err := c.StagedDiff(ctx, repo)
	if err != nil || !strings.Contains(staged, "+three") || strings.Contains(staged, "+two") {
		t.Errorf("StagedDiff = %q, %v", staged, err)
	}
	gittest.Run(t, dir, "reset", "-q", "--hard")

	subjects, err := c.Subjects(ctx, repo, "feat", 2)
	if err != nil || !slices.Equal(subjects, []string{"feat: two", "feat: one"}) {
		t.Errorf("Subjects = %v, %v", subjects, err)
	}
	msgs, err := c.Messages(ctx, repo, "main", "feat")
	if err != nil || len(msgs) != 2 || msgs[0] != "feat: one\n\nbody one" || msgs[1] != "feat: two" {
		t.Errorf("Messages = %q, %v", msgs, err)
	}

	if in, _ := c.RebaseInProgress(ctx, repo); in {
		t.Error("no rebase should be in progress")
	}
	// Create a conflict: main changes f.txt differently, then rebase feat.
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "f.txt", "main\n", "main change")
	gittest.Run(t, dir, "switch", "-q", "feat")
	if _, err := c.Runner().Run(ctx, gitCmd(dir, "rebase", "main")); err == nil {
		t.Fatal("expected rebase conflict")
	}
	if in, err := c.RebaseInProgress(ctx, repo); !in || err != nil {
		t.Errorf("RebaseInProgress = %v, %v", in, err)
	}
	files, err := c.ConflictedFiles(ctx, repo)
	if err != nil || !slices.Equal(files, []string{"f.txt"}) {
		t.Errorf("ConflictedFiles = %v, %v", files, err)
	}
	gittest.Run(t, dir, "rebase", "--abort")
	if in, _ := c.RebaseInProgress(ctx, repo); in {
		t.Error("rebase should be aborted")
	}
}

func TestAddedPathsAndUntracked(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	base := gittest.Run(t, dir, "rev-parse", "HEAD")
	gittest.Commit(t, dir, "README.md", "changed", "edit")
	tip := gittest.Commit(t, dir, "sub/new file.txt", "n", "add")
	added, err := c.AddedPaths(ctx, repo, base, tip)
	if err != nil || !slices.Equal(added, []string{"sub/new file.txt"}) {
		t.Errorf("AddedPaths = %q %v", added, err)
	}
	gittest.WriteFile(t, dir, "notes.md", "mine")
	gittest.WriteFile(t, dir, ".gitignore", "*.log\n")
	gittest.WriteFile(t, dir, "x.log", "ignored")
	untracked, err := c.Untracked(ctx, repo)
	if err != nil || !slices.Equal(untracked, []string{".gitignore", "notes.md"}) {
		t.Errorf("Untracked = %q %v", untracked, err)
	}
}

func TestLocalChangesAndChangedPaths(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	gittest.Commit(t, dir, "a.txt", "a", "a")
	base := gittest.Run(t, dir, "rev-parse", "HEAD")
	tip := gittest.Commit(t, dir, "b.txt", "b", "b")

	if got, err := c.LocalChanges(ctx, repo); err != nil || len(got) != 0 {
		t.Fatalf("clean LocalChanges = %v %v", got, err)
	}
	gittest.WriteFile(t, dir, "a.txt", "a2") // unstaged
	gittest.WriteFile(t, dir, "c.txt", "c")  // untracked: not a local change to HEAD
	gittest.WriteFile(t, dir, "b.txt", "b2")
	gittest.Run(t, dir, "add", "b.txt") // staged
	got, err := c.LocalChanges(ctx, repo)
	if err != nil || !slices.Equal(got, []string{"a.txt", "b.txt"}) {
		t.Errorf("LocalChanges = %v %v", got, err)
	}
	changed, err := c.ChangedPaths(ctx, repo, base, tip)
	if err != nil || !slices.Equal(changed, []string{"b.txt"}) {
		t.Errorf("ChangedPaths = %v %v", changed, err)
	}
}
