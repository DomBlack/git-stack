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

// TestSyncCoversEveryWorktree: a stack checked out in a linked worktree is
// synced there (gh stack syncs what is checked out where it runs), the
// current worktree goes first, and a stack nobody has checked out is
// reported rather than silently skipped.
func TestSyncCoversEveryWorktree(t *testing.T) {
	deps, sf, _, repo, dir := submitFixture(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "w1", wt)
	wts, err := deps.Git.Worktrees(context.Background(), repo)
	if err != nil || len(wts) != 2 {
		t.Fatalf("worktrees = %+v %v", wts, err)
	}
	wtPath := wts[1].Path

	graph := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "w1"}}, Worktree: wtPath},
		{Trunk: "main", Branches: []stack.Branch{{Name: "zz"}}, Worktree: wtPath},
	})
	deps.Meta = memMeta{graph}
	sf.graph = graph

	res, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.syncs) != 2 || sf.syncs[0].Dir != "" || sf.syncs[1].Dir != wtPath {
		t.Errorf("sync calls = %+v (want current worktree first, then %s)", sf.syncs, wtPath)
	}
	if len(res.Worktrees) != 2 || res.Worktrees[0].Branch != "b" || res.Worktrees[1].Branch != "w1" || res.Worktrees[1].Path != wtPath {
		t.Errorf("worktrees = %+v", res.Worktrees)
	}
	found := false
	for _, n := range res.Notices {
		if strings.Contains(n, "stack zz is not checked out") {
			found = true
		}
	}
	if !found {
		t.Errorf("unsynced stack should be reported: %v", res.Notices)
	}
}

// TestSyncNothingCheckedOut: on trunk with several stacks, nothing can be
// synced and the error says what to do.
func TestSyncNothingCheckedOut(t *testing.T) {
	deps, _, _, repo, dir := submitFixture(t)
	gittest.Run(t, dir, "switch", "-q", "main")
	graph := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}},
		{Trunk: "main", Branches: []stack.Branch{{Name: "c"}}},
	})
	deps.Meta = memMeta{graph}
	_, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Fatalf("expected not_in_stack, got %v", err)
	}
}
