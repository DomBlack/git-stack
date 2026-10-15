package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	if !strings.Contains(err.Error(), "trunk") {
		t.Errorf("trunk message = %q", err.Error())
	}
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
	if se, ok := errors.AsType[*stack.Error](err); !ok || !slices.ContainsFunc(se.NextSteps, func(s string) bool { return strings.Contains(s, "git stack sync") }) {
		t.Errorf("merged current steps = %v", err)
	}
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
	if se, ok := errors.AsType[*stack.Error](err); !ok || !slices.ContainsFunc(se.NextSteps, func(s string) bool { return strings.Contains(s, "git stack restack") }) {
		t.Errorf("needs restack steps = %v", err)
	}
	gittest.Run(t, f.dir, "update-ref", "refs/heads/a", f.fb.graph.Stacks[0].Branches[0].Head)
}

func TestAbsorbRefusesWhileARebaseIsInProgress(t *testing.T) {
	f := newAbsorbFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "-c", "side", "main")
	gittest.Commit(t, f.dir, "f.txt", withLine(numbered(), 5, "side"), "side edit")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	if _, err := gittest.RunErr(t, f.dir, "rebase", "side"); err == nil {
		t.Fatal("the rebase should have stopped on a conflict")
	}
	t.Cleanup(func() { _, _ = gittest.RunErr(t, f.dir, "rebase", "--abort") })
	gittest.WriteFile(t, f.dir, "x.txt", "x\n")
	gittest.Run(t, f.dir, "add", "x.txt")
	_, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindRebaseActive}) {
		t.Errorf("err = %v", err)
	}
}

func TestAbsorbCurrentBranchWithNoCommitsOfItsOwn(t *testing.T) {
	f := newAbsorbFixture(t)
	tip := f.rev(t, "c")
	gittest.Run(t, f.dir, "switch", "-q", "-c", "d", "c")
	f.fb.graph.Stacks[0].Branches = append(f.fb.graph.Stacks[0].Branches, stack.Branch{Name: "d", Head: tip, Base: tip})
	f.stageLine(t, 15, "FIFTEEN")
	res, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{})
	if err != nil || !res.Applied {
		t.Fatalf("res = %+v %v", res, err)
	}
	var names []string
	for _, m := range res.Rewritten {
		names = append(names, m.Name)
	}
	if !slices.Equal(names, []string{"b", "c", "d"}) {
		t.Errorf("rewritten = %v", names)
	}
	if f.rev(t, "d") != f.rev(t, "c") || f.rev(t, "c") == tip {
		t.Errorf("d = %s, c = %s, old c = %s", f.rev(t, "d"), f.rev(t, "c"), tip)
	}
	br := f.fb.graph.Stacks[0].Branches
	if br[3].Name != "d" || br[3].Base != f.rev(t, "c") || br[3].Head != f.rev(t, "d") {
		t.Errorf("d's metadata = %+v", br[3])
	}
}

func TestAbsorbRewritesThreeBranches(t *testing.T) {
	f := newAbsorbFixture(t)
	mainTip := f.rev(t, "main")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	f.stageLine(t, 5, "FIVE")
	res, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{})
	if err != nil || !res.Applied {
		t.Fatalf("res = %+v %v", res, err)
	}
	var names []string
	for _, m := range res.Rewritten {
		names = append(names, m.Name)
	}
	if !slices.Equal(names, []string{"a", "b", "c"}) {
		t.Errorf("rewritten = %v", names)
	}
	br := f.fb.graph.Stacks[0].Branches
	if br[1].Base != f.rev(t, "a") || br[2].Base != f.rev(t, "b") || br[0].Base != mainTip {
		t.Errorf("bases = %q %q %q", br[0].Base, br[1].Base, br[2].Base)
	}
	if got := gittest.Run(t, f.dir, "show", "c:f.txt"); !strings.Contains(got, "\nFIVE\n") {
		t.Errorf("c = %q", got)
	}
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

