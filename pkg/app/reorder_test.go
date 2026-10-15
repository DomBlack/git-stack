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

// fileCommit is one branch of a linear fixture: the branch is created on
// the previous one and gets one commit writing file.
type fileCommit struct{ branch, file, content string }

// newLinearFixture builds main (shared.txt "base") then one branch per
// entry, each with one commit, all tracked with heads and bases recorded,
// HEAD on the last branch.
func newLinearFixture(t *testing.T, commits []fileCommit) *restackFixture {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Commit(t, dir, "shared.txt", "base", "shared")
	for _, c := range commits {
		gittest.Run(t, dir, "switch", "-q", "-c", c.branch)
		gittest.Commit(t, dir, c.file, c.content, "feat: "+c.branch)
	}
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	rev := func(r string) string { return gittest.Run(t, dir, "rev-parse", r) }
	var branches []stack.Branch
	parent := "main"
	for _, c := range commits {
		branches = append(branches, stack.Branch{Name: c.branch, Head: rev(c.branch), Base: rev(parent)})
		parent = c.branch
	}
	graph := stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: branches}})
	fb := &fakeBackend{git: g, graph: graph}
	return &restackFixture{app: app.New(app.Deps{Git: g, Meta: fb, Tracker: fb}), fb: fb, repo: repo, dir: dir}
}

func (f *restackFixture) names() []string { return f.fb.graph.Stacks[0].Names() }

func (f *restackFixture) has(t *testing.T, branch, file string) bool {
	t.Helper()
	_, err := gittest.RunErr(t, f.dir, "cat-file", "-e", branch+":"+file)
	return err == nil
}

func TestReorderSwapsBranches(t *testing.T) {
	f := newRestackFixture(t) // main -> a -> b -> c, HEAD on c
	ctx := context.Background()
	res, err := f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: []string{"b", "a", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !slices.Equal(res.Before, []string{"a", "b", "c"}) || !slices.Equal(res.After, []string{"b", "a", "c"}) || !slices.Equal(res.Restacked, []string{"b", "a", "c"}) {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(f.names(), []string{"b", "a", "c"}) {
		t.Errorf("order = %v", f.names())
	}
	if f.has(t, "b", "a.txt") || !f.has(t, "a", "b.txt") || !f.has(t, "c", "a.txt") || !f.has(t, "c", "b.txt") {
		t.Error("the trees do not match the new order")
	}
	if f.base("b") != f.rev(t, "main") || f.base("a") != f.rev(t, "b") || f.base("c") != f.rev(t, "a") {
		t.Errorf("bases b=%s a=%s c=%s", f.base("b"), f.base("a"), f.base("c"))
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("current = %s", cur)
	}
}

func TestReorderKeepsValidBases(t *testing.T) {
	f := newRestackFixture(t)
	f.amend(t, "a", "a2.txt") // b and c are no longer on a; their bases still mark their own commits
	res, err := f.app.Reorder(context.Background(), f.repo, app.ReorderOptions{Order: []string{"a", "c", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Restacked, []string{"c", "b"}) {
		t.Errorf("restacked %v", res.Restacked)
	}
	if !f.has(t, "c", "a2.txt") || f.has(t, "c", "b.txt") || !f.has(t, "b", "c.txt") {
		t.Error("c should sit on the amended a without b's file, b on c")
	}
}

func TestReorderNothingToDoAndRefusals(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	res, err := f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: []string{"a", "b", "c"}})
	if err != nil || res.Changed || len(res.Restacked) != 0 {
		t.Errorf("same order: %+v %v", res, err)
	}
	for _, tc := range []struct {
		order []string
		want  string
	}{
		{[]string{"a", "b"}, "c"},
		{[]string{"a", "b", "c", "d"}, "d"},
		{[]string{"a", "a", "c"}, "twice"},
		{[]string{"c", "b", "x"}, "x"},
	} {
		_, err := f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: tc.order})
		if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("order %v: %v", tc.order, err)
		}
	}
	_, err = f.app.Reorder(ctx, f.repo, app.ReorderOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInteractionRequired}) {
		t.Errorf("no order and no picker: %v", err)
	}
	picked := false
	res, err = f.app.Reorder(ctx, f.repo, app.ReorderOptions{Pick: func(trunk string, branches []string, current string) ([]string, error) {
		picked = trunk == "main" && slices.Equal(branches, []string{"a", "b", "c"}) && current == "c"
		return nil, nil
	}})
	if err != nil || !res.Cancelled || !picked {
		t.Errorf("cancelled pick: %+v %v picked=%v", res, err, picked)
	}
	f.fb.graph.Stacks[0].Branches[0].PR = &stack.PRRef{Number: 1, Merged: true}
	_, err = f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: []string{"b", "a", "c"}})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "merged") {
		t.Errorf("merged branch: %v", err)
	}
	gittest.Run(t, f.dir, "switch", "-q", "main")
	_, err = f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: []string{"b", "a", "c"}})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("on trunk: %v", err)
	}
}

func TestReorderRefusesAOneBranchStack(t *testing.T) {
	f := newLinearFixture(t, []fileCommit{{"a", "a.txt", "a"}})
	_, err := f.app.Reorder(context.Background(), f.repo, app.ReorderOptions{Order: []string{"a"}})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "nothing to reorder") {
		t.Errorf("one branch: %v", err)
	}
}

