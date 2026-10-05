package app_test

import (
	"context"
	"encoding/json/v2"
	"errors"
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

// restackFixture: main (shared.txt) -> a (a.txt) -> b (b.txt) -> c (c.txt),
// all tracked with bases recorded, HEAD on c.
type restackFixture struct {
	app  *app.App
	fb   *fakeBackend
	repo git.Repo
	dir  string
}

func newRestackFixture(t *testing.T) *restackFixture {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Commit(t, dir, "shared.txt", "base", "shared")
	for _, n := range []string{"a", "b", "c"} {
		gittest.Run(t, dir, "switch", "-q", "-c", n)
		gittest.Commit(t, dir, n+".txt", n, "feat: "+n)
	}
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	rev := func(r string) string { return gittest.Run(t, dir, "rev-parse", r) }
	graph := stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{
		{Name: "a", Head: rev("a"), Base: rev("main")},
		{Name: "b", Head: rev("b"), Base: rev("a")},
		{Name: "c", Head: rev("c"), Base: rev("b")},
	}}})
	fb := &fakeBackend{git: g, graph: graph}
	return &restackFixture{app: app.New(app.Deps{Git: g, Meta: fb, Tracker: fb}), fb: fb, repo: repo, dir: dir}
}

func (f *restackFixture) rev(t *testing.T, r string) string {
	t.Helper()
	return gittest.Run(t, f.dir, "rev-parse", r)
}

func (f *restackFixture) base(name string) string {
	for _, b := range f.fb.graph.Stacks[0].Branches {
		if b.Name == name {
			return b.Base
		}
	}
	return ""
}

// amend rewrites branch's tip with a new file so everything above needs a restack.
func (f *restackFixture) amend(t *testing.T, branch, file string) {
	t.Helper()
	cur := gittest.Run(t, f.dir, "branch", "--show-current")
	gittest.Run(t, f.dir, "switch", "-q", branch)
	gittest.WriteFile(t, f.dir, file, file)
	gittest.Run(t, f.dir, "add", file)
	gittest.Run(t, f.dir, "commit", "-q", "--amend", "--no-edit")
	gittest.Run(t, f.dir, "switch", "-q", cur)
}

func moved(res app.RestackResult) []string {
	out := make([]string, len(res.Moved))
	for i, m := range res.Moved {
		out[i] = m.Name
	}
	return out
}

func TestRestackMovesEverythingAboveAnAmend(t *testing.T) {
	f := newRestackFixture(t)
	f.amend(t, "a", "a2.txt")
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Branches, []string{"a", "b", "c"}) || !slices.Equal(moved(res), []string{"b", "c"}) || !slices.Equal(res.InPlace, []string{"a"}) {
		t.Errorf("res = %+v", res)
	}
	// gittest.Run fails the test when git exits non-zero, so these are the assertions.
	gittest.Run(t, f.dir, "merge-base", "--is-ancestor", "a", "b")
	gittest.Run(t, f.dir, "merge-base", "--is-ancestor", "b", "c")
	if f.base("b") != f.rev(t, "a") || f.base("c") != f.rev(t, "b") {
		t.Errorf("bases not recorded: b=%s c=%s", f.base("b"), f.base("c"))
	}
	if gittest.Run(t, f.dir, "branch", "--show-current") != "c" {
		t.Error("restack must leave HEAD where it was")
	}
	if out := gittest.Run(t, f.dir, "status", "--short"); out != "" {
		t.Errorf("working tree should be clean, got %q", out)
	}
	if !strings.Contains(gittest.Run(t, f.dir, "ls-tree", "--name-only", "c"), "a2.txt") {
		t.Error("c should contain the amended a")
	}
}

func TestRestackRebasesBottomBranchOntoTrunk(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Commit(t, f.dir, "m.txt", "m", "main moves")
	gittest.Run(t, f.dir, "switch", "-q", "a")
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	if err != nil || !slices.Equal(moved(res), []string{"a", "b", "c"}) {
		t.Fatalf("res = %+v %v", res, err)
	}
	gittest.Run(t, f.dir, "merge-base", "--is-ancestor", "main", "a")
	if f.base("a") != f.rev(t, "main") {
		t.Error("a's base should be the new trunk tip")
	}
}

