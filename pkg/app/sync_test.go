package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// TestSyncPrunePolicy covers stack.sync.prune: always (default) prunes with
// no question, never keeps, and -f beats never.
func TestSyncPrunePolicy(t *testing.T) {
	deps, sf, fg, repo, _ := submitFixture(t)
	fg.prs[0].State = forge.StateMerged
	ap := &askPrompter{answer: false}
	deps.Prompter = ap
	res, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !sf.syncs[0].Prune || !res.Pruned || len(ap.asked) != 0 {
		t.Errorf("always: prune without asking, got %+v asked=%v", sf.syncs, ap.asked)
	}

	deps2, sf2, _, repo2, _ := submitFixture(t)
	deps2.Config.SyncPrune = config.SyncPruneNever
	res2, err := app.New(deps2).Sync(context.Background(), repo2, app.SyncOptions{})
	if err != nil || sf2.syncs[0].Prune || res2.Pruned {
		t.Errorf("never: must not prune, got %+v %v", sf2.syncs, err)
	}

	deps3, sf3, _, repo3, _ := submitFixture(t)
	deps3.Config.SyncPrune = config.SyncPruneNever
	if _, err := app.New(deps3).Sync(context.Background(), repo3, app.SyncOptions{Prune: true}); err != nil || !sf3.syncs[0].Prune {
		t.Errorf("-f beats never, got %+v %v", sf3.syncs, err)
	}
}

// TestSyncCoversEveryStack: like gt, every stack is synced. The current
// stack goes first, a stack in this worktree that isn't checked out gets a
// branch checked out and the original restored, a stack in a linked
// worktree runs there, and one that isn't checked out there is handled the
// same way in that worktree.
func TestSyncCoversEveryStack(t *testing.T) {
	deps, sf, _, repo, dir := submitFixture(t) // on b; stack a,b
	cur := func(d string) string { return gittest.Run(t, d, "branch", "--show-current") }
	gittest.Run(t, dir, "branch", "c", "main") // stack c, not checked out
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "w1", wt)
	gittest.Run(t, dir, "branch", "zz", "main") // stack zz, home is the worktree, not checked out
	wts, err := deps.Git.Worktrees(context.Background(), repo)
	if err != nil || len(wts) != 2 {
		t.Fatalf("worktrees = %+v %v", wts, err)
	}
	wtPath := wts[1].Path

	graph := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "c"}}, Worktree: repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "zz"}}, Worktree: wtPath},
		{Trunk: "main", Branches: []stack.Branch{{Name: "w1"}}, Worktree: wtPath},
	})
	deps.Meta = memMeta{graph}
	sf.graph = graph
	// Record where each sync ran from.
	var ranFrom []string
	sf.onSync = func(o stack.SyncOptions) {
		d := dir
		if o.Dir != "" {
			d = o.Dir
		}
		ranFrom = append(ranFrom, cur(d))
	}

	res, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := []string{"b", "c", "w1", "zz"}
	if strings.Join(ranFrom, ",") != strings.Join(wantFrom, ",") {
		t.Errorf("synced from %v, want %v", ranFrom, wantFrom)
	}
	if len(sf.syncs) != 4 || sf.syncs[0].Dir != "" || sf.syncs[1].Dir != "" || sf.syncs[2].Dir != wtPath || sf.syncs[3].Dir != wtPath {
		t.Errorf("dirs = %+v", sf.syncs)
	}
	if cur(dir) != "b" || cur(wt) != "w1" {
		t.Errorf("worktrees should be back where they were: main=%s wt=%s", cur(dir), cur(wt))
	}
	if len(res.Stacks) != 4 || res.Stacks[0].CheckedOut || !res.Stacks[1].CheckedOut || res.Stacks[2].CheckedOut || !res.Stacks[3].CheckedOut {
		t.Errorf("stacks = %+v", res.Stacks)
	}
	if len(res.Notices) != 0 {
		t.Errorf("unexpected notices: %v", res.Notices)
	}
}

// TestSyncSkipsWhatItCannotCheckOut: a dirty worktree is not switched, and a
// stack whose only branch is checked out in another worktree is left to be
// synced from there.
func TestSyncSkipsWhatItCannotCheckOut(t *testing.T) {
	deps, sf, _, repo, dir := submitFixture(t) // on b
	gittest.Run(t, dir, "branch", "c", "main")
	gittest.WriteFile(t, dir, "a.txt", "dirty") // uncommitted change in the main checkout
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "w1", wt)
	wts, _ := deps.Git.Worktrees(context.Background(), repo)
	wtPath := wts[1].Path

	graph := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "c"}}, Worktree: repo.TopLevel},  // needs a checkout; tree is dirty
		{Trunk: "main", Branches: []stack.Branch{{Name: "w1"}}, Worktree: repo.TopLevel}, // home here, but w1 is checked out in wt
	})
	deps.Meta = memMeta{graph}
	sf.graph = graph
	res, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.syncs) != 1 || len(res.Stacks) != 1 || res.Stacks[0].Branch != "b" {
		t.Errorf("only the checked out stack should sync: %+v %+v", sf.syncs, res.Stacks)
	}
	joined := strings.Join(res.Notices, "\n")
	if !strings.Contains(joined, "stack c is not checked out") || !strings.Contains(joined, "uncommitted changes") {
		t.Errorf("dirty worktree notice missing: %v", res.Notices)
	}
	if !strings.Contains(joined, "stack w1: every branch is checked out in another worktree") {
		t.Errorf("elsewhere notice missing: %v", res.Notices)
	}
	_ = wtPath
}