func TestAbsorbBottomBranchRebasedByHandOntoNewerTrunk(t *testing.T) {
	f := newAbsorbFixture(t)
	ctx := context.Background()
	// main gains a commit that changes line 10, then a, b and c are rebased
	// onto it with plain git. The recorded bases stay stale.
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Commit(t, f.dir, "f.txt", withLine(numbered(), 10, "ten"), "trunk: ten")
	gittest.Run(t, f.dir, "rebase", "-q", "main", "a")
	gittest.Run(t, f.dir, "rebase", "-q", "--onto", "a", f.fb.graph.Stacks[0].Branches[0].Head, "b")
	gittest.Run(t, f.dir, "rebase", "-q", "--onto", "b", f.fb.graph.Stacks[0].Branches[1].Head, "c")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	for i := range f.fb.graph.Stacks[0].Branches {
		br := &f.fb.graph.Stacks[0].Branches[i]
		br.Head = f.rev(t, br.Name)
	}

	f.stageLine(t, 10, "TEN")
	f.stageLine(t, 5, "FIVE")
	res, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := targetsOf(res.Plan); !slices.Equal(got, []string{"a", "none"}) && !slices.Equal(got, []string{"none", "a"}) {
		t.Fatalf("targets = %v", got)
	}
	for _, h := range res.Plan.Hunks {
		switch h.OldStart {
		case 5:
			if h.Target == nil || h.Target.Branch != "a" {
				t.Errorf("line 5 should target a: %+v", h)
			}
		case 10:
			if h.Target != nil {
				t.Errorf("line 10 was changed by trunk, so it should not be absorbed: %+v", h)
			}
		}
	}
}

func TestAbsorbIntoBottomBranchRewritesEverythingAbove(t *testing.T) {
	f := newAbsorbFixture(t)
	ctx := context.Background()
	oldA, oldB, oldC := f.rev(t, "a"), f.rev(t, "b"), f.rev(t, "c")
	authorsBefore := gittest.Run(t, f.dir, "log", "--format=%an <%ae> %at", "main..b")
	f.stageLine(t, 5, "FIVE")
	f.stageLine(t, 15, "FIFTEEN")
	f.stageLine(t, 1, "one")
	gittest.WriteFile(t, f.dir, "new.txt", "new\n")
	gittest.Run(t, f.dir, "add", "new.txt")
	gittest.WriteFile(t, f.dir, "a.txt", "unstaged edit\n")

	res, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.DryRun || len(res.Rewritten) != 2 || res.Rewritten[0].Name != "a" || res.Rewritten[0].From != oldA || res.Rewritten[1].Name != "b" || res.Rewritten[1].From != oldB {
		t.Errorf("res = %+v", res)
	}
	if !slices.Equal(res.Restacked, []string{"c"}) {
		t.Errorf("restacked = %v", res.Restacked)
	}
	// The hunks landed in the right commits and were carried above.
	if got := gittest.Run(t, f.dir, "show", "a~1:f.txt"); !strings.Contains(got, "\nFIVE\n") || strings.Contains(got, "FIFTEEN") {
		t.Errorf("a's first commit: %q", got)
	}
	if got := gittest.Run(t, f.dir, "show", "a:f.txt"); !strings.Contains(got, "\nFIVE\n") {
		t.Errorf("a's tip lost the change: %q", got)
	}
	if got := gittest.Run(t, f.dir, "show", "b:f.txt"); !strings.Contains(got, "\nFIVE\n") || !strings.Contains(got, "\nFIFTEEN\n") || strings.Contains(got, "\none\n") {
		t.Errorf("b: %q", got)
	}
	if got := gittest.Run(t, f.dir, "show", "c:f.txt"); !strings.Contains(got, "\nFIFTEEN\n") {
		t.Errorf("c was not restacked onto the new b: %q", got)
	}
	if n := gittest.Run(t, f.dir, "rev-list", "--count", "main..c"); n != "4" {
		t.Errorf("history grew or shrank: %s commits", n)
	}
	// Messages, authors and dates survive; the old tips are gone from the branches.
	if subj := gittest.Run(t, f.dir, "log", "--format=%s", "main..b"); subj != "feat: b\nfeat: a file\nfeat: a" {
		t.Errorf("subjects = %q", subj)
	}
	if an := gittest.Run(t, f.dir, "log", "--format=%an <%ae> %at", "main..b"); an != authorsBefore || strings.Count(an, "\n") != 2 {
		t.Errorf("authors = %q, were %q", an, authorsBefore)
	}
	if f.rev(t, "a") == oldA || f.rev(t, "b") == oldB || f.rev(t, "c") == oldC {
		t.Error("a branch did not move")
	}
	// What was not absorbed is exactly what is still staged; the
	// unstaged edit is untouched.
	if out := gittest.Run(t, f.dir, "diff", "--cached", "--name-only"); out != "f.txt\nnew.txt" {
		t.Errorf("still staged: %q", out)
	}
	staged := gittest.Run(t, f.dir, "diff", "--cached")
	if !strings.Contains(staged, "+one") || strings.Contains(staged, "FIVE") || strings.Contains(staged, "FIFTEEN") {
		t.Errorf("staged diff = %q", staged)
	}
	if out := gittest.Run(t, f.dir, "diff", "--name-only"); out != "a.txt" {
		t.Errorf("unstaged = %q", out)
	}
	// Metadata: heads and bases follow.
	br := f.fb.graph.Stacks[0].Branches
	if br[0].Head != f.rev(t, "a") || br[1].Head != f.rev(t, "b") || br[1].Base != f.rev(t, "a") || br[2].Head != f.rev(t, "c") || br[2].Base != f.rev(t, "b") {
		t.Errorf("metadata = %+v", br)
	}
	if br[0].Base != f.rev(t, "main") {
		t.Errorf("a's base should not change: %+v", br[0])
	}
}

