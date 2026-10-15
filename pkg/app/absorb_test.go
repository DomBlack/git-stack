package app_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// numbered is twenty numbered lines, the file every absorb test edits.
func numbered() string {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "%d\n", i)
	}
	return b.String()
}

// withLine replaces line n (1 based) of numbered() with text.
func withLine(base string, n int, text string) string {
	lines := strings.Split(strings.TrimSuffix(base, "\n"), "\n")
	lines[n-1] = text
	return strings.Join(lines, "\n") + "\n"
}

// absorbFixture: main (f.txt numbered 1..20) -> a (line 5 -> five, adds
// a.txt) -> b (line 15 -> fifteen) -> c (adds c.txt), tracked with heads and
// bases recorded, HEAD on b.
type absorbFixture struct {
	app  *app.App
	fb   *fakeBackend
	deps app.Deps
	repo git.Repo
	dir  string
}

func newAbsorbFixture(t *testing.T) *absorbFixture {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Commit(t, dir, "f.txt", numbered(), "numbers")
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	gittest.Commit(t, dir, "f.txt", withLine(numbered(), 5, "five"), "feat: a")
	gittest.Commit(t, dir, "a.txt", "a\n", "feat: a file")
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "f.txt", withLine(withLine(numbered(), 5, "five"), 15, "fifteen"), "feat: b")
	gittest.Run(t, dir, "switch", "-q", "-c", "c")
	gittest.Commit(t, dir, "c.txt", "c\n", "feat: c")
	gittest.Run(t, dir, "switch", "-q", "b")
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	rev := func(r string) string { return gittest.Run(t, dir, "rev-parse", r) }
	fb := &fakeBackend{git: g, graph: stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{
		{Name: "a", Head: rev("a"), Base: rev("main")},
		{Name: "b", Head: rev("b"), Base: rev("a")},
		{Name: "c", Head: rev("c"), Base: rev("b")},
	}}})}
	deps := app.Deps{Git: g, Meta: fb, Tracker: fb}
	return &absorbFixture{app: app.New(deps), fb: fb, deps: deps, repo: repo, dir: dir}
}

func (f *absorbFixture) rev(t *testing.T, r string) string {
	t.Helper()
	return gittest.Run(t, f.dir, "rev-parse", r)
}

// stageLine stages f.txt with line n replaced by text (on top of whatever
// the working tree has).
func (f *absorbFixture) stageLine(t *testing.T, n int, text string) {
	t.Helper()
	// Builds on the index, so several calls accumulate staged edits.
	cur := gittest.Run(t, f.dir, "show", ":f.txt") + "\n"
	gittest.WriteFile(t, f.dir, "f.txt", withLine(cur, n, text))
	gittest.Run(t, f.dir, "add", "f.txt")
}

func targetsOf(p app.AbsorbPlan) []string {
	var out []string
	for _, h := range p.Hunks {
		if h.Target == nil {
			out = append(out, "none")
		} else {
			out = append(out, h.Target.Branch)
		}
	}
	return out
}

func TestAbsorbPlansEachHunk(t *testing.T) {
	f := newAbsorbFixture(t)
	ctx := context.Background()
	f.stageLine(t, 5, "FIVE")
	f.stageLine(t, 15, "FIFTEEN")
	f.stageLine(t, 1, "one")
	gittest.WriteFile(t, f.dir, "new.txt", "new\n")
	gittest.Run(t, f.dir, "add", "new.txt")

	var previewed *app.AbsorbPlan
	res, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{DryRun: true, Preview: func(p *app.AbsorbPlan) { previewed = p }})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Applied || res.Branch != "b" || previewed == nil {
		t.Errorf("res = %+v", res)
	}
	if got := targetsOf(res.Plan); !slices.Equal(got, []string{"none", "a", "b"}) {
		t.Errorf("targets = %v (hunks %+v)", got, res.Plan.Hunks)
	}
	h := res.Plan.Hunks[1]
	if h.Path != "f.txt" || h.OldStart != 5 || h.OldCount != 1 || h.NewCount != 1 || h.Target.SHA != f.rev(t, "a~1") || h.Target.Subject != "feat: a" {
		t.Errorf("hunk = %+v", h)
	}
	if len(res.Plan.Skipped) != 1 || res.Plan.Skipped[0].Path != "new.txt" || res.Plan.Skipped[0].Reason != "new file" {
		t.Errorf("skipped = %+v", res.Plan.Skipped)
	}
	if res.Plan.Absorbed != 2 || res.Plan.Unabsorbed != 1 || res.Plan.Commits != 2 {
		t.Errorf("counts = %+v", res.Plan)
	}
	// A dry run changes no ref.
	if f.rev(t, "a") != f.fb.graph.Stacks[0].Branches[0].Head || f.rev(t, "b") != f.fb.graph.Stacks[0].Branches[1].Head {
		t.Error("dry run moved a branch")
	}
}