func TestRestackScopes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		scope stack.Scope
		from  string
		want  []string
	}{
		{stack.ScopeAll, "b", []string{"a", "b", "c"}},
		{stack.ScopeUpstack, "b", []string{"b", "c"}},
		{stack.ScopeDownstack, "b", []string{"a", "b"}},
		{stack.ScopeOnly, "b", []string{"b"}},
	} {
		f := newRestackFixture(t)
		f.amend(t, "a", "a2.txt")
		gittest.Run(t, f.dir, "switch", "-q", tc.from)
		res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Scope: tc.scope})
		if err != nil || !slices.Equal(res.Branches, tc.want) {
			t.Errorf("%v: %+v %v", tc.scope, res, err)
		}
	}
	// --only on b moves b alone; c is left needing a restack.
	f := newRestackFixture(t)
	f.amend(t, "a", "a2.txt")
	cBefore := f.rev(t, "c")
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Scope: stack.ScopeOnly, Branch: "b"})
	if err != nil || !slices.Equal(moved(res), []string{"b"}) || f.rev(t, "c") != cBefore {
		t.Errorf("only: %+v %v", res, err)
	}
}

func TestRestackNothingToDo(t *testing.T) {
	f := newRestackFixture(t)
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	if err != nil || len(res.Moved) != 0 || !slices.Equal(res.InPlace, []string{"a", "b", "c"}) {
		t.Errorf("res = %+v %v", res, err)
	}
}

func TestRestackRefusesTrunkUntrackedAndDetached(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	gittest.Run(t, f.dir, "switch", "-q", "main")
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Errorf("trunk: %v", err)
	}
	gittest.Run(t, f.dir, "switch", "-q", "-c", "loose")
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Errorf("untracked: %v", err)
	}
	gittest.Run(t, f.dir, "switch", "-q", "--detach", "c")
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("detached: %v", err)
	}
	// --branch picks the stack without checking anything out.
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Branch: "b", Scope: stack.ScopeUpstack})
	if err != nil || !slices.Equal(res.Branches, []string{"b", "c"}) {
		t.Errorf("branch: %+v %v", res, err)
	}
}

func TestRestackRebaseActiveComesFirst(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	// A plain git rebase that stops: HEAD detached, rebase in progress.
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "shared.txt", "a version", "a edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Commit(t, f.dir, "shared.txt", "b version", "b edits shared")
	_, _ = gittest.RunErr(t, f.dir, "rebase", "a")
	_, err := f.app.Restack(ctx, f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindRebaseActive || !strings.Contains(strings.Join(se.NextSteps, "\n"), "git rebase --continue") {
		t.Errorf("restack during a foreign rebase = %v", err)
	}
	if _, err := f.app.Modify(ctx, f.repo, app.ModifyOptions{Message: []string{"x"}}); !errors.Is(err, &stack.Error{Kind: stack.KindRebaseActive}) {
		t.Errorf("modify during a rebase = %v", err)
	}
}

func TestRestackConflictIsReportedWithFiles(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "shared.txt", "a version", "a edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Commit(t, f.dir, "shared.txt", "b version", "b edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	bBefore := f.rev(t, "b")
	_, err := f.app.Restack(ctx, f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "b" || !slices.Equal(se.Files, []string{"shared.txt"}) {
		t.Fatalf("err = %+v", err)
	}
	steps := strings.Join(se.NextSteps, "\n")
	if !strings.Contains(steps, "git add shared.txt") || !strings.Contains(steps, "git stack continue") || !strings.Contains(steps, "git stack abort") {
		t.Errorf("steps = %v", se.NextSteps)
	}
	if f.rev(t, "b") != bBefore {
		t.Error("the conflicting branch must not move")
	}
}

func TestRestackContinueAndAbortWithNothingPending(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "nothing to continue") {
		t.Errorf("continue: %v", err)
	}
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "nothing to abort") {
		t.Errorf("abort: %v", err)
	}
	// A rebase git-stack did not start is pointed at git's own commands.
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "shared.txt", "a version", "a edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Commit(t, f.dir, "shared.txt", "b version", "b edits shared")
	_, _ = gittest.RunErr(t, f.dir, "rebase", "a")
	_, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	var se *stack.Error
	if !errors.As(err, &se) || !strings.Contains(strings.Join(se.NextSteps, "\n"), "git rebase --continue") {
		t.Errorf("foreign rebase continue = %v", err)
	}
}

func TestRestackKeepsUnrelatedLocalChanges(t *testing.T) {
	f := newRestackFixture(t)
	f.amend(t, "a", "a2.txt")
	gittest.WriteFile(t, f.dir, "c.txt", "edited but not committed")
	gittest.WriteFile(t, f.dir, "notes.txt", "untracked")
	gittest.WriteFile(t, f.dir, "staged.txt", "staged new file")
	gittest.Run(t, f.dir, "add", "staged.txt")
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	if err != nil || !slices.Equal(moved(res), []string{"b", "c"}) {
		t.Fatalf("res = %+v %v", res, err)
	}
	status := gittest.Run(t, f.dir, "status", "--short")
	if !strings.Contains(status, "M c.txt") || !strings.Contains(status, "?? notes.txt") || !strings.Contains(status, "A  staged.txt") {
		t.Errorf("local changes lost: %q", status)
	}
	if gittest.Run(t, f.dir, "branch", "--show-current") != "c" {
		t.Error("still on c")
	}
}

