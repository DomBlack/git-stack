package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// addStack adds a second stack main -> x (x.txt) [-> y (y.txt) when withY]
// to the fixture and goes back to the branch that was checked out.
func addStack(t *testing.T, f *restackFixture, withY bool) {
	t.Helper()
	cur := gittest.Run(t, f.dir, "branch", "--show-current")
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Run(t, f.dir, "switch", "-q", "-c", "x")
	gittest.Commit(t, f.dir, "x.txt", "x", "feat: x")
	branches := []stack.Branch{{Name: "x", Head: f.rev(t, "x"), Base: f.rev(t, "main")}}
	if withY {
		gittest.Run(t, f.dir, "switch", "-q", "-c", "y")
		gittest.Commit(t, f.dir, "y.txt", "y", "feat: y")
		branches = append(branches, stack.Branch{Name: "y", Head: f.rev(t, "y"), Base: f.rev(t, "x")})
	}
	gittest.Run(t, f.dir, "switch", "-q", cur)
	f.fb.graph = stack.NewGraph(append(f.fb.graph.Stacks, stack.Stack{Trunk: "main", ID: "S2", Number: 2, Branches: branches}))
}

func stackNames(f *restackFixture) [][]string {
	var out [][]string
	for _, s := range f.fb.graph.Stacks {
		out = append(out, s.Names())
	}
	return out
}

