package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/stack/stacktest"
)

// getFixture is a clone that has only main, with origin holding a (on
// main) and b (on a) and the forge knowing #7 for a and #8 for b. Nothing
// is tracked.
type getFixture struct {
	app   *app.App
	be    *stacktest.Backend
	forge *recordingForge
	repo  git.Repo
	dir   string
}

func newGetFixture(t *testing.T) *getFixture {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	gittest.Commit(t, dir, "a.txt", "a", "feat: a")
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "b.txt", "b", "feat: b")
	gittest.InitRemote(t, dir)
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Run(t, dir, "branch", "-q", "-D", "a", "b")

	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	fg := &recordingForge{prs: []forge.PullRequest{
		{Number: 7, Head: "a", Base: "main", State: forge.StateOpen, URL: "u/7"},
		{Number: 8, Head: "b", Base: "a", State: forge.StateDraft, URL: "u/8"},
	}}
	be := stacktest.New(g, nil)
	cfg := config.Defaults()
	cfg.CacheTTL = 0
	a := app.New(app.Deps{Git: g, Meta: be, Tracker: be, Forge: fg, Cache: cache.New(repo), Config: cfg})
	return &getFixture{app: a, be: be, forge: fg, repo: repo, dir: dir}
}

func (f *getFixture) get(t *testing.T, target string) (app.GetResult, error) {
	t.Helper()
	return f.app.Get(context.Background(), f.repo, app.GetOptions{Target: target})
}

func (f *getFixture) current(t *testing.T) string {
	t.Helper()
	return gittest.Run(t, f.dir, "branch", "--show-current")
}