func TestAbsorbIntoCurrentBranchOnly(t *testing.T) {
	f := newAbsorbFixture(t)
	oldA := f.rev(t, "a")
	f.stageLine(t, 15, "FIFTEEN")
	res, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{})
	if err != nil || len(res.Rewritten) != 1 || res.Rewritten[0].Name != "b" || f.rev(t, "a") != oldA {
		t.Errorf("res = %+v %v", res, err)
	}
	if _, err := gittest.RunErr(t, f.dir, "diff", "--cached", "--quiet"); err != nil {
		t.Error("index should be clean")
	}
}

func TestAbsorbTwoHunksIntoFileCreatedInStack(t *testing.T) {
	f := newAbsorbFixture(t)
	// a created g.txt (20 lines); edits to lines 3 and 10 both go to a.
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "g.txt", numbered(), "feat: g")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Run(t, f.dir, "rebase", "-q", "a")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	gittest.Run(t, f.dir, "rebase", "-q", "b")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	br := f.fb.graph.Stacks[0].Branches
	br[0].Head, br[1].Head, br[1].Base, br[2].Head, br[2].Base = f.rev(t, "a"), f.rev(t, "b"), f.rev(t, "a"), f.rev(t, "c"), f.rev(t, "b")

	want := withLine(withLine(numbered(), 10, "ten"), 3, "three\nthree and a half") // line 10 first: the insertion shifts what follows
	gittest.WriteFile(t, f.dir, "g.txt", want)
	gittest.Run(t, f.dir, "add", "g.txt")
	res, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{})
	if err != nil || res.Plan.Absorbed != 2 || res.Plan.Commits != 1 {
		t.Fatalf("res = %+v %v", res, err)
	}
	if got := gittest.Run(t, f.dir, "show", "a:g.txt") + "\n"; got != want {
		t.Errorf("a:g.txt = %q, want %q", got, want)
	}
	if _, err := gittest.RunErr(t, f.dir, "diff", "--cached", "--quiet"); err != nil {
		t.Error("index should be clean")
	}
}

