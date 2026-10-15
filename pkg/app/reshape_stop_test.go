package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/stack/stacktest"
)

// stopFixture is a real repository whose stacks live in the in-memory
// backend, for reshapes that stop part way.
type stopFixture struct {
	dir  string
	repo git.Repo
	a    *App
	be   *stacktest.Backend
}

func (f *stopFixture) rev(t *testing.T, r string) string {
	t.Helper()
	return gittest.Run(t, f.dir, "rev-parse", r)
}

// branchOff creates name on from with one commit writing file.
func branchOff(t *testing.T, dir, from, name, file, content string) {
	t.Helper()
	gittest.Run(t, dir, "switch", "-q", "-c", name, from)
	gittest.Commit(t, dir, file, content, "feat: "+name)
}

// newStopFixture builds main (shared.txt "base"), then calls build, then
// tracks stacks: each is a trunk and its branches, heads and bases taken
// from the repository, with ids S1, S2... and numbers 7, 8...
func newStopFixture(t *testing.T, build func(dir string), stacks ...[]string) *stopFixture {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Commit(t, dir, "shared.txt", "base", "shared")
	build(dir)
	ctx := context.Background()
	g := git.New(exec.New())
	repo, err := g.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	rev := func(r string) string { return gittest.Run(t, dir, "rev-parse", r) }
	var ss []stack.Stack
	for i, names := range stacks {
		s := stack.Stack{Trunk: names[0], ID: "S" + string(rune('1'+i)), Number: 7 + i}
		parent := names[0]
		for _, n := range names[1:] {
			s.Branches = append(s.Branches, stack.Branch{Name: n, Head: rev(n), Base: rev(parent)})
			parent = n
		}
		ss = append(ss, s)
	}
	be := stacktest.New(g, stack.NewGraph(ss))
	return &stopFixture{dir: dir, repo: repo, a: New(Deps{Git: g, Meta: be}), be: be}
}

// reorder swaps the first two branches of the first stack.
func reorderEdit() reshapeEdit {
	return reshapeEdit{
		touched: []string{"a"},
		relink:  []string{"a"},
		edit: func(g *stack.Graph) error {
			bs := g.Stacks[0].Branches
			bs[0], bs[1] = bs[1], bs[0]
			return nil
		},
		segments: []restackSegment{{Parent: "main", Branches: []string{"b", "a", "c"}}},
	}
}

// onlyMoveEdit moves a alone from main -> a -> b -> c onto y, the way
// move --only does: b and c are left behind on main.
func onlyMoveEdit() reshapeEdit {
	return reshapeEdit{
		touched: []string{"a", "y"},
		relink:  []string{"a"},
		edit: func(g *stack.Graph) error {
			src, dst := &g.Stacks[0], &g.Stacks[1]
			dst.Branches = append(dst.Branches, src.Branches[0])
			src.Branches = src.Branches[1:]
			return nil
		},
		segments: []restackSegment{{Parent: "y", Branches: []string{"a"}}, {Parent: "main", Branches: []string{"b", "c"}}},
	}
}

func isKind(err error, k stack.Kind) bool { return errors.Is(err, &stack.Error{Kind: k}) }

