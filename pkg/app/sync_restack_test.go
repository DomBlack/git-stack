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
	if n := gittest.Run(t, f.dir, "rev-list", "--count", "main..b"); n != "1" {
		t.Errorf("b should have one commit on main, got %s", n)
	}
	if !strings.Contains(gittest.Run(t, f.dir, "ls-tree", "--name-only", "b"), "a.txt") {
		t.Error("b should still see a's file via the squash merge")
	}
}

func TestSyncRestackAfterSquashMergedParentWithoutBase(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	f.mergeOnRemote(t, "a", 0)
	if _, err := f.sync(t, app.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	// With no base recorded a's commit is replayed too, and dropped because
	// the squash merge already holds its changes.
	if got := gittest.Run(t, f.dir, "log", "--format=%s", "main..b"); got != "feat: b" {
		t.Errorf("b should carry no empty commits, got %q", got)
	}
}

func TestSyncUntrackedFileBlocksRestackOfCheckedOutBranch(t *testing.T) {
	f := newSyncFixture(t) // on b
	bBefore := f.rev(t, "b")
	f.advanceRemote(t, "main", "r.txt")
	gittest.WriteFile(t, f.dir, "r.txt", "mine")
	res, err := f.sync(t, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restacked(res, "a"); !ok {
		t.Error("a is not checked out and should restack")
	}
	if _, ok := restacked(res, "b"); ok || f.rev(t, "b") != bBefore {
		t.Error("b must stay put rather than overwrite r.txt")
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "r.txt")); string(b) != "mine" {
		t.Error("the untracked file must survive")
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "would overwrite untracked r.txt") {
		t.Errorf("notices = %v", res.Notices)
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

func TestSyncRestacksBranchCheckedOutInLinkedWorktree(t *testing.T) {
	f := newSyncFixture(t) // on b
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "a")
	f.advanceRemote(t, "main", "r.txt")
	res, err := f.sync(t, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restacked(res, "a"); !ok {
		t.Fatalf("a not restacked: %+v %v", res.Restacked, res.Notices)
	}
	if got := gittest.Run(t, wt, "rev-parse", "HEAD"); got != f.rev(t, "a") {
		t.Errorf("linked worktree at %s, a at %s", got, f.rev(t, "a"))
	}
	if _, err := os.Stat(filepath.Join(wt, "r.txt")); err != nil {
		t.Error("the linked worktree's tree should follow a")
	}
	if st := gittest.Run(t, wt, "status", "--porcelain"); st != "" {
		t.Errorf("linked worktree should be clean: %q", st)
	}
}

func TestSyncRefusedRefTransactionFailsTheStack(t *testing.T) {
	f := newSyncFixture(t)
	aBefore, bBefore := f.rev(t, "a"), f.rev(t, "b")
	f.advanceRemote(t, "main", "r.txt")
	// Another git process holding a's ref lock makes the transaction fail.
	lock := filepath.Join(f.dir, ".git", "refs", "heads", "a.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })
	res, err := f.sync(t, app.SyncOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || !strings.Contains(se.Msg, "1 stack not restacked (a)") {
		t.Fatalf("error = %v", err)
	}
	if f.rev(t, "a") != aBefore || f.rev(t, "b") != bBefore || len(res.Restacked) != 0 {
		t.Errorf("a stack whose transaction failed must not move: %+v", res.Restacked)
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "moved while syncing") {
		t.Errorf("notices = %v", res.Notices)
	}
	if trunk(res, "main").Status != app.TrunkFastForwarded {
		t.Errorf("the trunk still moves: %+v", res.Trunks)
	}
}

func TestSyncUntrackedPathClashBlocksRestack(t *testing.T) {
	for _, tc := range []struct{ name, added, untracked string }{
		{"untracked file where a directory arrives", "docs/x.txt", "docs"},
		{"untracked directory where a file arrives", "notes", "notes/n.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSyncFixture(t) // on b
			bBefore := f.rev(t, "b")
			f.advanceRemote(t, "main", tc.added)
			gittest.WriteFile(t, f.dir, tc.untracked, "mine")
			res, err := f.sync(t, app.SyncOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := restacked(res, "b"); ok || f.rev(t, "b") != bBefore {
				t.Error("b must stay put rather than delete the untracked path")
			}
			if b, _ := os.ReadFile(filepath.Join(f.dir, tc.untracked)); string(b) != "mine" {
				t.Error("the untracked file must survive")
			}
			if !strings.Contains(strings.Join(res.Notices, "\n"), "would overwrite untracked "+tc.untracked) {
				t.Errorf("notices = %v", res.Notices)
			}
		})
	}
}