func TestMoveOntoAnotherStacksTop(t *testing.T) {
	f := newRestackFixture(t) // main -> a -> b -> c, HEAD on c
	addStack(t, f, false)
	f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[0].Number = "S1", 1
	ctx := context.Background()
	res, err := f.app.Move(ctx, f.repo, app.MoveOptions{Source: "b", Onto: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "b" || res.From != "a" || res.Onto != "x" || res.NewStack || !slices.Equal(res.Moved, []string{"b", "c"}) || len(res.LeftBehind) != 0 || !slices.Equal(res.Restacked, []string{"b", "c"}) {
		t.Errorf("result = %+v", res)
	}
	want := [][]string{{"a"}, {"x", "b", "c"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if f.has(t, "b", "a.txt") || !f.has(t, "b", "x.txt") || !f.has(t, "c", "b.txt") || !f.has(t, "c", "x.txt") {
		t.Error("b and c should sit on x without a's file")
	}
	if f.base("b") != f.rev(t, "x") || f.base("c") != f.rev(t, "b") {
		t.Errorf("bases b=%s c=%s", f.base("b"), f.base("c"))
	}
	// The source stack lost members, so its GitHub stack is queued for
	// dissolving and its id cleared; the destination only gained an append.
	if f.fb.graph.Stacks[0].ID != "" || f.fb.graph.Stacks[1].ID != "S2" {
		t.Errorf("ids = %q %q", f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[1].ID)
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("current = %s", cur)
	}
}

func TestMoveOntoTrunkStartsANewStack(t *testing.T) {
	f := newRestackFixture(t)
	res, err := f.app.Move(context.Background(), f.repo, app.MoveOptions{Onto: "main"}) // source defaults to c
	if err != nil {
		t.Fatal(err)
	}
	if !res.NewStack || res.Branch != "c" || res.From != "b" || !slices.Equal(res.Moved, []string{"c"}) {
		t.Errorf("result = %+v", res)
	}
	want := [][]string{{"a", "b"}, {"c"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if f.has(t, "c", "a.txt") || f.has(t, "c", "b.txt") || !f.has(t, "c", "c.txt") || f.base("c") != f.rev(t, "main") {
		t.Error("c should carry only its own commit on main")
	}
}

func TestMoveOnlyLeavesTheRestBehind(t *testing.T) {
	f := newRestackFixture(t)
	addStack(t, f, false)
	res, err := f.app.Move(context.Background(), f.repo, app.MoveOptions{Source: "b", Onto: "x", Only: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Moved, []string{"b"}) || !slices.Equal(res.LeftBehind, []string{"c"}) || !slices.Equal(res.Restacked, []string{"b", "c"}) {
		t.Errorf("result = %+v", res)
	}
	want := [][]string{{"a", "c"}, {"x", "b"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if f.has(t, "c", "b.txt") || !f.has(t, "c", "a.txt") || !f.has(t, "c", "c.txt") || f.base("c") != f.rev(t, "a") {
		t.Error("c should sit on a without b's file")
	}
}

func TestMoveRefusals(t *testing.T) {
	f := newRestackFixture(t)
	addStack(t, f, true) // main -> x -> y
	gittest.Run(t, f.dir, "switch", "-q", "-c", "loose", "main")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	ctx := context.Background()
	for _, tc := range []struct {
		o    app.MoveOptions
		kind stack.Kind
		want string
	}{
		{app.MoveOptions{Source: "c", Onto: "a"}, stack.KindInvalidArgs, "reorder"},
		{app.MoveOptions{Source: "b", Onto: "x"}, stack.KindInvalidArgs, "y is above x"},
		{app.MoveOptions{Source: "b", Onto: "loose"}, stack.KindNotInStack, "loose"},
		{app.MoveOptions{Source: "loose", Onto: "y"}, stack.KindNotInStack, "loose"},
		{app.MoveOptions{Source: "main", Onto: "y"}, stack.KindInvalidArgs, "trunk"},
		{app.MoveOptions{Source: "a", Onto: "main"}, stack.KindInvalidArgs, "already on main"},
		{app.MoveOptions{Source: "b", Onto: "nope"}, stack.KindInvalidArgs, "nope"},
		{app.MoveOptions{Source: "b"}, stack.KindInteractionRequired, "destination"},
	} {
		_, err := f.app.Move(ctx, f.repo, tc.o)
		if !errors.Is(err, &stack.Error{Kind: tc.kind}) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc.o, err)
		}
	}
	dests := f.app.MoveDestinations(f.fb.graph, "b")
	var names []string
	for _, d := range dests {
		names = append(names, d.Name)
	}
	if !slices.Equal(names, []string{"main", "y"}) || !dests[0].IsTrunk || dests[1].Stack != "x" {
		t.Errorf("destinations = %+v", dests)
	}
	picked := ""
	res, err := f.app.Move(ctx, f.repo, app.MoveOptions{Source: "b", Pick: func(d []app.MoveDestination) (string, error) { picked = d[0].Name; return "", nil }})
	if err != nil || !res.Cancelled || picked != "main" {
		t.Errorf("cancelled pick: %+v %v %q", res, err, picked)
	}
}

func TestMoveOnlyConflictAbortRestoresEverything(t *testing.T) {
	// main -> a (shared "a") -> b (shared "b") -> c (shared "c"), and x
	// with shared "a" too: b onto x goes through (same starting text), then
	// c onto a conflicts (c changed the line from b's version).
	f := newLinearFixture(t, []fileCommit{{"a", "shared.txt", "a"}, {"b", "shared.txt", "b"}, {"c", "shared.txt", "c"}})
	addStack(t, f, false)
	gittest.Run(t, f.dir, "switch", "-q", "x")
	gittest.Commit(t, f.dir, "shared.txt", "a", "x shared")
	f.fb.graph.Stacks[1].Branches[0].Head = f.rev(t, "x")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	ctx := context.Background()
	tips := map[string]string{"a": f.rev(t, "a"), "b": f.rev(t, "b"), "c": f.rev(t, "c"), "x": f.rev(t, "x")}
	_, err := f.app.Move(ctx, f.repo, app.MoveOptions{Source: "b", Onto: "x", Only: true})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "c" {
		t.Fatalf("want a conflict on c, got %v", err)
	}
	if f.rev(t, "b") == tips["b"] {
		t.Error("b should have moved onto x before c conflicted")
	}
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true})
	if err != nil || !res.OrderRestored || !slices.Contains(res.Restored, "b") {
		t.Fatalf("abort: %+v %v", res, err)
	}
	want := [][]string{{"a", "b", "c"}, {"x"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	for n, tip := range tips {
		if f.rev(t, n) != tip {
			t.Errorf("%s moved: %s", n, f.rev(t, n))
		}
	}
	if f.base("b") != f.rev(t, "a") || f.base("c") != f.rev(t, "b") {
		t.Errorf("bases b=%s c=%s", f.base("b"), f.base("c"))
	}
}

func TestMoveConflictInFirstSegmentThenContinueFinishesBoth(t *testing.T) {
	// b conflicts with x (both change shared.txt); c is left behind and
	// must still be restacked onto a once b is through.
	f := newLinearFixture(t, []fileCommit{{"a", "a.txt", "a"}, {"b", "shared.txt", "b"}, {"c", "c.txt", "c"}})
	addStack(t, f, false)
	gittest.Run(t, f.dir, "switch", "-q", "x")
	gittest.Commit(t, f.dir, "shared.txt", "x", "x shared")
	f.fb.graph.Stacks[1].Branches[0].Head = f.rev(t, "x")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	ctx := context.Background()
	_, err := f.app.Move(ctx, f.repo, app.MoveOptions{Source: "b", Onto: "x", Only: true})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "b" {
		t.Fatalf("want a conflict on b, got %v", err)
	}
	gittest.WriteFile(t, f.dir, "shared.txt", "bx")
	gittest.Run(t, f.dir, "add", "shared.txt")
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := moved(res); !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("moved %v", got)
	}
	want := [][]string{{"a", "c"}, {"x", "b"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if gittest.Run(t, f.dir, "show", "c:shared.txt") != "base" || !f.has(t, "c", "a.txt") || f.base("c") != f.rev(t, "a") {
		t.Error("c should be on a without b's change")
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("current = %s", cur)
	}
}