func TestAbsorbStagesFirstEvenOnADryRun(t *testing.T) {
	f := newAbsorbFixture(t)
	gittest.WriteFile(t, f.dir, "f.txt", withLine(gittest.Run(t, f.dir, "show", "HEAD:f.txt")+"\n", 5, "FIVE"))
	res, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{DryRun: true, Staging: app.StageUpdate})
	if err != nil || res.Plan.Absorbed != 1 {
		t.Fatalf("res = %+v %v", res, err)
	}
	if out := gittest.Run(t, f.dir, "diff", "--cached", "--name-only"); out != "f.txt" {
		t.Errorf("-u should have staged f.txt, got %q", out)
	}
}

func TestAbsorbRefusals(t *testing.T) {
	f := newAbsorbFixture(t)
	ctx := context.Background()
	wantKind := func(what string, err error, kind stack.Kind) {
		t.Helper()
		if !errors.Is(err, &stack.Error{Kind: kind}) {
			t.Errorf("%s: got %v, want %s", what, err, kind)
		}
	}
	_, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	wantKind("nothing staged", err, stack.KindInvalidArgs)

	// A new file survives the branch switches below, which an edit to f.txt
	// (different on main) would not.
	gittest.WriteFile(t, f.dir, "x.txt", "x\n")
	gittest.Run(t, f.dir, "add", "x.txt")
	gittest.Run(t, f.dir, "switch", "-q", "main")
	_, err = f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	wantKind("trunk", err, stack.KindInvalidArgs)
	gittest.Run(t, f.dir, "switch", "-q", "-c", "loose", "b")
	_, err = f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	wantKind("untracked branch", err, stack.KindNotInStack)
	gittest.Run(t, f.dir, "switch", "-q", "--detach")
	_, err = f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	wantKind("detached", err, stack.KindInvalidArgs)
	gittest.Run(t, f.dir, "switch", "-q", "b")

	// b's own pull request has merged: nothing to rewrite.
	f.fb.graph.Stacks[0].Branches[1].PR = &stack.PRRef{Number: 2, Merged: true}
	_, err = f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	wantKind("merged current", err, stack.KindInvalidArgs)
	f.fb.graph.Stacks[0].Branches[1].PR = nil

	// a moved under b (amended by hand), so b needs restacking first.
	gittest.Run(t, f.dir, "switch", "-q", "--detach", "main")
	other := gittest.Commit(t, f.dir, "other.txt", "other\n", "a, amended")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Run(t, f.dir, "update-ref", "refs/heads/a", other)
	// The commit above took the staged file with it; stage again.
	gittest.WriteFile(t, f.dir, "y.txt", "y\n")
	gittest.Run(t, f.dir, "add", "y.txt")
	_, err = f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	wantKind("needs restack", err, stack.KindInvalidArgs)
	if !strings.Contains(err.Error(), "needs restacking") {
		t.Errorf("message = %q", err.Error())
	}
	gittest.Run(t, f.dir, "update-ref", "refs/heads/a", f.fb.graph.Stacks[0].Branches[0].Head)
}

func TestAbsorbRefusesMergeCommits(t *testing.T) {
	f := newAbsorbFixture(t)
	// b gains a merge commit of a side branch.
	gittest.Run(t, f.dir, "switch", "-q", "-c", "side", "a")
	gittest.Commit(t, f.dir, "s.txt", "s\n", "side")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Run(t, f.dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	f.fb.graph.Stacks[0].Branches[1].Head = f.rev(t, "b")
	f.stageLine(t, 15, "FIFTEEN")
	_, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "merge commit") {
		t.Errorf("err = %v", err)
	}
}

func TestAbsorbMergedBranchBelowIsNotACandidate(t *testing.T) {
	f := newAbsorbFixture(t)
	f.fb.graph.Stacks[0].Branches[0].PR = &stack.PRRef{Number: 1, Merged: true}
	f.stageLine(t, 5, "FIVE")
	f.stageLine(t, 15, "FIFTEEN")
	res, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := targetsOf(res.Plan); !slices.Equal(got, []string{"none", "b"}) {
		t.Errorf("targets = %v", got)
	}
}

func TestAbsorbNothingToAbsorbAndDeclined(t *testing.T) {
	f := newAbsorbFixture(t)
	ctx := context.Background()
	f.stageLine(t, 1, "one")
	res, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	if err != nil || res.Applied || res.Plan.Absorbed != 0 || res.Plan.Unabsorbed != 1 {
		t.Errorf("nothing absorbable: %+v %v", res, err)
	}
	// On a terminal the question is asked; no means nothing happens.
	p := &fakePrompter{confirm: false}
	f.deps.Prompter = p
	a := app.New(f.deps)
	f.stageLine(t, 5, "FIVE")
	res, err = a.Absorb(ctx, f.repo, app.AbsorbOptions{})
	if err != nil || res.Applied || len(p.asked) != 1 || !strings.Contains(p.asked[0], "Absorb 1 hunk into 1 commit?") {
		t.Errorf("declined: %+v %v asked %q", res, err, p.asked)
	}
	if len(res.Notices) == 0 || f.rev(t, "a") != f.fb.graph.Stacks[0].Branches[0].Head {
		t.Errorf("declined should change nothing: %+v", res)
	}
}