func TestRestackRefusesOverlappingLocalChanges(t *testing.T) {
	f := newRestackFixture(t)
	// a's amend changes shared.txt; c has an uncommitted edit to the same file.
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "shared.txt", "a version", "a edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	gittest.WriteFile(t, f.dir, "shared.txt", "my edit")
	before := map[string]string{"b": f.rev(t, "b"), "c": f.rev(t, "c")}
	_, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindInvalidArgs || !strings.Contains(se.Msg, "shared.txt") {
		t.Fatalf("err = %v", err)
	}
	if f.rev(t, "b") != before["b"] || f.rev(t, "c") != before["c"] {
		t.Error("nothing may move when the checked out branch cannot")
	}
}

func TestRestackUpdatesCleanOtherWorktreeAndSkipsDirtyOne(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	f.amend(t, "a", "a2.txt")
	wt := f.dir + "-wt"
	gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "b")
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{})
	if err != nil || !slices.Equal(moved(res), []string{"b", "c"}) {
		t.Fatalf("clean worktree: %+v %v", res, err)
	}
	if gittest.Run(t, wt, "rev-parse", "HEAD") != f.rev(t, "b") {
		t.Error("the other worktree's checkout should follow b")
	}

	f.amend(t, "a", "a3.txt")
	gittest.WriteFile(t, wt, "b.txt", "dirty")
	bBefore := f.rev(t, "b")
	res, err = f.app.Restack(ctx, f.repo, app.RestackOptions{})
	// b is skipped, so c has nothing new to sit on and stays where it is.
	if err != nil || len(res.Moved) != 0 || f.rev(t, "b") != bBefore {
		t.Fatalf("dirty worktree: %+v %v", res, err)
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "uncommitted changes") {
		t.Errorf("notices = %v", res.Notices)
	}
	// c still sits on b's current tip.
	gittest.Run(t, f.dir, "merge-base", "--is-ancestor", "b", "c")
}

// conflictFixture: a and b both edit shared.txt, so b conflicts on a.
func conflictFixture(t *testing.T) *restackFixture {
	t.Helper()
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "shared.txt", "a version", "a edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Commit(t, f.dir, "shared.txt", "b version", "b edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	return f
}

func (f *restackFixture) rebaseActive(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(f.repo.GitDir, "rebase-merge"))
	return err == nil
}

func (f *restackFixture) stateExists() bool {
	_, err := os.Stat(filepath.Join(f.repo.GitDir, "git-stack", "restack.json"))
	return err == nil
}

func TestRestackConflictStartsARealRebase(t *testing.T) {
	f := conflictFixture(t)
	_, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "b" || !slices.Equal(se.Files, []string{"shared.txt"}) {
		t.Fatalf("err = %+v", err)
	}
	if !f.rebaseActive(t) || !f.stateExists() {
		t.Error("a git rebase of b should be in progress with our state saved")
	}
	if gittest.Run(t, f.dir, "status", "--short") != "UU shared.txt" {
		t.Errorf("status = %q", gittest.Run(t, f.dir, "status", "--short"))
	}
	// A second restack says a restack is waiting, with our commands.
	_, err = f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	if !errors.As(err, &se) || se.Kind != stack.KindRebaseActive || !strings.Contains(strings.Join(se.NextSteps, "\n"), "git stack continue") {
		t.Errorf("second restack = %v", err)
	}
}

func TestRestackConflictNeedsCleanTree(t *testing.T) {
	f := prefixFixture(t) // b would move cleanly before c's rebase starts
	gittest.WriteFile(t, f.dir, "c.txt", "uncommitted")
	before := f.rev(t, "b")
	_, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindInvalidArgs || !strings.Contains(se.Msg, "clean working tree") {
		t.Fatalf("err = %v", err)
	}
	if f.rebaseActive(t) || f.stateExists() || f.rev(t, "b") != before {
		t.Error("nothing may start or move when the tree is dirty")
	}
}

func TestRestackConflictInOtherWorktreeIsNotStartedHere(t *testing.T) {
	f := conflictFixture(t)
	wt := f.dir + "-wt"
	gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "b")
	_, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || !strings.Contains(strings.Join(se.NextSteps, "\n"), wt) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(strings.Join(se.NextSteps, "\n"), "git stack restack") {
		t.Errorf("steps = %q", se.NextSteps)
	}
	if f.rebaseActive(t) || f.stateExists() {
		t.Error("no rebase may start in this worktree")
	}
}