// TestSyncOnTrunkSyncsEveryStack: from trunk nothing is "current", so each
// stack is checked out in turn and trunk restored.
func TestSyncOnTrunkSyncsEveryStack(t *testing.T) {
	deps, sf, _, repo, dir := submitFixture(t)
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Run(t, dir, "branch", "c", "main")
	graph := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "c"}}, Worktree: repo.TopLevel},
	})
	deps.Meta = memMeta{graph}
	sf.graph = graph
	res, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.syncs) != 2 || len(res.Stacks) != 2 || !res.Stacks[0].CheckedOut || !res.Stacks[1].CheckedOut {
		t.Errorf("both stacks should be synced via checkouts: %+v", res.Stacks)
	}
	if got := gittest.Run(t, dir, "branch", "--show-current"); got != "main" {
		t.Errorf("should be back on main, on %s", got)
	}
}

// TestSyncWithNoStacks errors helpfully.
func TestSyncWithNoStacks(t *testing.T) {
	deps, _, _, repo, _ := submitFixture(t)
	deps.Meta = memMeta{stack.NewGraph(nil)}
	_, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Fatalf("expected not_in_stack, got %v", err)
	}
}

// TestSyncSkipsBranchesGoneLocally: gh stack keeps a stack's metadata after
// --prune deletes its merged branches, so a stack whose branches no longer
// exist locally is normal. One with nothing left is finished and skipped;
// one with some branches left is synced from a branch that still exists.
func TestSyncSkipsBranchesGoneLocally(t *testing.T) {
	deps, sf, _, repo, dir := submitFixture(t) // on b; stack a,b
	cur := func(d string) string { return gittest.Run(t, d, "branch", "--show-current") }
	gittest.Run(t, dir, "branch", "c", "main")
	merged := &stack.PRRef{Number: 1, Merged: true}
	graph := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "gone", PR: merged}}, Worktree: repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "gone2", PR: merged}, {Name: "c"}, {Name: "gone3", PR: merged}}, Worktree: repo.TopLevel},
	})
	deps.Meta = memMeta{graph}
	sf.graph = graph
	var ranFrom []string
	sf.onSync = func(stack.SyncOptions) { ranFrom = append(ranFrom, cur(dir)) }

	res, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ranFrom, ","); got != "b,c" {
		t.Errorf("synced from %v, want b,c", ranFrom)
	}
	if len(res.Stacks) != 2 || res.Stacks[1].Branch != "c" || !res.Stacks[1].CheckedOut {
		t.Errorf("stacks = %+v", res.Stacks)
	}
	if len(res.Notices) != 0 {
		t.Errorf("a finished stack is not news: %v", res.Notices)
	}
	if cur(dir) != "b" {
		t.Errorf("should be back on b, on %s", cur(dir))
	}
}

// TestSyncNoticesVanishedBranches: a stack whose branches are gone without
// their PRs having merged was broken by hand; say so rather than fail.
func TestSyncNoticesVanishedBranches(t *testing.T) {
	deps, sf, _, repo, _ := submitFixture(t) // on b
	graph := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "vanished"}}, Worktree: repo.TopLevel},
	})
	deps.Meta = memMeta{graph}
	sf.graph = graph
	res, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stacks) != 1 || res.Stacks[0].Branch != "b" {
		t.Errorf("stacks = %+v", res.Stacks)
	}
	joined := strings.Join(res.Notices, "\n")
	if !strings.Contains(joined, "stack vanished") || !strings.Contains(joined, "no longer exist") {
		t.Errorf("vanished notice missing: %v", res.Notices)
	}
}

// TestSyncPrefersUnmergedBranch: when a stack needs a checkout, a branch
// whose PR is still open is chosen over a merged one (which prune would
// delete under us), whatever their order.
func TestSyncPrefersUnmergedBranch(t *testing.T) {
	deps, sf, _, repo, dir := submitFixture(t)
	gittest.Run(t, dir, "switch", "-q", "main")
	graph := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b", PR: &stack.PRRef{Number: 1, Merged: true}}}, Worktree: repo.TopLevel},
	})
	deps.Meta = memMeta{graph}
	sf.graph = graph
	var ranFrom []string
	sf.onSync = func(stack.SyncOptions) { ranFrom = append(ranFrom, gittest.Run(t, dir, "branch", "--show-current")) }
	if _, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ranFrom, ",") != "a" {
		t.Errorf("synced from %v, want a", ranFrom)
	}
}

// TestSyncContinuesPastFailure: one stack failing must not stop the rest
// (gt syncs what it can and lists what it couldn't). The failure is
// reported per stack and as the returned error, and the worktree is put
// back where it was.
func TestSyncContinuesPastFailure(t *testing.T) {
	deps, sf, _, repo, dir := submitFixture(t)
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Run(t, dir, "branch", "c", "main")
	graph := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "c"}}, Worktree: repo.TopLevel},
	})
	deps.Meta = memMeta{graph}
	sf.graph = graph
	cur := func() string { return gittest.Run(t, dir, "branch", "--show-current") }
	sf.syncErr = func(stack.SyncOptions) error {
		if cur() == "b" {
			return errors.New("boom")
		}
		return nil
	}
	res, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "b") {
		t.Fatalf("expected an error naming the failed stack, got %v", err)
	}
	if len(sf.syncs) != 2 || len(res.Stacks) != 2 {
		t.Fatalf("both stacks should have been attempted: %+v %+v", sf.syncs, res.Stacks)
	}
	if res.Stacks[0].Error == "" || res.Stacks[1].Error != "" {
		t.Errorf("stacks = %+v", res.Stacks)
	}
	if cur() != "main" {
		t.Errorf("should be back on main, on %s", cur())
	}
}
