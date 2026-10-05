package git_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git"
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

// rebaseFixture: main has shared.txt; feat edits it and adds f.txt; main
// then edits shared.txt too, so feat conflicts when rebased onto main.
func rebaseFixture(t *testing.T) (*git.Client, git.Repo, string) {
	t.Helper()
	c, repo, dir := objectsFixture(t)
	gittest.Commit(t, dir, "shared.txt", "base", "shared")
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	gittest.Commit(t, dir, "shared.txt", "feat version", "feat edits shared")
	gittest.Commit(t, dir, "f.txt", "f", "feat adds f")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "shared.txt", "main version", "main edits shared")
	gittest.Run(t, dir, "switch", "-q", "feat")
	return c, repo, dir
}

func TestRebaseMatches(t *testing.T) {
	for _, backend := range []string{"merge", "apply"} {
		for _, worktree := range []string{"main", "linked"} {
			t.Run(backend+"/"+worktree, func(t *testing.T) {
				c, repo, dir := rebaseFixture(t)
				ctx := context.Background()
				onto := gittest.Run(t, dir, "rev-parse", "main")
				tip := gittest.Run(t, dir, "rev-parse", "feat")
				from := gittest.Run(t, dir, "merge-base", "main", "feat")
				if worktree == "linked" {
					gittest.Run(t, dir, "switch", "-q", "main")
					linked := filepath.Join(t.TempDir(), "checkout")
					gittest.Run(t, dir, "worktree", "add", "-q", linked, "feat")
					dir = linked
					var err error
					repo, err = c.Discover(ctx, dir)
					if err != nil {
						t.Fatal(err)
					}
				}
				if matches, err := c.RebaseMatches(ctx, repo, "feat", onto, tip); err != nil || matches {
					t.Fatalf("no rebase: matches = %v, %v", matches, err)
				}
				if _, err := c.Runner().Run(ctx, gitCmd(dir, "rebase", "--"+backend, "--onto", onto, from, "feat")); err == nil {
					t.Fatal("expected a conflict")
				}
				for _, tc := range []struct {
					name, branch, base, tip string
					want                    bool
				}{
					{"ours", "feat", onto, tip, true},
					{"other branch", "other", onto, tip, false},
					{"other base", "feat", from, tip, false},
					{"other tip", "feat", onto, from, false},
				} {
					matches, err := c.RebaseMatches(ctx, repo, tc.branch, tc.base, tc.tip)
					if err != nil || matches != tc.want {
						t.Errorf("%s: matches = %v, %v, want %v", tc.name, matches, err, tc.want)
					}
				}
				if err := c.RebaseAbort(ctx, repo); err != nil {
					t.Fatal(err)
				}
				if matches, err := c.RebaseMatches(ctx, repo, "feat", onto, tip); err != nil || matches {
					t.Fatalf("after abort: matches = %v, %v", matches, err)
				}
			})
		}
	}
}

func TestRebaseOntoStopsOnConflictAndContinues(t *testing.T) {
	c, repo, dir := rebaseFixture(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "main")
	from := gittest.Run(t, dir, "merge-base", "main", "feat")

	stopped, err := c.RebaseOnto(ctx, repo, "main", from, "feat")
	if err != nil || !stopped {
		t.Fatalf("RebaseOnto = %v %v, want stopped", stopped, err)
	}
	if files, _ := c.ConflictedFiles(ctx, repo); !slices.Equal(files, []string{"shared.txt"}) {
		t.Errorf("conflicted = %v", files)
	}
	// Still conflicted: continue stops again, nothing else happens.
	if stopped, err := c.RebaseContinue(ctx, repo); err != nil || !stopped {
		t.Fatalf("premature continue = %v %v", stopped, err)
	}
	gittest.WriteFile(t, dir, "shared.txt", "resolved")
	gittest.Run(t, dir, "add", "shared.txt")
	if stopped, err := c.RebaseContinue(ctx, repo); err != nil || stopped {
		t.Fatalf("continue = %v %v", stopped, err)
	}
	if active, _ := c.RebaseInProgress(ctx, repo); active {
		t.Error("rebase should be finished")
	}
	if gittest.Run(t, dir, "branch", "--show-current") != "feat" {
		t.Error("git rebase ends on the rebased branch")
	}
	if ok, _ := c.IsAncestor(ctx, repo, "main", "feat"); !ok {
		t.Error("feat should now sit on main")
	}
	if log := gittest.Run(t, dir, "log", "--format=%s", "main..feat"); log != "feat adds f\nfeat edits shared" {
		t.Errorf("log = %q", log)
	}
}