func TestModifyConflictInOtherWorktreeSaysRestack(t *testing.T) {
	f := conflictFixture(t)
	wt := f.dir + "-wt"
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "b")
	gittest.WriteFile(t, f.dir, "a2.txt", "a2")
	gittest.Run(t, f.dir, "add", "a2.txt")
	_, err := f.app.Modify(context.Background(), f.repo, app.ModifyOptions{})
	var se *stack.Error
	steps := ""
	if errors.As(err, &se) {
		steps = strings.Join(se.NextSteps, "\n")
	}
	if se == nil || se.Kind != stack.KindConflict || !strings.Contains(steps, filepath.Base(wt)+" && git stack restack") {
		t.Fatalf("err = %v, steps = %q", err, steps)
	}
	if f.rebaseActive(t) || f.stateExists() {
		t.Error("no rebase may start in this worktree")
	}
}

func TestRestackBottomBranchConflictWithTrunk(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Commit(t, f.dir, "shared.txt", "main version", "main edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "shared.txt", "a version", "a edits shared")
	_, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "a" || !slices.Equal(se.Files, []string{"shared.txt"}) {
		t.Fatalf("err = %+v", err)
	}
	if !f.rebaseActive(t) {
		t.Error("the bottom branch's rebase onto trunk should be in progress")
	}
}

// prefixFixture: a is amended, b edits shared.txt after c branched and c
// edits it too, so b moves cleanly and c then conflicts on the new b.
func prefixFixture(t *testing.T) *restackFixture {
	t.Helper()
	f := newRestackFixture(t)
	gittest.Commit(t, f.dir, "shared.txt", "c version", "c edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Commit(t, f.dir, "shared.txt", "b version", "b edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "c")
	f.amend(t, "a", "a2.txt")
	return f
}

func TestRestackConflictAfterCleanPrefix(t *testing.T) {
	f := prefixFixture(t)
	bBefore := f.rev(t, "b")
	_, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "c" || !slices.Equal(se.Files, []string{"shared.txt"}) {
		t.Fatalf("err = %+v", err)
	}
	bAfter := f.rev(t, "b")
	if bAfter == bBefore || f.rev(t, "b~2") != f.rev(t, "a") || f.base("b") != f.rev(t, "a") {
		t.Errorf("b should sit on the amended a with its base recorded: b~2 = %s, a = %s, base = %s", f.rev(t, "b~2"), f.rev(t, "a"), f.base("b"))
	}
	if !f.rebaseActive(t) {
		t.Error("c's rebase should be in progress")
	}
	data, err := os.ReadFile(filepath.Join(f.repo.GitDir, "git-stack", "restack.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		OriginalBranch string   `json:"original_branch"`
		Conflict       string   `json:"conflict"`
		Onto           string   `json:"onto"`
		Remaining      []string `json:"remaining"`
		Moved          []struct {
			Name string `json:"name"`
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"moved"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st.OriginalBranch != "c" || st.Conflict != "c" || st.Onto != "b" || len(st.Remaining) != 0 {
		t.Errorf("state = %s", data)
	}
	if len(st.Moved) != 1 || st.Moved[0].Name != "b" || st.Moved[0].From != bBefore || st.Moved[0].To != bAfter {
		t.Errorf("moved = %+v, want b %s -> %s", st.Moved, bBefore, bAfter)
	}
}

// TestRestackConflictGitResolvesCarriesOn: the planner's merge-tree reads
// attributes from this worktree (c, no .gitattributes) and sees a conflict,
// but git rebase checks out a, whose .gitattributes makes shared.txt a union
// merge, so the rebase of b goes through and the run carries on to c.
func TestRestackConflictGitResolvesCarriesOn(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.WriteFile(t, f.dir, ".gitattributes", "shared.txt merge=union\n")
	gittest.Commit(t, f.dir, "shared.txt", "a version\n", "a edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Commit(t, f.dir, "shared.txt", "b version\n", "b edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "c")

	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(moved(res), []string{"b", "c"}) {
		t.Errorf("moved = %v", moved(res))
	}
	if f.rebaseActive(t) || f.stateExists() {
		t.Error("the run should leave no rebase and no state behind")
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("HEAD on %q, want c", cur)
	}
	if got := gittest.Run(t, f.dir, "show", "b:shared.txt"); got != "a version\nb version" {
		t.Errorf("b:shared.txt = %q", got)
	}
	if f.base("b") != f.rev(t, "a") || f.base("c") != f.rev(t, "b") {
		t.Errorf("bases b = %s (a %s), c = %s (b %s)", f.base("b"), f.rev(t, "a"), f.base("c"), f.rev(t, "b"))
	}
}

func TestRestackContinueFinishesTheStack(t *testing.T) {
	f := conflictFixture(t)
	ctx := context.Background()
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
		t.Fatalf("want a conflict, got %v", err)
	}
	// Premature continue: still conflicted, same error, state kept.
	_, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || !slices.Equal(se.Files, []string{"shared.txt"}) || !f.stateExists() {
		t.Fatalf("premature continue = %+v", err)
	}
	gittest.WriteFile(t, f.dir, "shared.txt", "resolved")
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true, StageAll: true})
	if err != nil || !slices.Equal(moved(res), []string{"b", "c"}) {
		t.Fatalf("continue = %+v %v", res, err)
	}
	if f.rebaseActive(t) || f.stateExists() {
		t.Error("nothing should be left in progress")
	}
	if gittest.Run(t, f.dir, "branch", "--show-current") != "c" {
		t.Error("back on the original branch")
	}
	gittest.Run(t, f.dir, "merge-base", "--is-ancestor", "a", "b")
	gittest.Run(t, f.dir, "merge-base", "--is-ancestor", "b", "c")
	if f.base("b") != f.rev(t, "a") || f.base("c") != f.rev(t, "b") {
		t.Errorf("bases: b=%s c=%s", f.base("b"), f.base("c"))
	}
	if log := gittest.Run(t, f.dir, "log", "--format=%s", "a..c"); log != "feat: c\nb edits shared\nfeat: b" {
		t.Errorf("history = %q", log)
	}
}

func TestRestackContinueAfterHandFinishedRebase(t *testing.T) {
	f := conflictFixture(t)
	ctx := context.Background()
	_, _ = f.app.Restack(ctx, f.repo, app.RestackOptions{})
	gittest.WriteFile(t, f.dir, "shared.txt", "resolved")
	gittest.Run(t, f.dir, "add", "shared.txt")
	t.Setenv("GIT_EDITOR", "true")
	gittest.Run(t, f.dir, "rebase", "--continue")
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	if err != nil || !slices.Equal(moved(res), []string{"b", "c"}) || f.stateExists() {
		t.Fatalf("continue = %+v %v", res, err)
	}
	gittest.Run(t, f.dir, "merge-base", "--is-ancestor", "b", "c")
}

func TestRestackContinueAfterHandAbortedRebase(t *testing.T) {
	f := conflictFixture(t)
	ctx := context.Background()
	_, _ = f.app.Restack(ctx, f.repo, app.RestackOptions{})
	gittest.Run(t, f.dir, "rebase", "--abort")
	_, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindInvalidArgs || !strings.Contains(strings.Join(se.NextSteps, "\n"), "git stack abort") || !f.stateExists() {
		t.Fatalf("err = %+v, state kept = %v", err, f.stateExists())
	}
	if !slices.Equal(se.NextSteps, []string{"git stack abort to put the moved branches back, then git stack restack again"}) {
		t.Fatalf("recovery must clear the saved state before retrying: %v", se.NextSteps)
	}
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
		t.Fatalf("retry after abort = %v, want a new conflict", err)
	}
}

