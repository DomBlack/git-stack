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
	res, err := f.app.Restack(context.Background(), f.repo, app.RestackOptions{})
	if err != nil || !slices.Equal(moved(res), []string{"b", "c"}) {
		t.Fatalf("res = %+v %v", res, err)
	}
	status := gittest.Run(t, f.dir, "status", "--short")
	if !strings.Contains(status, "M c.txt") || !strings.Contains(status, "?? notes.txt") {
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