func TestReorderResultDoesNotAliasTheOrder(t *testing.T) {
	f := newRestackFixture(t)
	order := []string{"a", "b", "c"}
	res, err := f.app.Reorder(context.Background(), f.repo, app.ReorderOptions{Order: order})
	if err != nil {
		t.Fatal(err)
	}
	order[0] = "zzz"
	if !slices.Equal(res.After, []string{"a", "b", "c"}) {
		t.Errorf("After follows the caller's slice: %v", res.After)
	}
}

// reorderConflictFixture: main (shared.txt "base") -> a (shared.txt "a") -> b (shared.txt "b") -> c (c.txt).
// Putting b under a conflicts: b's change assumes a's.
func reorderConflictFixture(t *testing.T) *restackFixture {
	return newLinearFixture(t, []fileCommit{{"a", "shared.txt", "a"}, {"b", "shared.txt", "b"}, {"c", "c.txt", "c"}})
}

func TestReorderConflictThenAbortRestoresOrder(t *testing.T) {
	f := reorderConflictFixture(t)
	ctx := context.Background()
	tips := map[string]string{"a": f.rev(t, "a"), "b": f.rev(t, "b"), "c": f.rev(t, "c")}
	bases := map[string]string{"a": f.base("a"), "b": f.base("b"), "c": f.base("c")}
	_, err := f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: []string{"b", "a", "c"}})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "b" {
		t.Fatalf("want a conflict on b, got %v", err)
	}
	if !slices.Equal(f.names(), []string{"b", "a", "c"}) {
		t.Errorf("order while conflicted = %v", f.names())
	}
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true})
	if err != nil || !res.OrderRestored {
		t.Fatalf("abort: %+v %v", res, err)
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Errorf("order after abort = %v", f.names())
	}
	for n, tip := range tips {
		if f.rev(t, n) != tip || f.base(n) != bases[n] {
			t.Errorf("%s: tip %s base %s, want %s %s", n, f.rev(t, n), f.base(n), tip, bases[n])
		}
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("current = %s", cur)
	}
}

// Swapping two branches that change the same line conflicts twice: b onto
// main, then a onto the resolved b. Continue drains both and then c.
func TestReorderConflictThenContinue(t *testing.T) {
	f := reorderConflictFixture(t)
	ctx := context.Background()
	_, err := f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: []string{"b", "a", "c"}})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "b" {
		t.Fatal(err)
	}
	gittest.WriteFile(t, f.dir, "shared.txt", "b")
	gittest.Run(t, f.dir, "add", "shared.txt")
	_, err = f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "a" {
		t.Fatalf("want the second conflict on a, got %v", err)
	}
	gittest.WriteFile(t, f.dir, "shared.txt", "ab")
	gittest.Run(t, f.dir, "add", "shared.txt")
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := moved(res); !slices.Equal(got, []string{"b", "a", "c"}) {
		t.Errorf("moved %v", got)
	}
	if !slices.Equal(f.names(), []string{"b", "a", "c"}) || !f.has(t, "c", "c.txt") {
		t.Errorf("order %v", f.names())
	}
	if got := gittest.Run(t, f.dir, "show", "c:shared.txt"); got != "ab" {
		t.Errorf("c:shared.txt = %q", got)
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("current = %s", cur)
	}
}

func TestReorderRollsBackWhenNothingMoved(t *testing.T) {
	f := reorderConflictFixture(t)
	ctx := context.Background()
	// The conflict would need b checked out, which a dirty tree forbids,
	// so the run refuses before any ref moves.
	gittest.WriteFile(t, f.dir, "dirty.txt", "x")
	gittest.Run(t, f.dir, "add", "dirty.txt")
	bases := map[string]string{"a": f.base("a"), "b": f.base("b"), "c": f.base("c")}
	_, err := f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: []string{"b", "a", "c"}})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindInvalidArgs || !slices.Contains(se.NextSteps, "nothing was changed") {
		t.Fatalf("want a refusal whose next steps say nothing was changed, got %v", err)
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Errorf("order = %v", f.names())
	}
	for n, b := range bases {
		if f.base(n) != b {
			t.Errorf("%s base changed", n)
		}
	}
	if st, _ := gittest.RunErr(t, f.dir, "status", "--porcelain"); !strings.Contains(st, "dirty.txt") {
		t.Error("the staged file should still be there")
	}
}

// A branch checked out with uncommitted changes in another worktree can't
// be replayed, and skipping it would lose the branch above it, so the
// reorder refuses before touching anything.
func TestReorderRefusesABranchDirtyInAnotherWorktree(t *testing.T) {
	f := newRestackFixture(t) // main -> a -> b -> c, HEAD on c
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "b")
	gittest.WriteFile(t, wt, "shared.txt", "dirty")
	tips := map[string]string{"a": f.rev(t, "a"), "b": f.rev(t, "b"), "c": f.rev(t, "c")}
	_, err := f.app.Reorder(context.Background(), f.repo, app.ReorderOptions{Order: []string{"b", "a", "c"}})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Fatalf("want invalid_args, got %v", err)
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Errorf("order = %v", f.names())
	}
	for n, tip := range tips {
		if f.rev(t, n) != tip {
			t.Errorf("%s moved", n)
		}
	}
	if log := gittest.Run(t, f.dir, "log", "--format=%s", "main..a"); log != "feat: a" {
		t.Errorf("log main..a = %q", log)
	}
}