func TestRestackRefusesForeignRebase(t *testing.T) {
	for _, foreign := range []string{"branch", "base", "tip"} {
		t.Run(foreign, func(t *testing.T) {
			f := conflictFixture(t)
			ctx := context.Background()
			if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
				t.Fatalf("restack = %v, want a conflict", err)
			}
			gittest.Run(t, f.dir, "rebase", "--abort")
			branch, onto, from := "b", "a", f.base("b")
			switch foreign {
			case "branch":
				gittest.Run(t, f.dir, "switch", "-q", "-c", "foreign", "b")
				branch = "foreign"
			case "base":
				gittest.Run(t, f.dir, "switch", "-q", "-c", "foreign", "main")
				gittest.Commit(t, f.dir, "shared.txt", "foreign version", "foreign edits shared")
				onto = "foreign"
			case "tip":
				gittest.Commit(t, f.dir, "extra.txt", "extra", "extra commit on b")
			}
			if _, err := f.fb.git.Runner().Run(ctx, exec.Cmd{Name: "git", Dir: f.dir, Args: []string{"rebase", "--onto", onto, from, branch}}); err == nil {
				t.Fatal("want the foreign rebase to stop")
			}
			gittest.WriteFile(t, f.dir, "shared.txt", "resolved but unstaged")
			statePath := filepath.Join(f.repo.GitDir, "git-stack", "restack.json")
			state, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			head, tip := f.rev(t, "HEAD"), f.rev(t, branch)
			status := gittest.Run(t, f.dir, "status", "--short")
			graph, err := json.Marshal(f.fb.graph)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range []app.RestackOptions{{Continue: true, StageAll: true}, {Abort: true}} {
				_, err := f.app.Restack(ctx, f.repo, o)
				var se *stack.Error
				if !errors.As(err, &se) || se.Kind != stack.KindInvalidArgs || !strings.Contains(se.Msg, "did not start") ||
					!slices.Equal(se.NextSteps, []string{"finish it with git rebase --continue or git rebase --abort, then git stack abort"}) {
					t.Fatalf("foreign rebase = %+v", err)
				}
				afterState, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				afterGraph, err := json.Marshal(f.fb.graph)
				if err != nil {
					t.Fatal(err)
				}
				if !f.rebaseActive(t) || f.rev(t, "HEAD") != head || f.rev(t, branch) != tip ||
					gittest.Run(t, f.dir, "status", "--short") != status || string(afterState) != string(state) || string(afterGraph) != string(graph) {
					t.Fatal("the foreign rebase, index, refs and metadata must be left alone")
				}
			}
		})
	}
}

