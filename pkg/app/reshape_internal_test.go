package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/stack/stacktest"
)

func reshapeApp(g *stack.Graph) (*App, *stacktest.Backend) {
	be := stacktest.New(nil, g)
	return New(Deps{Meta: be}), be
}

func names(s stack.Stack) []string { return s.Names() }

func TestRestoreReshapePutsAMoveBack(t *testing.T) {
	g := stack.NewGraph([]stack.Stack{
		{Trunk: "main", ID: "S1", Number: 1, Branches: []stack.Branch{{Name: "a", Base: "m"}, {Name: "b", Base: "ta"}, {Name: "c", Base: "tb"}}},
		{Trunk: "main", Branches: []stack.Branch{{Name: "x", Base: "m"}}},
	})
	snap := snapshotOf(g, []string{"b", "x"})
	if len(snap.Stacks) != 2 || snap.Stacks[0].ID != "S1" || snap.Stacks[0].Branches[1].Base != "ta" {
		t.Fatalf("snapshot = %+v", snap)
	}
	// Move b and c onto x; heads moved, bases rewritten, id cleared.
	g.Stacks[0].Branches = g.Stacks[0].Branches[:1]
	g.Stacks[0].ID, g.Stacks[0].Number = "", 0
	g.Stacks[1].Branches = append(g.Stacks[1].Branches, stack.Branch{Name: "b", Head: "nb", Base: "tx"}, stack.Branch{Name: "c", Head: "nc", Base: "nb"})
	a, be := reshapeApp(g)
	repo := git.Repo{CommonDir: t.TempDir()}
	if err := addPendingRemote(repo, []remoteStack{{ID: "S1", Number: 1, Stack: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.restoreReshape(context.Background(), repo, snap); err != nil {
		t.Fatal(err)
	}
	got := be.Graph()
	if len(got.Stacks) != 2 || !slices.Equal(names(got.Stacks[0]), []string{"a", "b", "c"}) || !slices.Equal(names(got.Stacks[1]), []string{"x"}) {
		t.Fatalf("stacks = %+v", got.Stacks)
	}
	if b := got.Stacks[0].Branches[1]; b.Head != "nb" || b.Base != "ta" {
		t.Errorf("b = %+v, want head kept and base restored", b)
	}
	if got.Stacks[0].ID != "S1" || got.Stacks[0].Number != 1 {
		t.Errorf("remote id not restored: %+v", got.Stacks[0])
	}
	if p, _ := loadPendingRemote(repo); p != nil {
		t.Errorf("pending dissolve should be dropped: %+v", p)
	}
}

func TestRestoreReshapeRemovesACreatedStack(t *testing.T) {
	g := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a", Base: "m"}, {Name: "b", Base: "ta"}}},
	})
	snap := snapshotOf(g, []string{"a"})
	snap.Created = []string{"b"}
	g.Stacks[0].Branches = g.Stacks[0].Branches[:1]
	g.Stacks = append(g.Stacks, stack.Stack{Trunk: "main", Branches: []stack.Branch{{Name: "b", Base: "m"}}})
	a, be := reshapeApp(g)
	if err := a.restoreReshape(context.Background(), git.Repo{CommonDir: t.TempDir()}, snap); err != nil {
		t.Fatal(err)
	}
	got := be.Graph()
	if len(got.Stacks) != 1 || !slices.Equal(names(got.Stacks[0]), []string{"a", "b"}) || got.Stacks[0].Branches[1].Base != "ta" {
		t.Errorf("stacks = %+v", got.Stacks)
	}
}

// A reorder through the driver: the remote id is queued for dissolving
// and every branch is replayed onto its new parent, with no commits of
// the branch that used to sit below it.
func TestReshapeReordersAndQueuesTheRemoteStack(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Commit(t, dir, "shared.txt", "base", "shared")
	for _, n := range []string{"a", "b", "c"} {
		gittest.Run(t, dir, "switch", "-q", "-c", n)
		gittest.Commit(t, dir, n+".txt", n, "feat: "+n)
	}
	ctx := context.Background()
	g := git.New(exec.New())
	repo, err := g.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	rev := func(r string) string { return gittest.Run(t, dir, "rev-parse", r) }
	be := stacktest.New(g, stack.NewGraph([]stack.Stack{{Trunk: "main", ID: "S1", Number: 3, Branches: []stack.Branch{
		{Name: "a", Head: rev("a"), Base: rev("main")},
		{Name: "b", Head: rev("b"), Base: rev("a")},
		{Name: "c", Head: rev("c"), Base: rev("b")},
	}}}))
	a := New(Deps{Git: g, Meta: be})
	res, err := a.reshape(ctx, repo, "git stack reorder", "Reordering", reshapeEdit{
		touched: []string{"a"},
		relink:  []string{"a"},
		edit: func(g *stack.Graph) error {
			bs := g.Stacks[0].Branches
			bs[0], bs[1] = bs[1], bs[0]
			return nil
		},
		segments: []restackSegment{{Parent: "main", Branches: []string{"b", "a", "c"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Branches, []string{"b", "a", "c"}) || len(res.Moved) != 3 {
		t.Errorf("result = %+v", res)
	}
	got := be.Graph().Stacks[0]
	if !slices.Equal(names(got), []string{"b", "a", "c"}) || got.ID != "" || got.Number != 0 {
		t.Errorf("stack = %+v", got)
	}
	if p, _ := loadPendingRemote(repo); p == nil || len(p.Dissolve) != 1 || p.Dissolve[0] != (remoteStack{ID: "S1", Number: 3, Stack: "a"}) {
		t.Errorf("pending = %+v", p)
	}
	if _, err := gittest.RunErr(t, dir, "cat-file", "-e", "b:a.txt"); err == nil {
		t.Error("b still carries a's commit")
	}
	if rev("b~1") != rev("main") || rev("a~1") != rev("b") || rev("c~1") != rev("a") {
		t.Error("branches not replayed onto their new parents")
	}
	if st, _ := loadRestackState(repo); st != nil {
		t.Errorf("state left behind: %+v", st)
	}
}

// trunksAndNames renders each stack as trunk:[branches] for comparison.
func trunksAndNames(g *stack.Graph) []string {
	var out []string
	for _, s := range g.Stacks {
		out = append(out, s.Trunk+":"+strings.Join(s.Names(), ","))
	}
	return out
}

func TestRestoreReshapePutsAStackBackOnItsTrunk(t *testing.T) {
	g := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Worktree: "/w", ID: "S1", Number: 1, Branches: []stack.Branch{{Name: "a", Base: "m"}, {Name: "b", Base: "ta"}}},
	})
	snap := snapshotOf(g, []string{"a"})
	snap.Created = []string{"a"}
	// The whole stack moved onto develop: a new stack there, the old one gone.
	g = stack.NewGraph([]stack.Stack{
		{Trunk: "develop", Branches: []stack.Branch{{Name: "a", Head: "na", Base: "d"}, {Name: "b", Head: "nb", Base: "na"}}},
	})
	a, be := reshapeApp(g)
	if err := a.restoreReshape(context.Background(), git.Repo{CommonDir: t.TempDir()}, snap); err != nil {
		t.Fatal(err)
	}
	got := be.Graph()
	if want := []string{"main:a,b"}; !slices.Equal(trunksAndNames(got), want) {
		t.Fatalf("stacks = %v, want %v", trunksAndNames(got), want)
	}
	if s := got.Stacks[0]; s.Worktree != "/w" || s.ID != "S1" || s.Branches[0].Base != "m" || s.Branches[0].Head != "na" {
		t.Errorf("stack = %+v", s)
	}
}

func TestRestoreReshapeUndoesAnOnlyMoveOfABottomBranchAcrossTrunks(t *testing.T) {
	g := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a", Base: "m"}, {Name: "b", Base: "ta"}, {Name: "c", Base: "tb"}}},
		{Trunk: "develop", Branches: []stack.Branch{{Name: "x", Base: "d"}}},
	})
	snap := snapshotOf(g, []string{"a", "x"})
	g.Stacks[0].Branches = g.Stacks[0].Branches[1:]
	g.Stacks[1].Branches = append(g.Stacks[1].Branches, stack.Branch{Name: "a", Base: "tx"})
	a, be := reshapeApp(g)
	if err := a.restoreReshape(context.Background(), git.Repo{CommonDir: t.TempDir()}, snap); err != nil {
		t.Fatal(err)
	}
	if got, want := trunksAndNames(be.Graph()), []string{"main:a,b,c", "develop:x"}; !slices.Equal(got, want) {
		t.Errorf("stacks = %v, want %v", got, want)
	}
}

func TestRestoreReshapeKeepsBranchesItDoesNotKnow(t *testing.T) {
	g := stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}},
	})
	snap := snapshotOf(g, []string{"a"})
	g.Stacks[0].Branches = []stack.Branch{{Name: "b"}, {Name: "a"}, {Name: "late"}}
	a, be := reshapeApp(g)
	if err := a.restoreReshape(context.Background(), git.Repo{CommonDir: t.TempDir()}, snap); err != nil {
		t.Fatal(err)
	}
	if got, want := trunksAndNames(be.Graph()), []string{"main:a,b,late"}; !slices.Equal(got, want) {
		t.Errorf("stacks = %v, want %v", got, want)
	}
}