// pushRemote commits file on top of origin/<branch> and pushes it, leaving
// the local branch (if any) and the checkout where they were.
func (f *getFixture) pushRemote(t *testing.T, branch, file string) string {
	t.Helper()
	cur := f.current(t)
	gittest.Run(t, f.dir, "fetch", "-q", "origin")
	gittest.Run(t, f.dir, "switch", "-q", "--detach", "origin/"+branch)
	sha := gittest.Commit(t, f.dir, file, file, "remote: "+file)
	gittest.Run(t, f.dir, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	gittest.Run(t, f.dir, "switch", "-q", cur)
	return sha
}

func TestGetPullRequestInNoStack(t *testing.T) {
	f := newGetFixture(t)
	res, err := f.get(t, "#8")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != app.GetPRs || res.Branch != "b" || res.Trunk != "main" ||
		!slices.Equal(res.Branches, []string{"a", "b"}) || !slices.Equal(res.Created, []string{"a", "b"}) {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(f.be.Imports, []string{"8"}) {
		t.Errorf("gh stack was asked for %q", f.be.Imports)
	}
	// The base was found by a lookup for its branch, not in the list of
	// recent pull requests.
	if !slices.Equal(f.forge.heads, []string{"a"}) || f.forge.lists != 0 {
		t.Errorf("looked up heads %q, listed %d times", f.forge.heads, f.forge.lists)
	}
	s, i, ok := f.be.Graph().StackOf("b")
	if !ok || i != 1 || s.Trunk != "main" || s.Branches[0].Name != "a" {
		t.Errorf("stack = %+v", s)
	}
	if f.current(t) != "b" {
		t.Errorf("checked out %s", f.current(t))
	}
	for _, b := range []string{"a", "b"} {
		if got := gittest.Run(t, f.dir, "rev-parse", b); got != gittest.Run(t, f.dir, "rev-parse", "origin/"+b) {
			t.Errorf("%s is not at origin/%s", b, b)
		}
		if up := gittest.Run(t, f.dir, "rev-parse", "--abbrev-ref", b+"@{upstream}"); up != "origin/"+b {
			t.Errorf("%s tracks %q", b, up)
		}
	}

	// Asking again finds it here and only brings it up to date.
	tip := f.pushRemote(t, "a", "more.txt")
	gittest.Run(t, f.dir, "switch", "-q", "main")
	res, err = f.get(t, "https://forge.test/o/r/pull/8/files")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != app.GetLocal || res.Branch != "b" || !res.CheckedOut || len(res.Created) != 0 ||
		len(res.Updated) != 1 || res.Updated[0].Name != "a" || res.Updated[0].To != tip {
		t.Errorf("again: %+v", res)
	}
	// a moved on, so b is no longer on it.
	if !slices.Equal(res.NeedsRestack, []string{"b"}) {
		t.Errorf("needs restack after the fast forward: %q", res.NeedsRestack)
	}
	if len(f.be.Imports) != 1 {
		t.Errorf("a tracked stack went to gh stack: %q", f.be.Imports)
	}
	if f.current(t) != "b" {
		t.Errorf("checked out %s", f.current(t))
	}

	// A URL for another repository is refused before anything happens.
	_, err = f.get(t, "https://github.com/acme/app/pull/8")
	if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindInvalidArgs {
		t.Errorf("other repository: %v", err)
	}
}

func TestGetAgainWithNothingCached(t *testing.T) {
	f := newGetFixture(t)
	// #7 is on main, so nothing lists pull requests and the cache stays
	// empty; the second get has to find a by asking the forge about #7.
	if res, err := f.get(t, "7"); err != nil || res.Source != app.GetPRs {
		t.Fatalf("first: %+v %v", res, err)
	}
	gittest.Run(t, f.dir, "switch", "-q", "main")
	res, err := f.get(t, "7")
	if err != nil || res.Source != app.GetLocal || res.Branch != "a" || f.current(t) != "a" {
		t.Errorf("again: %+v %v on %s", res, err, f.current(t))
	}
	if len(f.be.Imports) != 1 {
		t.Errorf("a tracked stack went to gh stack: %q", f.be.Imports)
	}
}

func TestGetCheckoutFailureIsPartialAndUndoable(t *testing.T) {
	f := newGetFixture(t)
	// An untracked a.txt with other content is in the way of switching to a.
	gittest.WriteFile(t, f.dir, "a.txt", "mine")
	res, err := f.get(t, "7")
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindPartial || len(se.NextSteps) == 0 {
		t.Fatalf("err = %v", err)
	}
	if res.CheckedOut || res.Branch != "a" || !slices.Equal(res.Created, []string{"a"}) || f.current(t) != "main" {
		t.Errorf("result = %+v on %s", res, f.current(t))
	}
	if _, _, ok := f.be.Graph().StackOf("a"); !ok {
		t.Error("a is not tracked")
	}
	if h := app.UndoHistory(f.repo); len(h) == 0 || h[0] != "git stack get (keep)" {
		t.Errorf("undo history = %q", h)
	}
}

func TestGetByBranchName(t *testing.T) {
	f := newGetFixture(t)
	res, err := f.get(t, "a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != app.GetPRs || !slices.Equal(res.Branches, []string{"a"}) || f.current(t) != "a" {
		t.Errorf("result = %+v on %s", res, f.current(t))
	}
	if !slices.Equal(f.be.Imports, []string{"a"}) {
		t.Errorf("gh stack was asked for %q", f.be.Imports)
	}

	// b sits on a, which is now in a stack of its own here.
	_, err = f.get(t, "b")
	if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindUnsupported {
		t.Errorf("b on a tracked branch: %v", err)
	}
}

func TestGetFromGitHubStack(t *testing.T) {
	f := newGetFixture(t)
	// a exists locally but is behind origin/a, which gh stack leaves alone.
	gittest.Run(t, f.dir, "branch", "a", "origin/a")
	tip := f.pushRemote(t, "a", "more.txt")
	f.be.ImportFn = func(target string) error {
		ctx := context.Background()
		g := git.New(exec.New())
		if err := g.CreateTrackingBranch(ctx, f.repo, "b", "origin"); err != nil {
			return err
		}
		f.be.SetGraph(stack.NewGraph([]stack.Stack{{Trunk: "main", Number: 3, Branches: []stack.Branch{
			{Name: "a", PR: &stack.PRRef{Number: 7}}, {Name: "b", PR: &stack.PRRef{Number: 8}},
		}}}))
		return g.Switch(ctx, f.repo, "a")
	}
	res, err := f.get(t, "7")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != app.GetStack || res.Branch != "a" || !slices.Equal(res.Branches, []string{"a", "b"}) ||
		!slices.Equal(res.Created, []string{"b"}) || len(res.Updated) != 1 || res.Updated[0].To != tip {
		t.Errorf("result = %+v", res)
	}
	if got := gittest.Run(t, f.dir, "rev-parse", "a"); got != tip {
		t.Errorf("a = %s, want %s", got, tip)
	}
	if len(f.forge.prs) != 2 || f.forge.lists != 0 {
		t.Errorf("the forge was asked about a stack gh stack found (%d lists)", f.forge.lists)
	}
}

func TestGetPassesOtherBackendErrorsOn(t *testing.T) {
	f := newGetFixture(t)
	f.be.ImportFn = func(string) error { return stack.New(stack.KindAuthRequired, "gh is not logged in") }
	_, err := f.get(t, "8")
	if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindAuthRequired {
		t.Errorf("err = %v", err)
	}
	if _, _, ok := f.be.Graph().StackOf("b"); ok {
		t.Error("tracked b anyway")
	}

	// A repository without stacked PRs falls back like a lone PR.
	f.be.ImportFn = func(string) error { return stack.New(stack.KindStacksUnavailable, "no stacks") }
	if res, err := f.get(t, "8"); err != nil || res.Source != app.GetPRs {
		t.Errorf("stacks unavailable: %+v %v", res, err)
	}
}

func TestGetRefusals(t *testing.T) {
	f := newGetFixture(t)
	f.forge.prs = append(f.forge.prs,
		forge.PullRequest{Number: 9, Head: "x", Base: "main", State: forge.StateOpen, Fork: true},
		forge.PullRequest{Number: 10, Head: "y", Base: "main", State: forge.StateMerged},
		forge.PullRequest{Number: 11, Head: "gone", Base: "main", State: forge.StateOpen},
		// Based on #7, whose branch a is on origin, but c isn't.
		forge.PullRequest{Number: 14, Head: "c", Base: "a", State: forge.StateOpen},
	)
	for target, kind := range map[string]stack.Kind{
		"":      stack.KindInvalidArgs,
		"0":     stack.KindInvalidArgs,
		"9":     stack.KindUnsupported,
		"10":    stack.KindInvalidArgs,
		"12":    stack.KindInvalidArgs,
		"11":    stack.KindInvalidArgs,
		"14":    stack.KindInvalidArgs,
		"nope":  stack.KindInvalidArgs,
		"#9999": stack.KindInvalidArgs,
	} {
		_, err := f.get(t, target)
		if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != kind {
			t.Errorf("%q: %v, want %s", target, err, kind.Code())
		}
	}
	if len(f.be.Graph().Stacks) != 0 || f.current(t) != "main" {
		t.Errorf("a refusal changed something: %+v on %s", f.be.Graph().Stacks, f.current(t))
	}
	if got := gittest.Run(t, f.dir, "branch", "--format=%(refname:short)"); got != "main" {
		t.Errorf("a refusal left branches behind: %q", got)
	}
	// gh stack never saw the fork or the merged pull request.
	if slices.Contains(f.be.Imports, "9") || slices.Contains(f.be.Imports, "10") {
		t.Errorf("imports = %q", f.be.Imports)
	}
}

func TestGetFailedImportCheckoutIsPartial(t *testing.T) {
	f := newGetFixture(t)
	// gh stack creates the branches and saves the stack, then fails to
	// check a out because an untracked a.txt is in the way.
	gittest.WriteFile(t, f.dir, "a.txt", "mine")
	f.be.ImportFn = func(target string) error {
		ctx := context.Background()
		g := git.New(exec.New())
		for _, name := range []string{"a", "b"} {
			if err := g.CreateTrackingBranch(ctx, f.repo, name, "origin"); err != nil {
				return err
			}
		}
		f.be.SetGraph(stack.NewGraph([]stack.Stack{{Trunk: "main", Number: 3, Branches: []stack.Branch{
			{Name: "a", PR: &stack.PRRef{Number: 7}}, {Name: "b", PR: &stack.PRRef{Number: 8}},
		}}}))
		return stack.New(stack.KindUnknown, "gh stack checkout failed")
	}
	res, err := f.get(t, "7")
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindPartial {
		t.Fatalf("err = %v", err)
	}
	if res.Source != app.GetStack || res.CheckedOut || res.Branch != "a" ||
		!slices.Equal(res.Created, []string{"a", "b"}) || f.current(t) != "main" {
		t.Errorf("result = %+v on %s", res, f.current(t))
	}
	if h := app.UndoHistory(f.repo); len(h) == 0 || h[0] != "git stack get (keep)" {
		t.Errorf("undo history = %q", h)
	}
}

func TestGetUndo(t *testing.T) {
	f := newGetFixture(t)
	if _, err := f.get(t, "8"); err != nil {
		t.Fatal(err)
	}
	res, err := f.app.Undo(context.Background(), f.repo, app.UndoOptions{})
	if err != nil || res.Undone.Command != "git stack get" {
		t.Fatalf("undo: %+v %v", res, err)
	}
	if f.current(t) != "main" || len(f.be.Graph().Stacks) != 0 {
		t.Errorf("after undo: on %s with %+v", f.current(t), f.be.Graph().Stacks)
	}
	if out, _ := gittest.RunErr(t, f.dir, "branch", "--list", "a", "b"); out != "" {
		t.Errorf("branches left behind: %q", out)
	}
}