func TestRestackContinueThroughASecondConflict(t *testing.T) {
	f := conflictFixture(t)
	ctx := context.Background()
	// c also edits shared.txt, so it conflicts on the resolved b.
	gittest.Commit(t, f.dir, "shared.txt", "c version", "c edits shared")
	_, _ = f.app.Restack(ctx, f.repo, app.RestackOptions{})
	gittest.WriteFile(t, f.dir, "shared.txt", "resolved b")
	gittest.Run(t, f.dir, "add", "shared.txt")
	_, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "c" {
		t.Fatalf("second conflict = %+v", err)
	}
	gittest.WriteFile(t, f.dir, "shared.txt", "resolved c")
	gittest.Run(t, f.dir, "add", "shared.txt")
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	if err != nil || !slices.Equal(moved(res), []string{"b", "c"}) || f.stateExists() {
		t.Fatalf("final continue = %+v %v", res, err)
	}
	if gittest.Run(t, f.dir, "branch", "--show-current") != "c" {
		t.Error("back on c")
	}
}

func TestModifyConflictAndContinue(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Commit(t, f.dir, "shared.txt", "b version", "b edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.WriteFile(t, f.dir, "shared.txt", "a version")
	gittest.Run(t, f.dir, "add", "shared.txt")
	_, err := f.app.Modify(ctx, f.repo, app.ModifyOptions{Message: []string{"a edits shared"}})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || !strings.Contains(strings.Join(se.NextSteps, "\n"), "git stack continue") {
		t.Fatalf("modify = %+v", err)
	}
	gittest.WriteFile(t, f.dir, "shared.txt", "resolved")
	gittest.Run(t, f.dir, "add", "shared.txt")
	res, err := f.app.Modify(ctx, f.repo, app.ModifyOptions{Continue: true})
	if err != nil || !slices.Equal(res.Restacked, []string{"b", "c"}) || res.Branch != "a" {
		t.Fatalf("modify --continue = %+v %v", res, err)
	}
}

// abortFixture: a is amended so b moves cleanly, then c conflicts on the
// moved b. Returns the tips before the restack.
func abortFixture(t *testing.T) (*restackFixture, map[string]string) {
	t.Helper()
	return abortFixtureWith(t, nil)
}

// abortFixtureWith is abortFixture with prep run just before the restack.
func abortFixtureWith(t *testing.T, prep func(*restackFixture)) (*restackFixture, map[string]string) {
	t.Helper()
	f := prefixFixture(t)
	if prep != nil {
		prep(f)
	}
	before := map[string]string{"a": f.rev(t, "a"), "b": f.rev(t, "b"), "c": f.rev(t, "c")}
	bases := map[string]string{"b": f.base("b"), "c": f.base("c")}
	_, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "c" {
		t.Fatalf("want c to conflict, got %+v", err)
	}
	if f.rev(t, "b") == before["b"] {
		t.Fatal("b should have moved before the conflict")
	}
	before["base:b"], before["base:c"] = bases["b"], bases["c"]
	return f, before
}

func TestRestackAbortPutsEverythingBack(t *testing.T) {
	f, before := abortFixture(t)
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || !slices.Equal(res.Restored, []string{"b"}) {
		t.Fatalf("abort = %+v %v", res, err)
	}
	for _, n := range []string{"a", "b", "c"} {
		if f.rev(t, n) != before[n] {
			t.Errorf("%s = %s, want %s", n, f.rev(t, n), before[n])
		}
	}
	if f.base("b") != before["base:b"] || f.base("c") != before["base:c"] {
		t.Errorf("bases not restored: b=%s c=%s", f.base("b"), f.base("c"))
	}
	if f.rebaseActive(t) || f.stateExists() {
		t.Error("nothing should be left in progress")
	}
	if gittest.Run(t, f.dir, "branch", "--show-current") != "c" || gittest.Run(t, f.dir, "status", "--short") != "" {
		t.Error("back on c with a clean tree")
	}
	// And now there is nothing to abort.
	if _, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("second abort = %v", err)
	}
}