func TestAbsorbFileWithoutTrailingNewline(t *testing.T) {
	f := newAbsorbFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "n.txt", "x\ny", "feat: no newline")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Run(t, f.dir, "rebase", "-q", "a")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	gittest.Run(t, f.dir, "rebase", "-q", "b")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	br := f.fb.graph.Stacks[0].Branches
	br[0].Head, br[1].Head, br[1].Base, br[2].Head, br[2].Base = f.rev(t, "a"), f.rev(t, "b"), f.rev(t, "a"), f.rev(t, "c"), f.rev(t, "b")

	gittest.WriteFile(t, f.dir, "n.txt", "x\nY")
	gittest.Run(t, f.dir, "add", "n.txt")
	res, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{})
	if err != nil || res.Plan.Absorbed != 1 {
		t.Fatalf("res = %+v %v", res, err)
	}
	if got := gittest.Run(t, f.dir, "show", "a:n.txt"); got != "x\nY" {
		t.Errorf("a:n.txt = %q", got)
	}
	if _, err := gittest.RunErr(t, f.dir, "diff", "--cached", "--quiet"); err != nil {
		t.Error("index should be clean")
	}
}

func TestAbsorbOtherWorktree(t *testing.T) {
	f := newAbsorbFixture(t)
	ctx := context.Background()
	wt := t.TempDir()
	gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "a")

	// Dirty there: refused before anything moves.
	gittest.WriteFile(t, wt, "a.txt", "dirty\n")
	oldA := f.rev(t, "a")
	f.stageLine(t, 5, "FIVE")
	_, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "checked out in") {
		t.Errorf("dirty worktree: %v", err)
	}
	if f.rev(t, "a") != oldA {
		t.Error("a moved despite the refusal")
	}

	// Clean there, apart from an untracked file: moved and the checkout
	// brought along.
	gittest.Run(t, wt, "checkout", "-q", "--", "a.txt")
	gittest.WriteFile(t, wt, "junk.txt", "x\n")
	res, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	if err != nil || !res.Applied {
		t.Fatalf("clean worktree: %+v %v", res, err)
	}
	if head := gittest.Run(t, wt, "rev-parse", "HEAD"); head != f.rev(t, "a") || head == oldA {
		t.Errorf("worktree HEAD = %s, a = %s", head, f.rev(t, "a"))
	}
	if got := gittest.Run(t, wt, "show", "HEAD:f.txt"); !strings.Contains(got, "\nFIVE\n") || gittest.Run(t, wt, "status", "--porcelain", "--untracked-files=no") != "" {
		t.Errorf("worktree not updated cleanly: %q", got)
	}
	if _, err := os.Stat(filepath.Join(wt, "junk.txt")); err != nil {
		t.Errorf("the untracked file should still be there: %v", err)
	}

	// A dirty checkout of a branch absorb doesn't rewrite is no reason to refuse.
	t.Run("unaffected branch", func(t *testing.T) {
		f := newAbsorbFixture(t)
		wt := t.TempDir()
		gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "c")
		gittest.WriteFile(t, wt, "c.txt", "dirty\n")
		oldC := f.rev(t, "c")
		f.stageLine(t, 15, "FIFTEEN")
		res, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
		if err != nil {
			t.Fatalf("absorb: %v", err)
		}
		if !res.Applied || len(res.Rewritten) != 1 || res.Rewritten[0].Name != "b" {
			t.Errorf("res = %+v", res)
		}
		if f.rev(t, "c") != oldC || slices.Contains(res.Restacked, "c") {
			t.Errorf("c should have been skipped: %+v", res)
		}
		if len(res.Notices) == 0 {
			t.Error("the skipped restack should have been noticed")
		}
		if got := gittest.Run(t, wt, "status", "--porcelain"); got != "M c.txt" {
			t.Errorf("c's checkout was touched: %q", got)
		}
	})
}

func TestAbsorbRefusesWhenABranchMovedMeanwhile(t *testing.T) {
	f := newAbsorbFixture(t)
	f.stageLine(t, 5, "FIVE")
	// A Preview hook is the one place something can happen between the
	// plan and the ref transaction.
	oldA := f.rev(t, "a")
	moved := ""
	_, err := f.app.Absorb(context.Background(), f.repo, app.AbsorbOptions{Preview: func(*app.AbsorbPlan) {
		gittest.Run(t, f.dir, "update-ref", "refs/heads/a", "main")
		moved = f.rev(t, "a")
	}})
	// (a now sits at main, which no candidate commit is; the planner ran
	// against the tips it read before the hook, and the transaction must
	// notice the difference.)
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "moved while") {
		t.Errorf("err = %v", err)
	}
	if f.rev(t, "a") != moved || moved == oldA {
		t.Error("the transaction should have left the moved ref alone")
	}
}

