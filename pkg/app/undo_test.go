package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// tips reads the named branches' tips.
func tips(t *testing.T, dir string, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, n := range names {
		out[n] = gittest.Run(t, dir, "rev-parse", n)
	}
	return out
}

func wantTips(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	for n, sha := range want {
		if got := gittest.Run(t, dir, "rev-parse", n); got != sha {
			t.Errorf("%s = %s, want %s", n, got, sha)
		}
	}
}

func current(t *testing.T, dir string) string {
	t.Helper()
	return gittest.Run(t, dir, "branch", "--show-current")
}

func status(t *testing.T, dir string) string {
	t.Helper()
	return gittest.Run(t, dir, "status", "--short", "--untracked-files=no")
}

func undoKind(err error) stack.Kind {
	if se, ok := errors.AsType[*stack.Error](err); ok {
		return se.Kind
	}
	return stack.KindUnknown
}

func TestUndoCreate(t *testing.T) {
	a, fb, repo, dir, _ := mutFixture(t)
	ctx := context.Background()
	gittest.WriteFile(t, dir, "b.txt", "b")
	gittest.Run(t, dir, "add", "b.txt")
	before := tips(t, dir, "main", "a")
	created, err := a.Create(ctx, repo, app.CreateOptions{Message: []string{"Add B"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Undo(ctx, repo, app.UndoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Undone == nil || res.Undone.Command != "git stack create" || !res.Staged {
		t.Errorf("result = %+v", res)
	}
	if !slices.Contains(res.Undone.Restores, created.Branch+" deleted") {
		t.Errorf("restores = %q", res.Undone.Restores)
	}
	if current(t, dir) != "a" || status(t, dir) != "A  b.txt" {
		t.Errorf("on %s with %q", current(t, dir), status(t, dir))
	}
	wantTips(t, dir, before)
	if branchExists(t, dir, created.Branch) {
		t.Error("the created branch should be gone")
	}
	if len(fb.graph.Stacks) != 1 || !slices.Equal(fb.graph.Stacks[0].Names(), []string{"a"}) {
		t.Errorf("stacks = %+v", fb.graph.Stacks)
	}
	if _, err := a.Undo(ctx, repo, app.UndoOptions{}); undoKind(err) != stack.KindInvalidArgs {
		t.Errorf("a second undo = %v", err)
	}
}

func TestUndoModify(t *testing.T) {
	a, _, repo, dir, _ := mutFixture(t)
	ctx := context.Background()
	before := tips(t, dir, "a")
	gittest.WriteFile(t, dir, "a.txt", "a2")
	gittest.Run(t, dir, "add", "a.txt")
	if _, err := a.Modify(ctx, repo, app.ModifyOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := a.Undo(ctx, repo, app.UndoOptions{})
	if err != nil || !res.Staged {
		t.Fatalf("undo = %+v %v", res, err)
	}
	wantTips(t, dir, before)
	if status(t, dir) != "M  a.txt" {
		t.Errorf("status = %q", status(t, dir))
	}
}

func TestUndoAbsorb(t *testing.T) {
	f := newAbsorbFixture(t)
	ctx := context.Background()
	before := tips(t, f.dir, "a", "b", "c")
	f.stageLine(t, 5, "FIVE")
	if _, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	if current(t, f.dir) != "b" || status(t, f.dir) != "M  f.txt" {
		t.Errorf("on %s with %q", current(t, f.dir), status(t, f.dir))
	}
}

func TestUndoRestack(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	advanceMain(t, f.dir)
	before := tips(t, f.dir, "main", "a", "b", "c")
	bases := []string{f.base("a"), f.base("b"), f.base("c")}
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := f.app.Undo(ctx, f.repo, app.UndoOptions{})
	if err != nil || res.Staged {
		t.Fatalf("undo = %+v %v", res, err)
	}
	wantTips(t, f.dir, before)
	if got := []string{f.base("a"), f.base("b"), f.base("c")}; !slices.Equal(got, bases) {
		t.Errorf("bases = %v, want %v", got, bases)
	}
	if current(t, f.dir) != "c" || status(t, f.dir) != "" {
		t.Errorf("on %s with %q", current(t, f.dir), status(t, f.dir))
	}
}

func TestUndoReorder(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	before := tips(t, f.dir, "a", "b", "c")
	if _, err := f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: []string{"b", "a", "c"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) || f.base("b") != before["a"] {
		t.Errorf("stack = %v, base of b %s", f.names(), f.base("b"))
	}
}

func TestUndoMove(t *testing.T) {
	f := newRestackFixture(t)
	addStack(t, f, false)
	ctx := context.Background()
	before := tips(t, f.dir, "a", "b", "c", "x")
	if _, err := f.app.Move(ctx, f.repo, app.MoveOptions{Source: "b", Onto: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	want := [][]string{{"a", "b", "c"}, {"x"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
}

func TestUndoInit(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	gittest.Run(t, f.dir, "switch", "-q", "-c", "p", "main")
	gittest.Commit(t, f.dir, "p.txt", "p", "p")
	if _, err := f.app.Init(ctx, f.repo, app.InitOptions{Branches: []string{"p"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.fb.graph.Tracked("p") || !branchExists(t, f.dir, "p") {
		t.Error("p should exist but not be tracked")
	}
}

func TestUndoSyncBringsBackADeletedBranch(t *testing.T) {
	f := newSyncFixture(t) // on b
	ctx := context.Background()
	f.mergeOnRemote(t, "a", 0)
	before := tips(t, f.dir, "main", "a", "b")
	if _, err := f.sync(t, app.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if branchExists(t, f.dir, "a") {
		t.Fatal("sync should have deleted a")
	}
	res, err := app.New(f.deps).Undo(ctx, f.repo, app.UndoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Undone.Restores, "a recreated") {
		t.Errorf("restores = %q", res.Undone.Restores)
	}
	wantTips(t, f.dir, before)
	g, _ := f.deps.Meta.Load(ctx, f.repo)
	if len(g.Stacks) != 1 || !slices.Equal(g.Stacks[0].Names(), []string{"a", "b"}) {
		t.Errorf("stacks = %+v", g.Stacks)
	}
	if current(t, f.dir) != "b" || status(t, f.dir) != "" {
		t.Errorf("on %s with %q", current(t, f.dir), status(t, f.dir))
	}
}

func TestUndoStepsBack(t *testing.T) {
	a, _, repo, dir, _ := mutFixture(t)
	ctx := context.Background()
	for _, n := range []string{"x", "y"} {
		gittest.WriteFile(t, dir, n+".txt", n)
		gittest.Run(t, dir, "add", n+".txt")
		if _, err := a.Create(ctx, repo, app.CreateOptions{Message: []string{"Add " + n}}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := a.Undo(ctx, repo, app.UndoOptions{List: true})
	if err != nil || len(list.Entries) != 2 || list.Entries[0].Command != "git stack create" || list.Undone != nil {
		t.Fatalf("list = %+v %v", list, err)
	}
	if !list.Entries[0].At.After(list.Entries[1].At) {
		t.Error("the list should be newest first")
	}
	if len(app.UndoHistory(repo)) != 2 {
		t.Error("listing must not change anything")
	}
	for range 2 {
		if _, err := a.Undo(ctx, repo, app.UndoOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if current(t, dir) != "a" || status(t, dir) != "A  x.txt\nA  y.txt" {
		t.Errorf("on %s with %q", current(t, dir), status(t, dir))
	}
	_, err = a.Undo(ctx, repo, app.UndoOptions{})
	if undoKind(err) != stack.KindInvalidArgs || !strings.Contains(err.Error(), "nothing to undo") {
		t.Errorf("third undo = %v", err)
	}
}

func TestUndoIsPerWorktree(t *testing.T) {
	f := newRestackFixture(t) // on c
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, f.dir, "worktree", "add", "-q", "-b", "p", wt, "main")
	gittest.Commit(t, wt, "p.txt", "p", "p")
	g := git.New(exec.New())
	other, err := g.Discover(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	b := app.New(app.Deps{Git: g, Meta: f.fb, Tracker: f.fb})
	if _, err := b.Init(ctx, other, app.InitOptions{Branches: []string{"p"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); undoKind(err) != stack.KindInvalidArgs {
		t.Fatalf("undo here = %v", err)
	}
	if list, err := f.app.Undo(ctx, f.repo, app.UndoOptions{List: true}); err != nil || len(list.Entries) != 0 {
		t.Fatalf("list here = %+v %v", list, err)
	}
	advanceMain(t, f.dir)
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := f.app.Undo(ctx, f.repo, app.UndoOptions{})
	if err != nil || res.Undone.Command != "git stack restack" {
		t.Fatalf("undo = %+v %v", res, err)
	}
	if !f.fb.graph.Tracked("p") {
		t.Error("the other worktree's init should still be there")
	}
	// main moved on by hand since the init, so it takes --force.
	if res, err := b.Undo(ctx, other, app.UndoOptions{Force: true}); err != nil || res.Undone.Command != "git stack init" {
		t.Errorf("undo there = %+v %v", res, err)
	}
	if f.fb.graph.Tracked("p") {
		t.Error("undoing the init should stop tracking p")
	}
}

func TestUndoRefusesDriftUnlessForced(t *testing.T) {
	f := newRestackFixture(t) // on c
	ctx := context.Background()
	advanceMain(t, f.dir)
	before := tips(t, f.dir, "a", "b", "c")
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); err != nil {
		t.Fatal(err)
	}
	gittest.Commit(t, f.dir, "later.txt", "later", "later")
	_, err := f.app.Undo(ctx, f.repo, app.UndoOptions{})
	if undoKind(err) != stack.KindInvalidArgs || !strings.Contains(err.Error(), "c") {
		t.Fatalf("undo = %v", err)
	}
	if se, _ := errors.AsType[*stack.Error](err); !strings.Contains(strings.Join(se.NextSteps, "\n"), "git stack undo --force") {
		t.Errorf("steps = %v", se.NextSteps)
	}
	if len(app.UndoHistory(f.repo)) != 1 {
		t.Error("a refused undo must keep the entry")
	}
	gittest.WriteFile(t, f.dir, "a.txt", "mine")
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	if status(t, f.dir) != "M a.txt" {
		t.Errorf("the working tree change should survive: %q", status(t, f.dir))
	}
}

func TestUndoRefusesWhenChangesAreInTheWay(t *testing.T) {
	f := newRestackFixture(t) // on c
	ctx := context.Background()
	advanceMain(t, f.dir) // adds m.txt
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); err != nil {
		t.Fatal(err)
	}
	after := tips(t, f.dir, "a", "b", "c")
	gittest.WriteFile(t, f.dir, "m.txt", "mine")
	_, err := f.app.Undo(ctx, f.repo, app.UndoOptions{})
	if undoKind(err) != stack.KindInvalidArgs || !strings.Contains(err.Error(), "m.txt") {
		t.Fatalf("undo = %v", err)
	}
	wantTips(t, f.dir, after)
	if len(app.UndoHistory(f.repo)) != 1 {
		t.Error("a refused undo must keep the entry")
	}
}

func TestUndoKeepsUnrelatedChanges(t *testing.T) {
	f := newRestackFixture(t) // on c
	ctx := context.Background()
	advanceMain(t, f.dir)
	before := tips(t, f.dir, "a", "b", "c")
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); err != nil {
		t.Fatal(err)
	}
	gittest.WriteFile(t, f.dir, "a.txt", "mine")
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	if status(t, f.dir) != "M a.txt" {
		t.Errorf("status = %q", status(t, f.dir))
	}
}

func TestUndoMovesOtherWorktrees(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "dirty"}[dirty], func(t *testing.T) {
			f := newRestackFixture(t) // on c
			ctx := context.Background()
			wt := filepath.Join(t.TempDir(), "wt")
			gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "b")
			advanceMain(t, f.dir) // adds m.txt
			before := tips(t, f.dir, "a", "b", "c")
			if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); err != nil {
				t.Fatal(err)
			}
			after := tips(t, f.dir, "a", "b", "c")
			if after["b"] == before["b"] {
				t.Fatal("restack should have moved b")
			}
			if dirty {
				gittest.WriteFile(t, wt, "m.txt", "mine")
			}
			_, err := f.app.Undo(ctx, f.repo, app.UndoOptions{})
			if dirty {
				if undoKind(err) != stack.KindInvalidArgs || !strings.Contains(err.Error(), "m.txt") {
					t.Fatalf("undo = %v", err)
				}
				wantTips(t, f.dir, after)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantTips(t, f.dir, before)
			if gittest.Run(t, wt, "rev-parse", "HEAD") != before["b"] || status(t, wt) != "" {
				t.Errorf("the other worktree should follow b back: %q", status(t, wt))
			}
		})
	}
}

func TestUndoRefusesDuringARestack(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Commit(t, f.dir, "a.txt", "main's a", "main: a")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); undoKind(err) != stack.KindConflict {
		t.Fatalf("restack = %v", err)
	}
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); undoKind(err) != stack.KindRebaseActive {
		t.Errorf("undo = %v", err)
	}
}