func TestRestackAbortLeavesBranchMovedSince(t *testing.T) {
	f, before := abortFixture(t)
	// Someone moves b by hand while the conflict is open.
	gittest.Run(t, f.dir, "branch", "-f", "b", "a")
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || len(res.Restored) != 0 || !strings.Contains(strings.Join(res.Notices, "\n"), "b moved") {
		t.Fatalf("abort = %+v %v", res, err)
	}
	if f.rev(t, "b") != f.rev(t, "a") {
		t.Error("a branch moved by hand must be left where it is")
	}
	if f.rev(t, "c") != before["c"] || f.rebaseActive(t) || f.stateExists() {
		t.Error("c must still be put back by git and the state cleared")
	}
}

func TestRestackAbortAfterHandAbortedRebase(t *testing.T) {
	f, before := abortFixture(t)
	gittest.Run(t, f.dir, "rebase", "--abort")
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || !slices.Equal(res.Restored, []string{"b"}) || f.rev(t, "b") != before["b"] {
		t.Fatalf("abort = %+v %v", res, err)
	}
}

func TestRestackAbortAfterHandFinishedRebase(t *testing.T) {
	f, before := abortFixture(t)
	gittest.WriteFile(t, f.dir, "shared.txt", "resolved")
	gittest.Run(t, f.dir, "add", "shared.txt")
	t.Setenv("GIT_EDITOR", "true")
	gittest.Run(t, f.dir, "rebase", "--continue")
	if f.rev(t, "c") == before["c"] {
		t.Fatal("the hand finished rebase should have moved c")
	}
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || !slices.Equal(res.Restored, []string{"b", "c"}) {
		t.Fatalf("abort = %+v %v", res, err)
	}
	for _, b := range f.fb.graph.Stacks[0].Branches {
		if f.rev(t, b.Name) != before[b.Name] || (b.Name != "a" && b.Head != before[b.Name]) {
			t.Errorf("%s was not put back: head %s, metadata %s", b.Name, f.rev(t, b.Name), b.Head)
		}
	}
	if f.base("b") != before["base:b"] || f.base("c") != before["base:c"] || f.rebaseActive(t) || f.stateExists() {
		t.Fatal("abort must restore the bases and clear the saved state")
	}
	if gittest.Run(t, f.dir, "branch", "--show-current") != "c" || gittest.Run(t, f.dir, "status", "--short") != "" {
		t.Fatal("abort should finish on the original branch with a clean tree")
	}
}

type failNextMetadataUpdate struct {
	stack.Metadata
	err error
}

func (m *failNextMetadataUpdate) Update(ctx context.Context, repo git.Repo, fn func(*stack.Graph) error) error {
	if m.err != nil {
		err := m.err
		m.err = nil
		return err
	}
	return m.Metadata.Update(ctx, repo, fn)
}

func TestRestackAbortRetriesAfterHandFinishedRebase(t *testing.T) {
	f, before := abortFixture(t)
	gittest.WriteFile(t, f.dir, "shared.txt", "resolved")
	gittest.Run(t, f.dir, "add", "shared.txt")
	t.Setenv("GIT_EDITOR", "true")
	gittest.Run(t, f.dir, "rebase", "--continue")
	failed := errors.New("metadata unavailable")
	meta := &failNextMetadataUpdate{Metadata: f.fb, err: failed}
	f.app = app.New(app.Deps{Git: f.fb.git, Meta: meta, Tracker: f.fb})
	ctx := context.Background()
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true}); !errors.Is(err, failed) || !f.stateExists() {
		t.Fatalf("abort = %v, want failed metadata update with state kept", err)
	}
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true})
	if err != nil || !slices.Equal(res.Restored, []string{"b", "c"}) || len(res.Notices) != 0 {
		t.Fatalf("retried abort = %+v %v", res, err)
	}
	for _, n := range []string{"b", "c"} {
		if f.rev(t, n) != before[n] || f.base(n) != before["base:"+n] {
			t.Errorf("%s: head %s, base %s were not restored", n, f.rev(t, n), f.base(n))
		}
	}
	if f.stateExists() {
		t.Fatal("successful retry should clear the state")
	}
}