// A reorder must not skip a branch that is checked out dirty in another
// worktree: the branch would keep the commits of the one that used to sit
// below it, and the next restack would drop them from both.
func TestReshapeRefusesADirtyCheckoutElsewhere(t *testing.T) {
	var wt string
	f := newStopFixture(t, func(dir string) {
		branchOff(t, dir, "main", "a", "a.txt", "a")
		branchOff(t, dir, "a", "b", "b.txt", "b")
		branchOff(t, dir, "b", "c", "c.txt", "c")
		gittest.Run(t, dir, "switch", "-q", "a")
		wt = filepath.Join(t.TempDir(), "wt")
		gittest.Run(t, dir, "worktree", "add", "-q", wt, "b")
		gittest.Run(t, dir, "switch", "-q", "c")
	}, []string{"main", "a", "b", "c"})
	if err := os.WriteFile(filepath.Join(wt, "shared.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	tips := map[string]string{}
	for _, n := range []string{"a", "b", "c"} {
		tips[n] = f.rev(t, n)
	}
	ctx := context.Background()
	_, err := f.a.reshape(ctx, f.repo, "git stack reorder", "Reordering", reorderEdit())
	if !isKind(err, stack.KindInvalidArgs) || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v", err)
	}
	if got := f.be.Graph().Stacks[0]; !slices.Equal(got.Names(), []string{"a", "b", "c"}) || got.ID != "S1" {
		t.Errorf("stack = %+v, want the old order and id back", got)
	}
	for n, tip := range tips {
		if f.rev(t, n) != tip {
			t.Errorf("%s moved", n)
		}
	}
	if _, err := gittest.RunErr(t, f.dir, "merge-base", "--is-ancestor", tips["a"], "a"); err != nil {
		t.Error("a's commit is no longer on a")
	}
	if st, _ := loadRestackState(f.repo); st != nil {
		t.Errorf("state saved: %+v", st)
	}
	if p, _ := loadPendingRemote(f.repo); p != nil {
		t.Errorf("pending dissolve kept: %+v", p)
	}
}

// A reorder whose conflicting branch is checked out in another worktree
// can't hand the conflict over, so it is rolled back like any refusal
// before a ref moved.
func TestReshapeRollsBackAConflictCheckedOutElsewhere(t *testing.T) {
	f := newStopFixture(t, func(dir string) {
		branchOff(t, dir, "main", "a", "a.txt", "a")
		branchOff(t, dir, "a", "b", "a.txt", "b") // b onto main conflicts
		branchOff(t, dir, "b", "c", "c.txt", "c")
		gittest.Run(t, dir, "switch", "-q", "a")
		gittest.Run(t, dir, "worktree", "add", "-q", filepath.Join(t.TempDir(), "wt"), "b")
		gittest.Run(t, dir, "switch", "-q", "c")
	}, []string{"main", "a", "b", "c"})
	ctx := context.Background()
	_, err := f.a.reshape(ctx, f.repo, "git stack reorder", "Reordering", reorderEdit())
	if err == nil || !strings.Contains(err.Error(), "checked out in") {
		t.Fatalf("err = %v", err)
	}
	if se, ok := errors.AsType[*stack.Error](err); !ok || !slices.Contains(se.NextSteps, "nothing was changed") {
		t.Errorf("steps = %v", err)
	}
	if got := f.be.Graph().Stacks[0].Names(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("order = %v", got)
	}
	if _, err := f.a.Restack(ctx, f.repo, RestackOptions{Abort: true}); err == nil || !strings.Contains(err.Error(), "nothing to abort") {
		t.Errorf("abort = %v", err)
	}
}

// onlyMoveFixture is main -> a -> b -> c and main -> y, HEAD on c.
func onlyMoveFixture(t *testing.T, build func(dir string)) *stopFixture {
	return newStopFixture(t, func(dir string) {
		branchOff(t, dir, "main", "y", "y.txt", "y")
		build(dir)
		gittest.Run(t, dir, "switch", "-q", "c")
	}, []string{"main", "a", "b", "c"}, []string{"main", "y"})
}

// checkPutBack checks the stacks, refs and bases are as before the move.
func checkPutBack(t *testing.T, f *stopFixture, before *stack.Graph, tips map[string]string) {
	t.Helper()
	got := f.be.Graph()
	if want := []string{"main:a,b,c", "main:y"}; !slices.Equal(trunksAndNames(got), want) {
		t.Fatalf("stacks = %v", trunksAndNames(got))
	}
	for i, s := range got.Stacks {
		if s.ID != before.Stacks[i].ID || s.Number != before.Stacks[i].Number {
			t.Errorf("stack %d id = %s/%d", i, s.ID, s.Number)
		}
		for j, b := range s.Branches {
			if was := before.Stacks[i].Branches[j]; b.Base != was.Base || b.Head != was.Head {
				t.Errorf("%s = %+v, was %+v", b.Name, b, was)
			}
		}
	}
	for n, tip := range tips {
		if f.rev(t, n) != tip {
			t.Errorf("%s not put back", n)
		}
	}
	if st, _ := loadRestackState(f.repo); st != nil {
		t.Errorf("state left: %+v", st)
	}
	if p, _ := loadPendingRemote(f.repo); p != nil {
		t.Errorf("pending dissolve kept: %+v", p)
	}
}

func cloneGraph(g *stack.Graph) *stack.Graph {
	ss := slices.Clone(g.Stacks)
	for i := range ss {
		ss[i].Branches = slices.Clone(ss[i].Branches)
	}
	return stack.NewGraph(ss)
}

// A move --only whose left behind branches are refused after the moved
// branch went (here: their restack conflicts while the working tree is
// dirty) is saved, so abort undoes the whole move and continue finishes it.
func TestReshapeOnlyMoveStoppedLeavesAState(t *testing.T) {
	for _, then := range []string{"abort", "continue"} {
		t.Run(then, func(t *testing.T) {
			f := onlyMoveFixture(t, func(dir string) {
				branchOff(t, dir, "main", "a", "a.txt", "a")
				branchOff(t, dir, "a", "b", "a.txt", "b") // b onto main conflicts
				branchOff(t, dir, "b", "c", "c.txt", "c")
			})
			before := cloneGraph(f.be.Graph())
			tips := map[string]string{}
			for _, n := range []string{"a", "b", "c", "y"} {
				tips[n] = f.rev(t, n)
			}
			if err := os.WriteFile(filepath.Join(f.dir, "shared.txt"), []byte("dirty"), 0o644); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			_, err := f.a.reshape(ctx, f.repo, "git stack move", "Moving", onlyMoveEdit())
			se, ok := errors.AsType[*stack.Error](err)
			if !ok || se.Kind != stack.KindInvalidArgs || !slices.Contains(se.NextSteps, "fix that, then git stack continue finishes the move") {
				t.Fatalf("err = %v (%+v)", err, se)
			}
			if f.rev(t, "a~1") != tips["y"] {
				t.Fatal("a should already be on y")
			}
			st, _ := loadRestackState(f.repo)
			if st == nil || st.Conflict != "" || st.Reshape == nil || len(st.Moved) != 1 || st.Moved[0].Name != "a" ||
				len(st.Then) != 1 || !slices.Equal(st.Then[0].Branches, []string{"b", "c"}) {
				t.Fatalf("state = %+v", st)
			}
			_, err = f.a.Restack(ctx, f.repo, RestackOptions{})
			if !isKind(err, stack.KindRebaseActive) || !strings.Contains(err.Error(), "interrupted") {
				t.Errorf("restack while stopped = %v", err)
			}
			if then == "continue" {
				gittest.Run(t, f.dir, "checkout", "--", "shared.txt")
				_, err := f.a.Restack(ctx, f.repo, RestackOptions{Continue: true})
				if !isKind(err, stack.KindConflict) {
					t.Fatalf("continue = %v, want b's conflict", err)
				}
				if st, _ := loadRestackState(f.repo); st == nil || st.Conflict != "b" || st.Reshape == nil || len(st.Moved) != 1 {
					t.Fatalf("state after continue = %+v", st)
				}
			}
			res, err := f.a.Restack(ctx, f.repo, RestackOptions{Abort: true})
			if err != nil || !res.OrderRestored {
				t.Fatalf("abort = %+v %v", res, err)
			}
			checkPutBack(t, f, before, tips)
		})
	}
}

// Once whatever stopped the left behind branches is fixed, continue
// finishes the move.
func TestReshapeOnlyMoveStoppedThenContinued(t *testing.T) {
	var wt string
	f := onlyMoveFixture(t, func(dir string) {
		branchOff(t, dir, "main", "a", "a.txt", "a")
		branchOff(t, dir, "a", "b", "b.txt", "b")
		branchOff(t, dir, "b", "c", "c.txt", "c")
		gittest.Run(t, dir, "switch", "-q", "a")
		wt = filepath.Join(t.TempDir(), "wt")
		gittest.Run(t, dir, "worktree", "add", "-q", wt, "b")
	})
	if err := os.WriteFile(filepath.Join(wt, "shared.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err := f.a.reshape(ctx, f.repo, "git stack move", "Moving", onlyMoveEdit())
	if !isKind(err, stack.KindInvalidArgs) {
		t.Fatalf("err = %v", err)
	}
	if st, _ := loadRestackState(f.repo); st == nil || st.Conflict != "" {
		t.Fatalf("state = %+v", st)
	}
	gittest.Run(t, wt, "checkout", "--", "shared.txt")
	res, err := f.a.Restack(ctx, f.repo, RestackOptions{Continue: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Branches, []string{"b", "c"}) {
		t.Errorf("result = %+v", res)
	}
	if want := []string{"main:b,c", "main:y,a"}; !slices.Equal(trunksAndNames(f.be.Graph()), want) {
		t.Errorf("stacks = %v", trunksAndNames(f.be.Graph()))
	}
	if f.rev(t, "b~1") != f.rev(t, "main") || f.rev(t, "c~1") != f.rev(t, "b") || f.rev(t, "a~1") != f.rev(t, "y") {
		t.Error("branches not on their new parents")
	}
	if _, err := gittest.RunErr(t, f.dir, "cat-file", "-e", "b:a.txt"); err == nil {
		t.Error("b still carries a's commit")
	}
	if gittest.Run(t, wt, "rev-parse", "HEAD") != f.rev(t, "b") {
		t.Error("the other worktree's checkout was not moved with b")
	}
	if st, _ := loadRestackState(f.repo); st != nil {
		t.Errorf("state left: %+v", st)
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("current = %s", cur)
	}
}

// cloningMeta gives the in-memory backend the real adapter's contract: fn
// edits a copy, which is kept only when fn and the write succeed.
type cloningMeta struct {
	*stacktest.Backend
	failWrite bool
}

func (m *cloningMeta) Update(ctx context.Context, repo git.Repo, fn func(*stack.Graph) error) error {
	g := cloneGraph(m.Graph())
	if err := fn(g); err != nil {
		return err
	}
	if m.failWrite {
		return errors.New("disk full")
	}
	return m.Backend.Update(ctx, repo, func(cur *stack.Graph) error {
		*cur = *g
		return nil
	})
}

// A failed write never leaves a stack's remote id cleared with nothing
// queued to dissolve it, nor a dissolve queued for an id still recorded.
func TestReshapeFailedWriteKeepsIDAndQueueInStep(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failPending bool
	}{{"queue write fails", true}, {"metadata write fails", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStopFixture(t, func(dir string) {
				branchOff(t, dir, "main", "a", "a.txt", "a")
				branchOff(t, dir, "a", "b", "b.txt", "b")
				branchOff(t, dir, "b", "c", "c.txt", "c")
			}, []string{"main", "a", "b", "c"})
			meta := &cloningMeta{Backend: f.be, failWrite: !tc.failPending}
			f.a = New(Deps{Git: f.a.d.Git, Meta: meta})
			if tc.failPending {
				// A directory where the queue file goes makes saving it fail.
				if err := os.MkdirAll(remoteStacksPath(f.repo), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(remoteStacksPath(f.repo), "x"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.a.reshape(context.Background(), f.repo, "git stack reorder", "Reordering", reorderEdit())
			if err == nil {
				t.Fatal("want the write's error")
			}
			if s := f.be.Graph().Stacks[0]; s.ID != "S1" || !slices.Equal(s.Names(), []string{"a", "b", "c"}) {
				t.Errorf("stack = %+v", s)
			}
			if !tc.failPending {
				if p, _ := loadPendingRemote(f.repo); p != nil {
					t.Errorf("dissolve queued for a stack that kept its id: %+v", p)
				}
			}
		})
	}
}

// A continue that is refused for the same reason as the first run keeps
// the state, so its steps are continue and abort, not the original command.
func TestReshapeContinueRefusedAgainPointsAtContinueAndAbort(t *testing.T) {
	var wt string
	f := onlyMoveFixture(t, func(dir string) {
		branchOff(t, dir, "main", "a", "a.txt", "a")
		branchOff(t, dir, "a", "b", "b.txt", "b")
		branchOff(t, dir, "b", "c", "c.txt", "c")
		gittest.Run(t, dir, "switch", "-q", "a")
		wt = filepath.Join(t.TempDir(), "wt")
		gittest.Run(t, dir, "worktree", "add", "-q", wt, "b")
	})
	if err := os.WriteFile(filepath.Join(wt, "shared.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := f.a.reshape(ctx, f.repo, "git stack move", "Moving", onlyMoveEdit()); !isKind(err, stack.KindInvalidArgs) {
		t.Fatalf("first run = %v", err)
	}
	_, err := f.a.Restack(ctx, f.repo, RestackOptions{Continue: true})
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindInvalidArgs {
		t.Fatalf("continue = %v", err)
	}
	steps := strings.Join(se.NextSteps, "\n")
	if !strings.Contains(steps, "git stack continue") || !strings.Contains(steps, "git stack abort") || strings.Contains(steps, "git stack move") {
		t.Errorf("steps = %q", se.NextSteps)
	}
	if st, _ := loadRestackState(f.repo); st == nil {
		t.Error("state was dropped")
	}
}

// A continue whose queued branches are all gone has nothing to restack, but
// it is still the end of the operation: the state must not linger.
func TestContinueWithNothingLeftClearsTheState(t *testing.T) {
	f := newStopFixture(t, func(dir string) {
		branchOff(t, dir, "main", "a", "a.txt", "a")
		branchOff(t, dir, "a", "b", "b.txt", "b")
		gittest.Run(t, dir, "switch", "-q", "a")
	}, []string{"main", "a", "b"})
	err := saveRestackState(f.repo, &restackState{
		Command: "git stack move", OriginalBranch: "b", Trunk: "main",
		Then: []restackSegment{{Parent: "main", Branches: []string{"ghost"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Restack(context.Background(), f.repo, RestackOptions{Continue: true}); err != nil {
		t.Fatal(err)
	}
	if st, _ := loadRestackState(f.repo); st != nil {
		t.Errorf("state left: %+v", st)
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "b" {
		t.Errorf("current = %s, want the original branch", cur)
	}
}

// The checked out branch is reset before the ref transaction; if that
// transaction fails, abort must still put the branch back.
func TestAbortPutsBackTheCheckedOutBranchAfterAFailedTransaction(t *testing.T) {
	f := newStopFixture(t, func(dir string) {
		branchOff(t, dir, "main", "a", "a.txt", "a")
		branchOff(t, dir, "a", "b", "b.txt", "b")
		branchOff(t, dir, "b", "c", "c.txt", "c")
	}, []string{"main", "a", "b", "c"})
	before := cloneGraph(f.be.Graph())
	tips := map[string]string{}
	for _, n := range []string{"a", "b", "c"} {
		tips[n] = f.rev(t, n)
	}
	// A held lock on b's ref makes the transaction fail after c was reset.
	lock := filepath.Join(f.dir, ".git", "refs", "heads", "b.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err := f.a.reshape(ctx, f.repo, "git stack reorder", "Reordering", reorderEdit())
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || !slices.Contains(se.NextSteps, "or git stack abort puts everything back") {
		t.Fatalf("err = %v", err)
	}
	if st, _ := loadRestackState(f.repo); st == nil || !slices.ContainsFunc(st.Moved, func(m movedRef) bool { return m.Name == "c" }) {
		t.Fatalf("state = %+v, want c recorded as moved", st)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Restack(ctx, f.repo, RestackOptions{Abort: true}); err != nil {
		t.Fatal(err)
	}
	for n, tip := range tips {
		if f.rev(t, n) != tip {
			t.Errorf("%s not put back", n)
		}
	}
	if got := f.be.Graph().Stacks[0]; !slices.Equal(got.Names(), before.Stacks[0].Names()) {
		t.Errorf("stack = %v", got.Names())
	}
	if st, _ := loadRestackState(f.repo); st != nil {
		t.Errorf("state left: %+v", st)
	}
}