func TestRebaseOntoCleanDoesNotStop(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	gittest.Commit(t, dir, "f.txt", "f", "f")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "m.txt", "m", "m")
	from := gittest.Run(t, dir, "merge-base", "main", "feat")
	if stopped, err := c.RebaseOnto(ctx, repo, "main", from, "feat"); err != nil || stopped {
		t.Fatalf("RebaseOnto = %v %v", stopped, err)
	}
	if ok, _ := c.IsAncestor(ctx, repo, "main", "feat"); !ok {
		t.Error("feat should sit on main")
	}
}

func TestRebaseAbortRestoresTheBranch(t *testing.T) {
	c, repo, dir := rebaseFixture(t)
	ctx := context.Background()
	before := gittest.Run(t, dir, "rev-parse", "feat")
	from := gittest.Run(t, dir, "merge-base", "main", "feat")
	if stopped, _ := c.RebaseOnto(ctx, repo, "main", from, "feat"); !stopped {
		t.Fatal("expected a conflict")
	}
	if err := c.RebaseAbort(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if gittest.Run(t, dir, "rev-parse", "feat") != before || gittest.Run(t, dir, "branch", "--show-current") != "feat" {
		t.Error("abort must put feat back and check it out")
	}
}

// Git drops a commit whose resolution is empty on --continue (the default
// --empty=drop); this pins the behaviour we rely on.
func TestRebaseContinueDropsEmptyResolution(t *testing.T) {
	c, repo, dir := rebaseFixture(t)
	ctx := context.Background()
	from := gittest.Run(t, dir, "merge-base", "main", "feat")
	if stopped, _ := c.RebaseOnto(ctx, repo, "main", from, "feat"); !stopped {
		t.Fatal("expected a conflict")
	}
	// Resolving to exactly main's content leaves the commit empty.
	gittest.WriteFile(t, dir, "shared.txt", "main version")
	gittest.Run(t, dir, "add", "shared.txt")
	if stopped, err := c.RebaseContinue(ctx, repo); err != nil || stopped {
		t.Fatalf("continue = %v %v", stopped, err)
	}
	if active, _ := c.RebaseInProgress(ctx, repo); active {
		t.Error("rebase should be finished")
	}
	if log := gittest.Run(t, dir, "log", "--format=%s", "main..feat"); log != "feat adds f" {
		t.Errorf("log = %q, want the emptied commit dropped", log)
	}
}

func TestRebaseOntoStopsWithRerereResolutionStaged(t *testing.T) {
	c, repo, dir := rebaseFixture(t)
	ctx := context.Background()
	gittest.Run(t, dir, "config", "rerere.enabled", "true")
	// Without autoupdate rerere only rewrites the file and leaves it unmerged.
	gittest.Run(t, dir, "config", "rerere.autoupdate", "true")
	from := gittest.Run(t, dir, "merge-base", "main", "feat")
	featTip := gittest.Run(t, dir, "rev-parse", "feat")
	// Teach rerere the resolution once.
	if stopped, _ := c.RebaseOnto(ctx, repo, "main", from, "feat"); !stopped {
		t.Fatal("expected the first conflict")
	}
	gittest.WriteFile(t, dir, "shared.txt", "resolved")
	gittest.Run(t, dir, "add", "shared.txt")
	if stopped, err := c.RebaseContinue(ctx, repo); err != nil || stopped {
		t.Fatalf("continue = %v %v", stopped, err)
	}
	// The rebase left feat checked out; put it back where it started.
	gittest.Run(t, dir, "reset", "-q", "--hard", featTip)
	// Second time round git stages the remembered resolution and stops with nothing unmerged.
	stopped, err := c.RebaseOnto(ctx, repo, "main", from, "feat")
	if err != nil || !stopped {
		t.Fatalf("RebaseOnto with rerere = %v %v, want stopped", stopped, err)
	}
	if files, _ := c.ConflictedFiles(ctx, repo); len(files) != 0 {
		t.Errorf("rerere should have staged the resolution, got unmerged %v", files)
	}
	if stopped, err := c.RebaseContinue(ctx, repo); err != nil || stopped {
		t.Fatalf("continue after rerere = %v %v", stopped, err)
	}
}
