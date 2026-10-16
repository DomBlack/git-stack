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

func tipsOf(t *testing.T, f *restackFixture, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, n := range names {
		out[n] = f.rev(t, n)
	}
	return out
}

func assertTips(t *testing.T, f *restackFixture, tips map[string]string) {
	t.Helper()
	for n, tip := range tips {
		if f.rev(t, n) != tip {
			t.Errorf("%s moved: %s", n, f.rev(t, n))
		}
	}
}

func pendingRemote(t *testing.T, f *restackFixture) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.repo.CommonDir, "git-stack", "remote-stacks.json"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUntrackTakesTheBranchesAboveWithIt(t *testing.T) {
	f := newRestackFixture(t) // main -> a -> b -> c, HEAD on c
	f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[0].Number = "S1", 1
	tips := tipsOf(t, f, "a", "b", "c")
	res, err := f.app.Untrack(context.Background(), f.repo, app.UntrackOptions{Branch: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "b" || !slices.Equal(res.Untracked, []string{"b", "c"}) || len(res.Restacked) != 0 || res.Parent != "" {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(res.NextSteps, []string{"git stack init b c"}) {
		t.Errorf("next steps = %q", res.NextSteps)
	}
	want := [][]string{{"a"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	assertTips(t, f, tips)
	if f.fb.graph.Stacks[0].ID != "" {
		t.Errorf("id = %q, want it cleared", f.fb.graph.Stacks[0].ID)
	}
	if got := pendingRemote(t, f); !strings.Contains(got, `"id":"S1"`) {
		t.Errorf("the stack should be queued for dissolving: %q", got)
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("current = %s", cur)
	}
}

func TestUntrackDefaultsToTheCurrentBranch(t *testing.T) {
	f := newRestackFixture(t)
	res, err := f.app.Untrack(context.Background(), f.repo, app.UntrackOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Untracked, []string{"c"}) {
		t.Errorf("result = %+v", res)
	}
	want := [][]string{{"a", "b"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if got := pendingRemote(t, f); got != "" {
		t.Errorf("a stack GitHub doesn't know needs no dissolving: %q", got)
	}
}

func TestUntrackTheBottomEmptiesTheStack(t *testing.T) {
	f := newRestackFixture(t)
	addStack(t, f, false)
	f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[0].Number = "S1", 1
	res, err := f.app.Untrack(context.Background(), f.repo, app.UntrackOptions{Branch: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Untracked, []string{"a", "b", "c"}) {
		t.Errorf("result = %+v", res)
	}
	want := [][]string{{"x"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if f.fb.graph.Stacks[0].ID != "S2" {
		t.Errorf("the other stack's id = %q", f.fb.graph.Stacks[0].ID)
	}
	if got := pendingRemote(t, f); !strings.Contains(got, `"id":"S1"`) || strings.Contains(got, "S2") {
		t.Errorf("pending = %q", got)
	}
}

func TestUntrackOnlyRestacksTheBranchesAboveOntoItsParent(t *testing.T) {
	f := newRestackFixture(t)
	f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[0].Number = "S1", 1
	b := f.rev(t, "b")
	res, err := f.app.Untrack(context.Background(), f.repo, app.UntrackOptions{Branch: "b", Only: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Untracked, []string{"b"}) || res.Parent != "a" || !slices.Equal(res.Restacked, []string{"c"}) {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(res.NextSteps, []string{"git stack init b"}) {
		t.Errorf("next steps = %q", res.NextSteps)
	}
	want := [][]string{{"a", "c"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if f.rev(t, "b") != b {
		t.Error("b itself should not move")
	}
	if f.has(t, "c", "b.txt") || !f.has(t, "c", "a.txt") || !f.has(t, "c", "c.txt") || f.base("c") != f.rev(t, "a") {
		t.Error("c should sit on a without b's file")
	}
	if f.fb.graph.Stacks[0].ID != "" || !strings.Contains(pendingRemote(t, f), `"id":"S1"`) {
		t.Error("the stack should be queued for dissolving")
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("current = %s", cur)
	}
}

// --only on the top branch has nothing above it to restack.
func TestUntrackOnlyTheTop(t *testing.T) {
	f := newRestackFixture(t)
	res, err := f.app.Untrack(context.Background(), f.repo, app.UntrackOptions{Branch: "c", Only: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Untracked, []string{"c"}) || len(res.Restacked) != 0 {
		t.Errorf("result = %+v", res)
	}
	want := [][]string{{"a", "b"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
}

func TestUntrackOnlyConflictAbortPutsTheBranchBack(t *testing.T) {
	// c changed shared.txt from b's version, so it conflicts on a.
	f := newLinearFixture(t, []fileCommit{{"a", "shared.txt", "a"}, {"b", "shared.txt", "b"}, {"c", "shared.txt", "c"}})
	f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[0].Number = "S1", 1
	ctx := context.Background()
	tips := tipsOf(t, f, "a", "b", "c")
	bases := map[string]string{"a": f.base("a"), "b": f.base("b"), "c": f.base("c")}
	_, err := f.app.Untrack(ctx, f.repo, app.UntrackOptions{Branch: "b", Only: true})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "c" {
		t.Fatalf("want a conflict on c, got %v", err)
	}
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true})
	if err != nil || !res.OrderRestored {
		t.Fatalf("abort: %+v %v", res, err)
	}
	want := [][]string{{"a", "b", "c"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	assertTips(t, f, tips)
	for n, b := range bases {
		if f.base(n) != b {
			t.Errorf("%s base = %s, want %s", n, f.base(n), b)
		}
	}
	if s, i, ok := f.fb.graph.StackOf("b"); !ok || s.Branches[i].Head != tips["b"] {
		t.Errorf("b's entry should be back with its head")
	}
	if f.fb.graph.Stacks[0].ID != "S1" || strings.Contains(pendingRemote(t, f), "S1") {
		t.Errorf("the remote id should be back and the dissolve forgotten: %q %q", f.fb.graph.Stacks[0].ID, pendingRemote(t, f))
	}
}

func TestUntrackRefusals(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "-c", "loose", "main")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	ctx := context.Background()
	for _, tc := range []struct {
		o    app.UntrackOptions
		kind stack.Kind
		want string
	}{
		{app.UntrackOptions{Branch: "main"}, stack.KindInvalidArgs, "trunk"},
		{app.UntrackOptions{Branch: "loose"}, stack.KindNotInStack, "loose is not in a stack"},
		{app.UntrackOptions{Branch: "nope"}, stack.KindInvalidArgs, "no branch nope"},
	} {
		_, err := f.app.Untrack(ctx, f.repo, tc.o)
		if !errors.Is(err, &stack.Error{Kind: tc.kind}) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc.o, err)
		}
	}
	f.fb.graph.Stacks[0].Branches[0].PR = &stack.PRRef{Number: 1, Merged: true}
	_, err := f.app.Untrack(ctx, f.repo, app.UntrackOptions{Branch: "b"})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "sync") {
		t.Errorf("merged: %v", err)
	}
	if got := stackNames(f); len(got) != 1 || len(got[0]) != 3 {
		t.Errorf("a refusal changed the stacks: %v", got)
	}
}

// --only on the bottom branch leaves the rest of the stack on trunk.
func TestUntrackOnlyTheBottom(t *testing.T) {
	f := newRestackFixture(t)
	res, err := f.app.Untrack(context.Background(), f.repo, app.UntrackOptions{Branch: "a", Only: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Parent != "main" || !slices.Equal(res.Restacked, []string{"b", "c"}) {
		t.Errorf("result = %+v", res)
	}
	want := [][]string{{"b", "c"}}
	if got := stackNames(f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stacks = %v", got)
	}
	if f.has(t, "b", "a.txt") || f.has(t, "c", "a.txt") || f.base("b") != f.rev(t, "main") {
		t.Error("b and c should sit on main without a's file")
	}
}
