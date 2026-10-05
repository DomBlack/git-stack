package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func restacked(res app.SyncResult, name string) (app.BranchMove, bool) {
	i := slices.IndexFunc(res.Restacked, func(m app.BranchMove) bool { return m.Name == name })
	if i < 0 {
		return app.BranchMove{}, false
	}
	return res.Restacked[i], true
}

func TestSyncRestacksOntoMovedTrunk(t *testing.T) {
	f := newSyncFixture(t) // on b
	sha := f.advanceRemote(t, "main", "r.txt")
	res, err := f.sync(t, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restacked(res, "a"); !ok {
		t.Errorf("a not restacked: %+v", res.Restacked)
	}
	if _, ok := restacked(res, "b"); !ok {
		t.Errorf("b not restacked: %+v", res.Restacked)
	}
	if !strings.Contains(gittest.Run(t, f.dir, "ls-tree", "--name-only", "b"), "r.txt") {
		t.Error("b should contain trunk's new file")
	}
	if gittest.Run(t, f.dir, "log", "--format=%s", "main..b") != "feat: b\nfeat: a" {
		t.Errorf("history = %q", gittest.Run(t, f.dir, "log", "--format=%s", "main..b"))
	}
	if gittest.Run(t, f.dir, "merge-base", "main", "a") != sha {
		t.Error("a should sit on the new trunk")
	}
	// b is checked out: the working tree follows.
	if _, err := os.Stat(filepath.Join(f.dir, "r.txt")); err != nil {
		t.Error("working tree not updated")
	}
	// Metadata heads and bases match the refs.
	g, _ := f.deps.Meta.Load(context.Background(), f.repo)
	a, b := g.Stacks[0].Branches[0], g.Stacks[0].Branches[1]
	if a.Head != f.rev(t, "a") || a.Base != sha || b.Head != f.rev(t, "b") || b.Base != a.Head {
		t.Errorf("metadata a=%+v b=%+v", a, b)
	}
}

func TestSyncRestackAfterSquashMergedParent(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	// Record a's tip as b's base, as a previous restack would have.
	f.meta.g.Stacks[0].Branches[1].Base = f.rev(t, "a")
	f.mergeOnRemote(t, "a", 0)
	res, err := f.sync(t, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deleted(res, "a"); !ok {
		t.Fatalf("a should be deleted: %+v", res.Deleted)
	}
	if got := gittest.Run(t, f.dir, "log", "--format=%s", "main..b"); got != "feat: b" {
		t.Errorf("b should carry only its own commit on top of main, got %q", got)
	}
	if !strings.Contains(gittest.Run(t, f.dir, "ls-tree", "--name-only", "b"), "a.txt") {
		t.Error("b should still see a's file via the squash merge")
	}
}

func TestSyncRestackFallsBackToMergeBase(t *testing.T) {
	f := newSyncFixture(t)
	f.meta.g.Stacks[0].Branches[1].Base = "0000000000000000000000000000000000000000" // nonsense base
	f.advanceRemote(t, "main", "r.txt")
	if _, err := f.sync(t, app.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, f.dir, "log", "--format=%s", "main..b"); got != "feat: b\nfeat: a" {
		t.Errorf("history = %q", got)
	}
}

func TestSyncConflictStopsOneStackOnly(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Run(t, f.dir, "switch", "-q", "-c", "c", "main")
	gittest.Commit(t, f.dir, "README.md", "c version", "c clashes")
	gittest.Run(t, f.dir, "switch", "-q", "main")
	f.deps.Meta = memMeta{stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: f.repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "c"}}, Worktree: f.repo.TopLevel},
	})}
	cBefore := f.rev(t, "c")
	f.advanceRemote(t, "main", "README.md") // conflicts with c
	res, err := f.sync(t, app.SyncOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
		t.Fatalf("want a conflict error, got %v", err)
	}
	var se *stack.Error
	if !errors.As(err, &se) || !strings.Contains(se.Msg, "1 conflict (c)") || !strings.Contains(strings.Join(se.NextSteps, "\n"), "git stack restack") {
		t.Errorf("error = %+v", err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].Branch != "c" || res.Conflicts[0].Stack != "c" || res.Conflicts[0].Onto != "main" || !slices.Contains(res.Conflicts[0].Files, "README.md") {
		t.Errorf("conflicts = %+v", res.Conflicts)
	}
	if f.rev(t, "c") != cBefore {
		t.Error("a conflicted branch must not move")
	}
	if _, ok := restacked(res, "b"); !ok {
		t.Errorf("the other stack must still restack: %+v", res.Restacked)
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "git stack restack") {
		t.Errorf("notice should point at restack: %v", res.Notices)
	}
}

func TestSyncDirtyCheckedOutBranchIsSkipped(t *testing.T) {
	f := newSyncFixture(t) // on b
	bBefore := f.rev(t, "b")
	f.advanceRemote(t, "main", "r.txt")
	gittest.WriteFile(t, f.dir, "b.txt", "edited")
	res, err := f.sync(t, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restacked(res, "a"); !ok {
		t.Error("a should restack")
	}
	if _, ok := restacked(res, "b"); ok || f.rev(t, "b") != bBefore {
		t.Error("dirty b must stay put")
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "b.txt")); string(b) != "edited" {
		t.Error("the edit must survive")
	}
}

func TestSyncNoRestack(t *testing.T) {
	f := newSyncFixture(t)
	f.advanceRemote(t, "main", "r.txt")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil || len(res.Restacked) != 0 {
		t.Errorf("no restack: %+v %v", res.Restacked, err)
	}
}

func TestSyncNothingToRestack(t *testing.T) {
	f := newSyncFixture(t)
	res, err := f.sync(t, app.SyncOptions{})
	if err != nil || len(res.Restacked) != 0 {
		t.Errorf("already on trunk: %+v %v", res.Restacked, err)
	}
	_ = forge.StateOpen
}

func TestSyncPlanningErrorFailsOneStack(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Run(t, f.dir, "switch", "-q", "-c", "c", "main")
	gittest.Commit(t, f.dir, "c.txt", "c", "feat: c")
	gittest.Run(t, f.dir, "switch", "-q", "main")
	f.deps.Meta = memMeta{stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: f.repo.TopLevel},
		{Trunk: "nope", Branches: []stack.Branch{{Name: "c"}}, Worktree: f.repo.TopLevel},
	})}
	f.advanceRemote(t, "main", "r.txt")
	res, err := f.sync(t, app.SyncOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || !strings.Contains(se.Msg, "1 stack not restacked (c)") {
		t.Fatalf("error = %v", err)
	}
	if _, ok := restacked(res, "b"); !ok {
		t.Errorf("the other stack must still restack: %+v", res.Restacked)
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "was not restacked") {
		t.Errorf("notices = %v", res.Notices)
	}
}
