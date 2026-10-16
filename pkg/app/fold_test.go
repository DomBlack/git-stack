package app_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// withPRs gives the fixture a PR cache holding prs.
func withPRs(t *testing.T, f *restackFixture, prs ...forge.PullRequest) {
	t.Helper()
	c := cache.New(f.repo)
	if err := cache.Write(c, "prs", app.PRSnapshot{PRs: prs}); err != nil {
		t.Fatal(err)
	}
	f.app = app.New(app.Deps{Git: f.fb.git, Meta: f.fb, Tracker: f.fb, Cache: c})
}

func pendingQueue(t *testing.T, f *restackFixture) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.repo.CommonDir, "git-stack", "remote-stacks.json"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFoldTopBranchIntoItsParent(t *testing.T) {
	f := newRestackFixture(t) // main -> a -> b -> c, HEAD on c
	tip := f.rev(t, "c")
	res, err := f.app.Fold(context.Background(), f.repo, app.FoldOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Into != "b" || res.Folded != "c" || res.Closing != 0 {
		t.Errorf("result = %+v", res)
	}
	if f.rev(t, "b") != tip || branchExists(t, f.dir, "c") {
		t.Error("b should be at c's old tip and c gone")
	}
	if got := stackNames(f); !slices.EqualFunc(got, [][]string{{"a", "b"}}, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "b" {
		t.Errorf("current = %s", cur)
	}
	if st := gittest.Run(t, f.dir, "status", "--porcelain"); st != "" {
		t.Errorf("working tree should be clean: %q", st)
	}
	if !f.has(t, "b", "c.txt") {
		t.Error("b should have c's file")
	}
	if f.fb.graph.Stacks[0].Branches[1].Head != tip {
		t.Errorf("b's recorded head = %s", f.fb.graph.Stacks[0].Branches[1].Head)
	}
	if q := pendingQueue(t, f); q != "" {
		t.Errorf("nothing should be queued without a pull request: %s", q)
	}
}

// Folding a middle branch leaves the one above where it is: it already has
// every commit, only its parent changes.
func TestFoldMiddleBranchKeepsTheBranchAbove(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "b")
	bTip, cTip, cBase := f.rev(t, "b"), f.rev(t, "c"), f.base("c")
	res, err := f.app.Fold(context.Background(), f.repo, app.FoldOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Into != "a" || res.Folded != "b" {
		t.Errorf("result = %+v", res)
	}
	if got := stackNames(f); !slices.EqualFunc(got, [][]string{{"a", "c"}}, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if f.rev(t, "a") != bTip || f.rev(t, "c") != cTip || f.base("c") != cBase {
		t.Error("a should be at b's tip, c untouched with its base")
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "a" {
		t.Errorf("current = %s", cur)
	}
	for _, file := range []string{"a.txt", "b.txt", "c.txt"} {
		if !f.has(t, "c", file) {
			t.Errorf("c lost %s", file)
		}
	}
}

func TestFoldKeepTakesTheParentsPlace(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "b")
	bTip, aBase := f.rev(t, "b"), f.base("a")
	res, err := f.app.Fold(context.Background(), f.repo, app.FoldOptions{Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Into != "b" || res.Folded != "a" {
		t.Errorf("result = %+v", res)
	}
	if got := stackNames(f); !slices.EqualFunc(got, [][]string{{"b", "c"}}, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if branchExists(t, f.dir, "a") || f.rev(t, "b") != bTip {
		t.Error("a should be deleted and b unmoved")
	}
	if f.base("b") != aBase || aBase != f.rev(t, "main") {
		t.Errorf("b's base = %s, want a's old base %s", f.base("b"), aBase)
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "b" {
		t.Errorf("current = %s", cur)
	}
}

func TestFoldCarriesUncommittedChanges(t *testing.T) {
	f := newRestackFixture(t)
	gittest.WriteFile(t, f.dir, "shared.txt", "edited")
	if _, err := f.app.Fold(context.Background(), f.repo, app.FoldOptions{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "shared.txt"))
	if err != nil || string(data) != "edited" {
		t.Errorf("shared.txt = %q %v", data, err)
	}
}

func TestFoldRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *restackFixture)
		keep  bool
		want  string
	}{
		{"bottom", func(t *testing.T, f *restackFixture) { gittest.Run(t, f.dir, "switch", "-q", "a") }, false, "a is the bottom of its stack"},
		{"trunk", func(t *testing.T, f *restackFixture) { gittest.Run(t, f.dir, "switch", "-q", "main") }, false, "main is a trunk"},
		{"not on parent", func(t *testing.T, f *restackFixture) {
			f.amend(t, "b", "b2.txt")
		}, false, "c isn't on b yet"},
		{"parent elsewhere", func(t *testing.T, f *restackFixture) {
			gittest.Run(t, f.dir, "worktree", "add", "-q", filepath.Join(t.TempDir(), "wt"), "b")
		}, false, "b is checked out in"},
		{"deleted elsewhere", func(t *testing.T, f *restackFixture) {
			gittest.Run(t, f.dir, "worktree", "add", "-q", filepath.Join(t.TempDir(), "wt"), "b")
		}, true, "b is checked out in"},
		{"merged", func(t *testing.T, f *restackFixture) {
			f.fb.graph.Stacks[0].Branches[0].PR = &stack.PRRef{Number: 1, Merged: true}
		}, false, "a has merged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRestackFixture(t)
			tc.setup(t, f)
			before := stackNames(f)
			heads := gittest.Run(t, f.dir, "for-each-ref", "refs/heads/")
			_, err := f.app.Fold(context.Background(), f.repo, app.FoldOptions{Keep: tc.keep})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if kindOf(err) != stack.KindInvalidArgs {
				t.Errorf("kind = %v", kindOf(err))
			}
			if got := stackNames(f); !slices.EqualFunc(got, before, slices.Equal) {
				t.Errorf("stacks changed: %v", got)
			}
			if gittest.Run(t, f.dir, "for-each-ref", "refs/heads/") != heads {
				t.Error("refs changed")
			}
		})
	}
}

func TestFoldUntrackedBranchIsNotInStack(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "-c", "loose")
	_, err := f.app.Fold(context.Background(), f.repo, app.FoldOptions{})
	if kindOf(err) != stack.KindNotInStack {
		t.Errorf("err = %v", err)
	}
}

func TestFoldQueuesThePullRequestToClose(t *testing.T) {
	f := newRestackFixture(t)
	withPRs(t, f,
		forge.PullRequest{Number: 11, Head: "b", Base: "a", State: forge.StateOpen},
		forge.PullRequest{Number: 12, Head: "c", Base: "b", State: forge.StateOpen},
	)
	tip := f.rev(t, "c")
	res, err := f.app.Fold(context.Background(), f.repo, app.FoldOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Closing != 12 {
		t.Errorf("closing = %d", res.Closing)
	}
	q := pendingQueue(t, f)
	for _, want := range []string{`"number":12`, `"branch":"c"`, `"comment":"Folded into #11 (b)"`, `"tip":"` + tip + `"`} {
		if !strings.Contains(q, want) {
			t.Errorf("queue %s lacks %s", q, want)
		}
	}
}

func TestFoldKeepQueuesTheParentsPullRequest(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "b")
	withPRs(t, f, forge.PullRequest{Number: 10, Head: "a", Base: "main", State: forge.StateOpen})
	res, err := f.app.Fold(context.Background(), f.repo, app.FoldOptions{Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Closing != 10 {
		t.Errorf("closing = %d", res.Closing)
	}
	if q := pendingQueue(t, f); !strings.Contains(q, `"number":10`) || !strings.Contains(q, `"comment":"Folded into b"`) {
		t.Errorf("queue = %s", q)
	}
}

func TestFoldDissolvesTheRemoteStackOnce(t *testing.T) {
	f := newRestackFixture(t)
	f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[0].Number = "S1", 1
	if _, err := f.app.Fold(context.Background(), f.repo, app.FoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.fb.graph.Stacks[0].ID != "" || f.fb.graph.Stacks[0].Number != 0 {
		t.Errorf("id = %q %d", f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[0].Number)
	}
	if q := pendingQueue(t, f); strings.Count(q, `"id":"S1"`) != 1 {
		t.Errorf("queue = %s", q)
	}
}
