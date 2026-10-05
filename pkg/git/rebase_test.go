package git_test

import (
	"context"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestReplayMovesCommitsAndKeepsAuthors(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	t.Setenv("GIT_AUTHOR_NAME", "Ada")
	c1 := gittest.Commit(t, dir, "1.txt", "1", "one")
	c2 := gittest.Commit(t, dir, "2.txt", "2", "two\n\nbody")
	gittest.Run(t, dir, "switch", "-q", "main")
	mainTip := gittest.Commit(t, dir, "m.txt", "m", "main moves")

	res, err := c.Replay(ctx, repo, []string{c1, c2}, mainTip)
	if err != nil || res.Conflict != nil || res.Replayed != 2 || res.Tip == "" {
		t.Fatalf("Replay = %+v %v", res, err)
	}
	log := gittest.Run(t, dir, "log", "--format=%an %s", mainTip+".."+res.Tip)
	if log != "Ada two\nAda one" {
		t.Errorf("log = %q", log)
	}
	if out := gittest.Run(t, dir, "ls-tree", "--name-only", res.Tip); !strings.Contains(out, "1.txt") || !strings.Contains(out, "2.txt") || !strings.Contains(out, "m.txt") {
		t.Errorf("tree = %q", out)
	}
	if gittest.Run(t, dir, "rev-parse", "feat") != c2 {
		t.Error("Replay must not move refs")
	}
	if body := gittest.Run(t, dir, "log", "-1", "--format=%B", res.Tip); body != "two\n\nbody" {
		t.Errorf("message = %q", body)
	}
}

func TestReplayNothing(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	tip := gittest.Run(t, dir, "rev-parse", "HEAD")
	res, err := c.Replay(context.Background(), repo, nil, tip)
	if err != nil || res.Tip != tip || res.Replayed != 0 {
		t.Errorf("Replay(nil) = %+v %v", res, err)
	}
}

func TestReplayStopsAtConflict(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	c1 := gittest.Commit(t, dir, "1.txt", "1", "one")
	c2 := gittest.Commit(t, dir, "README.md", "ours", "clash")
	gittest.Run(t, dir, "switch", "-q", "main")
	mainTip := gittest.Commit(t, dir, "README.md", "theirs", "main clash")

	res, err := c.Replay(ctx, repo, []string{c1, c2}, mainTip)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflict == nil || res.Conflict.Commit != c2 || len(res.Conflict.Files) != 1 || res.Conflict.Files[0] != "README.md" {
		t.Errorf("conflict = %+v", res.Conflict)
	}
	if res.Tip != "" || res.Replayed != 1 {
		t.Errorf("a conflicted replay has no tip: %+v", res)
	}
}

func TestReplayDropsCommitsAlreadyInTheParent(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	c1 := gittest.Commit(t, dir, "1.txt", "1", "one")
	c2 := gittest.Commit(t, dir, "2.txt", "2", "two")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Run(t, dir, "cherry-pick", c1)
	mainTip := gittest.Run(t, dir, "rev-parse", "HEAD")

	res, err := c.Replay(ctx, repo, []string{c1, c2}, mainTip)
	if err != nil || res.Conflict != nil || res.Replayed != 1 || res.Skipped != 1 {
		t.Fatalf("Replay = %+v %v", res, err)
	}
	if log := gittest.Run(t, dir, "log", "--format=%s", mainTip+".."+res.Tip); log != "two" {
		t.Errorf("log = %q", log)
	}
}

func TestReplayOfOnlyLandedCommitsIsOnto(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	c1 := gittest.Commit(t, dir, "1.txt", "1", "one")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Run(t, dir, "cherry-pick", c1)
	mainTip := gittest.Run(t, dir, "rev-parse", "HEAD")

	res, err := c.Replay(context.Background(), repo, []string{c1}, mainTip)
	if err != nil || res.Tip != mainTip || res.Replayed != 0 || res.Skipped != 1 {
		t.Errorf("Replay = %+v %v", res, err)
	}
}
