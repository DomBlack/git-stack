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
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// memMeta is an in-memory stack.Metadata.
type memMeta struct{ g *stack.Graph }

func (m memMeta) Load(context.Context, git.Repo) (*stack.Graph, error) { return m.g, nil }

// fixture builds main -> a -> b -> c plus an untracked branch "loose" where
// b has been amended so c needs a restack.
func fixture(t *testing.T) (*app.App, git.Repo, string) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	gittest.Commit(t, dir, "a.txt", "a", "a")
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "b.txt", "b", "b")
	gittest.Run(t, dir, "switch", "-q", "-c", "c")
	gittest.Commit(t, dir, "c.txt", "c", "c")
	gittest.Run(t, dir, "switch", "-q", "b")
	gittest.Run(t, dir, "commit", "-q", "--amend", "-m", "b amended")
	gittest.Run(t, dir, "branch", "loose", "main")

	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	graph := stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{
		{Name: "a"}, {Name: "b", PR: &stack.PRRef{Number: 5, URL: "u"}}, {Name: "c"},
	}}})
	return app.New(app.Deps{Git: g, Meta: memMeta{graph}}), repo, dir
}

func TestView(t *testing.T) {
	a, repo, dir := fixture(t)
	v, err := a.View(context.Background(), repo, app.ViewOptions{IncludeUntracked: true})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(v.Rows))
	for _, r := range v.Rows {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"main", "a", "b", "c", "loose"}) {
		t.Fatalf("rows = %v", names)
	}
	if v.Current != "b" {
		t.Errorf("Current = %q", v.Current)
	}
	rows := map[string]app.Row{}
	for _, r := range v.Rows {
		rows[r.Name] = r
	}
	if !rows["main"].IsTrunk || rows["main"].Depth != 0 || rows["a"].Depth != 1 || rows["c"].Depth != 3 {
		t.Errorf("depths: %+v", rows)
	}
	if rows["a"].Parent != "main" || rows["c"].Parent != "b" {
		t.Errorf("parents wrong")
	}
	if !rows["b"].IsCurrent || rows["a"].IsCurrent {
		t.Errorf("current marker wrong")
	}
	if !rows["c"].NeedsRestack || rows["a"].NeedsRestack || rows["b"].NeedsRestack {
		t.Errorf("needs-restack: a=%v b=%v c=%v", rows["a"].NeedsRestack, rows["b"].NeedsRestack, rows["c"].NeedsRestack)
	}
	if rows["b"].PR == nil || rows["b"].PR.Number != 5 || rows["b"].PR.State != "" {
		t.Errorf("PR = %+v", rows["b"].PR)
	}
	if rows["loose"].Tracked || rows["loose"].IsTrunk || rows["loose"].Depth != 0 {
		t.Errorf("loose = %+v", rows["loose"])
	}
	if rows["c"].Head == "" || rows["c"].LastCommit.IsZero() {
		t.Errorf("git details missing: %+v", rows["c"])
	}

	// Another worktree checking out "a" shows up on the row.
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", wt, "a")
	v, _ = a.View(context.Background(), repo, app.ViewOptions{})
	r, _ := v.Row("a")
	if r.Worktree == "" || !strings.HasSuffix(r.Worktree, "wt") {
		t.Errorf("worktree not detected: %+v", r)
	}
	if r, _ := v.Row("b"); r.Worktree != "" {
		t.Errorf("current branch must not report its own worktree: %+v", r)
	}

	v, _ = a.View(context.Background(), repo, app.ViewOptions{CurrentStackOnly: true, SkipRestackCheck: true})
	if len(v.Rows) != 4 {
		t.Errorf("current stack only: %d rows", len(v.Rows))
	}
	if r, _ := v.Row("c"); r.NeedsRestack {
		t.Error("SkipRestackCheck ignored")
	}
}

func TestNavigateAndCheckout(t *testing.T) {
	a, repo, dir := fixture(t)
	ctx := context.Background()

	res, err := a.Navigate(ctx, repo, stack.NavRequest{Dir: stack.Up})
	if err != nil || res.Target != "c" || res.Moved != 1 {
		t.Fatalf("up: %+v %v", res, err)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "c" {
		t.Errorf("checked out %q", cur)
	}
	res, err = a.Navigate(ctx, repo, stack.NavRequest{Dir: stack.Up})
	if err != nil || !res.Clamped || res.Message != stack.MsgAlreadyTop {
		t.Fatalf("up at top: %+v %v", res, err)
	}
	res, err = a.Navigate(ctx, repo, stack.NavRequest{Dir: stack.Bottom})
	if err != nil || res.Target != "a" {
		t.Fatalf("bottom: %+v %v", res, err)
	}
	res, err = a.Navigate(ctx, repo, stack.NavRequest{Dir: stack.Down})
	if err != nil || res.Target != "main" {
		t.Fatalf("down to trunk: %+v %v", res, err)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "main" {
		t.Errorf("checked out %q", cur)
	}
	res, err = a.Navigate(ctx, repo, stack.NavRequest{Dir: stack.Top})
	if err != nil || res.Target != "c" || res.Moved != 3 {
		t.Fatalf("top from trunk: %+v %v", res, err)
	}

	gittest.Run(t, dir, "switch", "-q", "loose")
	_, err = a.Navigate(ctx, repo, stack.NavRequest{Dir: stack.Up})
	if !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Errorf("untracked: %v", err)
	}

	if err := a.Checkout(ctx, repo, "nope"); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("missing branch: %v", err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", wt, "a")
	err = a.Checkout(ctx, repo, "a")
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || !strings.Contains(se.Msg, "another worktree") || len(se.NextSteps) != 1 {
		t.Errorf("worktree conflict: %v", err)
	}
	if err := a.Checkout(ctx, repo, "b"); err != nil {
		t.Errorf("plain checkout: %v", err)
	}

	gittest.Run(t, dir, "checkout", "-q", "--detach")
	_, err = a.Navigate(ctx, repo, stack.NavRequest{Dir: stack.Up})
	if !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Errorf("detached: %v", err)
	}
}

func TestTrunk(t *testing.T) {
	a, repo, dir := fixture(t)
	ctx := context.Background()
	if tr, err := a.Trunk(ctx, repo); err != nil || tr != "main" {
		t.Errorf("from stack: %q %v", tr, err)
	}
	gittest.Run(t, dir, "switch", "-q", "loose")
	if tr, err := a.Trunk(ctx, repo); err != nil || tr != "main" {
		t.Errorf("single trunk from untracked: %q %v", tr, err)
	}
	empty := app.New(app.Deps{Git: git.New(exec.New()), Meta: memMeta{stack.NewGraph(nil)}})
	if tr, err := empty.Trunk(ctx, repo); err != nil || tr != "main" {
		t.Errorf("no stacks falls back to the default branch: %q %v", tr, err)
	}
	v, err := empty.View(ctx, repo, app.ViewOptions{})
	if err != nil || len(v.Rows) != 1 || !v.Rows[0].IsTrunk || v.Rows[0].Name != "main" {
		t.Errorf("empty repo view should show the default branch as trunk: %+v %v", v.Rows, err)
	}
	gittest.Run(t, dir, "branch", "-m", "main", "trunk")
	if _, err := empty.Trunk(ctx, repo); !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Errorf("no stacks and no main: %v", err)
	}
	_ = os.Getenv
}