// startedOnAFixture: main edits shared.txt and so does b, so a moves
// cleanly onto the new main while checked out here and b then conflicts on
// the moved a.
func startedOnAFixture(t *testing.T) (*restackFixture, map[string]string) {
	t.Helper()
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Commit(t, f.dir, "shared.txt", "main version", "main edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Commit(t, f.dir, "shared.txt", "b version", "b edits shared")
	gittest.Run(t, f.dir, "switch", "-q", "a")
	before := map[string]string{"a": f.rev(t, "a"), "b": f.rev(t, "b"), "c": f.rev(t, "c"), "base:a": f.base("a")}
	_, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "b" {
		t.Fatalf("want b to conflict, got %+v", err)
	}
	if f.rev(t, "a") == before["a"] {
		t.Fatal("a should have moved before the conflict")
	}
	return f, before
}

func TestRestackAbortPutsBackTheBranchItStartedOn(t *testing.T) {
	f, before := startedOnAFixture(t)
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || !slices.Equal(res.Restored, []string{"a"}) {
		t.Fatalf("abort = %+v %v", res, err)
	}
	for _, n := range []string{"a", "b", "c"} {
		if f.rev(t, n) != before[n] {
			t.Errorf("%s = %s, want %s", n, f.rev(t, n), before[n])
		}
	}
	if f.base("a") != before["base:a"] {
		t.Errorf("base of a = %s, want %s", f.base("a"), before["base:a"])
	}
	if gittest.Run(t, f.dir, "branch", "--show-current") != "a" || gittest.Run(t, f.dir, "status", "--short") != "" {
		t.Error("back on a with a clean tree")
	}
}

// TestRestackAbortResetsTheCheckedOutBranch: with the moved branch checked
// out here, abort moves it back with reset --keep, keeping unrelated edits.
func TestRestackAbortResetsTheCheckedOutBranch(t *testing.T) {
	f, before := startedOnAFixture(t)
	gittest.Run(t, f.dir, "rebase", "--abort")
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.WriteFile(t, f.dir, "a.txt", "edited")
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || !slices.Equal(res.Restored, []string{"a"}) || f.rev(t, "a") != before["a"] {
		t.Fatalf("abort = %+v %v", res, err)
	}
	if gittest.Run(t, f.dir, "branch", "--show-current") != "a" || gittest.Run(t, f.dir, "status", "--short") != "M a.txt" {
		t.Errorf("want a checked out with its edit kept, status = %q", gittest.Run(t, f.dir, "status", "--short"))
	}
}

func TestRestackContinueReportsEveryMoveOfTheOperation(t *testing.T) {
	f := prefixFixture(t)
	ctx := context.Background()
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
		t.Fatalf("want a conflict, got %v", err)
	}
	gittest.WriteFile(t, f.dir, "shared.txt", "resolved")
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true, StageAll: true})
	if err != nil || !slices.Equal(moved(res), []string{"b", "c"}) {
		t.Fatalf("continue = %+v %v", res, err)
	}
}

func TestRestackAbortAfterBranchWasPutBackByHand(t *testing.T) {
	f, before := abortFixture(t)
	// b is already back (a retried abort, or by hand) while the state still
	// says it moved.
	gittest.Run(t, f.dir, "update-ref", "refs/heads/b", before["b"])
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || !slices.Equal(res.Restored, []string{"b"}) || len(res.Notices) != 0 {
		t.Fatalf("abort = %+v %v", res, err)
	}
	if f.rev(t, "b") != before["b"] || f.base("b") != before["base:b"] {
		t.Errorf("b = %s base %s, want %s base %s", f.rev(t, "b"), f.base("b"), before["b"], before["base:b"])
	}
}

func TestRestackAbortLeavesDeletedBranch(t *testing.T) {
	f, _ := abortFixture(t)
	gittest.Run(t, f.dir, "update-ref", "-d", "refs/heads/b")
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || len(res.Restored) != 0 || !strings.Contains(strings.Join(res.Notices, "\n"), "b no longer exists") {
		t.Fatalf("abort = %+v %v", res, err)
	}
}

func TestRestackAbortResetsCleanOtherWorktree(t *testing.T) {
	wt := ""
	f, before := abortFixtureWith(t, func(f *restackFixture) {
		wt = f.dir + "-wt"
		gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "b")
	})
	if gittest.Run(t, wt, "rev-parse", "HEAD") == before["b"] {
		t.Fatal("the restack should have moved b in its worktree")
	}
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || !slices.Equal(res.Restored, []string{"b"}) {
		t.Fatalf("abort = %+v %v", res, err)
	}
	if gittest.Run(t, wt, "rev-parse", "HEAD") != before["b"] || gittest.Run(t, wt, "status", "--short") != "" {
		t.Error("the other worktree should be back on b's old tip, clean")
	}
}

func TestRestackAbortSkipsDirtyOtherWorktree(t *testing.T) {
	wt := ""
	f, _ := abortFixtureWith(t, func(f *restackFixture) {
		wt = f.dir + "-wt"
		gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "b")
	})
	movedTo := f.rev(t, "b")
	gittest.WriteFile(t, wt, "b.txt", "uncommitted")
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{Abort: true})
	if err != nil || slices.Contains(res.Restored, "b") || !strings.Contains(strings.Join(res.Notices, "\n"), "uncommitted changes") {
		t.Fatalf("abort = %+v %v", res, err)
	}
	if f.rev(t, "b") != movedTo || f.stateExists() {
		t.Error("b stays where the restack put it and the state is cleared")
	}
}