func TestAbsorbConflictAboveLeavesTheAbsorbInPlace(t *testing.T) {
	f := newAbsorbFixture(t)
	ctx := context.Background()
	// c also changes line 15, so restacking it onto the rewritten b conflicts.
	gittest.Run(t, f.dir, "switch", "-q", "c")
	gittest.Commit(t, f.dir, "f.txt", withLine(gittest.Run(t, f.dir, "show", "HEAD:f.txt")+"\n", 15, "c's fifteen"), "feat: c15")
	f.fb.graph.Stacks[0].Branches[2].Head = f.rev(t, "c")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	oldB, oldC := f.rev(t, "b"), f.rev(t, "c")

	f.stageLine(t, 15, "FIFTEEN")
	res, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
		t.Fatalf("want a conflict, got %v", err)
	}
	if !res.Applied || len(res.Rewritten) != 1 || res.Rewritten[0].Name != "b" || f.rev(t, "b") == oldB {
		t.Errorf("absorb should have happened: %+v", res)
	}
	if len(res.Restacked) != 0 {
		t.Errorf("nothing was restacked yet: %v", res.Restacked)
	}
	if got := gittest.Run(t, f.dir, "show", "b:f.txt"); !strings.Contains(got, "\nFIFTEEN\n") {
		t.Errorf("b = %q", got)
	}
	// The usual abort puts c back and the user back on b.
	ar, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true})
	if err != nil || gittest.Run(t, f.dir, "branch", "--show-current") != "b" {
		t.Errorf("abort: %+v %v", ar, err)
	}
	if f.rev(t, "b") == oldB {
		t.Error("abort must not undo the absorb itself")
	}
	if f.rev(t, "c") != oldC {
		t.Errorf("c should be back at its tip from before the absorb: %s, was %s", f.rev(t, "c"), oldC)
	}
}

func TestAbsorbRestackRefusalAboveSaysWhatIsLeft(t *testing.T) {
	f := newAbsorbFixture(t)
	ctx := context.Background()
	gittest.Run(t, f.dir, "switch", "-q", "c")
	gittest.Commit(t, f.dir, "f.txt", withLine(gittest.Run(t, f.dir, "show", "HEAD:f.txt")+"\n", 15, "c's fifteen"), "feat: c15")
	f.fb.graph.Stacks[0].Branches[2].Head = f.rev(t, "c")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	oldB, oldC := f.rev(t, "b"), f.rev(t, "c")

	gittest.WriteFile(t, f.dir, "a.txt", "unstaged\n")
	f.stageLine(t, 15, "FIFTEEN")
	f.stageLine(t, 1, "one")
	res, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Fatalf("want invalid_args, got %v", err)
	}
	if !res.Applied || len(res.Rewritten) != 1 || res.Rewritten[0].Name != "b" || f.rev(t, "b") == oldB {
		t.Errorf("absorb should have happened: %+v", res)
	}
	if !strings.Contains(err.Error(), "absorbed, but c above b was not restacked") {
		t.Errorf("message = %q", err)
	}
	se, _ := errors.AsType[*stack.Error](err)
	var restackStep, tryAgain bool
	for _, step := range se.NextSteps {
		restackStep = restackStep || strings.Contains(step, "git stack restack")
		tryAgain = tryAgain || strings.Contains(step, "try again")
	}
	if !restackStep || tryAgain {
		t.Errorf("steps = %v", se.NextSteps)
	}
	if _, err := os.Stat(filepath.Join(f.repo.GitDir, "git-stack", "restack.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no restack state should exist: %v", err)
	}
	if f.rev(t, "c") != oldC {
		t.Error("c should be untouched")
	}
}
