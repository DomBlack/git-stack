package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/ai"
	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// fakeBackend implements Metadata, Tracker and Restacker in memory, doing
// the git branch work for real so the use cases can be exercised end to end.
type fakeBackend struct {
	git        *git.Client
	graph      *stack.Graph
	restacks   []stack.Scope
	restackErr error
	continued  int
	aborted    int
}

func (f *fakeBackend) Load(context.Context, git.Repo) (*stack.Graph, error) { return f.graph, nil }

func (f *fakeBackend) InitStack(ctx context.Context, repo git.Repo, trunk string, branches []string) error {
	s := stack.Stack{Trunk: trunk}
	prev := trunk
	for _, b := range branches {
		if err := f.git.CreateBranch(ctx, repo, b, prev); err != nil {
			return err
		}
		s.Branches = append(s.Branches, stack.Branch{Name: b})
		prev = b
	}
	f.graph = stack.NewGraph(append(f.graph.Stacks, s))
	return f.git.Switch(ctx, repo, prev)
}

func (f *fakeBackend) AddTop(ctx context.Context, repo git.Repo, name string) error {
	cur, _ := f.git.CurrentBranch(ctx, repo)
	s, i, ok := f.graph.StackOf(cur)
	if !ok || i != len(s.Branches)-1 {
		return stack.New(stack.KindNotAtTop, "not at top")
	}
	if err := f.git.CreateBranch(ctx, repo, name, cur); err != nil {
		return err
	}
	s.Branches = append(s.Branches, stack.Branch{Name: name})
	return f.git.Switch(ctx, repo, name)
}

func (f *fakeBackend) Restack(_ context.Context, _ git.Repo, scope stack.Scope) error {
	f.restacks = append(f.restacks, scope)
	return f.restackErr
}
func (f *fakeBackend) Continue(context.Context, git.Repo) error { f.continued++; return nil }
func (f *fakeBackend) Abort(context.Context, git.Repo) error    { f.aborted++; return nil }

type fakeAI struct {
	commit ai.Commit
	inputs []ai.CommitInput
}

func (f *fakeAI) DraftCommit(_ context.Context, in ai.CommitInput) (ai.Commit, error) {
	f.inputs = append(f.inputs, in)
	return f.commit, nil
}
func (f *fakeAI) DraftPR(context.Context, ai.PRInput) (ai.PullRequest, error) {
	return ai.PullRequest{}, nil
}

type fakePrompter struct {
	confirm bool
	asked   []string
}

func (p *fakePrompter) Confirm(q string, _ bool) (bool, error) {
	p.asked = append(p.asked, q)
	return p.confirm, nil
}
func (p *fakePrompter) Select(string, []string) (int, error) { return 0, nil }

// mutFixture: main -> a (tracked), HEAD on a.
func mutFixture(t *testing.T) (*app.App, *fakeBackend, git.Repo, string, app.Deps) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	gittest.Commit(t, dir, "a.txt", "a", "feat: a")
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakeBackend{git: g, graph: stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{{Name: "a"}}}})}
	cfg := config.Defaults()
	cfg.BranchPrefix = "dom/"
	deps := app.Deps{Git: g, Meta: fb, Tracker: fb, Restack: fb, Config: cfg}
	return app.New(deps), fb, repo, dir, deps
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Add the B file: it's Great!": "add-the-b-file-it-s-great",
		"  Fix/Thing  ":               "fix-thing",
		"":                            "",
		"---":                         "",
		strings.Repeat("word ", 30):   "word-word-word-word-word-word-word-word-word-word-word-word",
	}
	for in, want := range cases {
		if got := app.Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateOnTopWithMessage(t *testing.T) {
	a, fb, repo, dir, _ := mutFixture(t)
	ctx := context.Background()
	gittest.WriteFile(t, dir, "b.txt", "b")
	gittest.Run(t, dir, "add", "b.txt")

	res, err := a.Create(ctx, repo, app.CreateOptions{Message: []string{"Add B file", "body"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "dom/add-b-file" || res.Parent != "a" || res.NewStack || res.Commit == nil || res.Commit.Subject != "Add B file" {
		t.Errorf("res = %+v", res)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "dom/add-b-file" {
		t.Errorf("on %s", cur)
	}
	if fb.graph.Stacks[0].Top() != "dom/add-b-file" {
		t.Errorf("not tracked: %v", fb.graph.Stacks[0].Names())
	}
	if n := gittest.Run(t, dir, "rev-list", "--count", "a..HEAD"); n != "1" {
		t.Errorf("commit count = %s", n)
	}

	// Same message again → name gets a suffix; nothing staged → empty branch.
	res, err = a.Create(ctx, repo, app.CreateOptions{Message: []string{"Add B file"}})
	if err != nil || res.Branch != "dom/add-b-file-2" || res.Commit != nil {
		t.Errorf("suffix/empty: %+v %v", res, err)
	}
}

func TestCreateFromTrunkStartsNewStack(t *testing.T) {
	a, fb, repo, dir, _ := mutFixture(t)
	gittest.Run(t, dir, "switch", "-q", "main")
	// The very first stack: no trunk is declared yet, main is the default branch.
	fb.graph = stack.NewGraph(nil)
	res, err := a.Create(context.Background(), repo, app.CreateOptions{Name: "feat/x"})
	if err != nil || !res.NewStack || res.Branch != "feat/x" || res.Parent != "main" {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if len(fb.graph.Stacks) != 1 || fb.graph.Stacks[0].Bottom() != "feat/x" || fb.graph.Stacks[0].Trunk != "main" {
		t.Errorf("graph = %+v", fb.graph.Stacks)
	}
	// A second stack from a declared trunk.
	gittest.Run(t, dir, "switch", "-q", "main")
	res, err = a.Create(context.Background(), repo, app.CreateOptions{Name: "feat/y"})
	if err != nil || !res.NewStack || len(fb.graph.Stacks) != 2 {
		t.Errorf("second stack: %+v %v", res, err)
	}
}

func TestCreateErrors(t *testing.T) {
	a, fb, repo, dir, _ := mutFixture(t)
	ctx := context.Background()
	fb.graph.Stacks[0].Branches = append(fb.graph.Stacks[0].Branches, stack.Branch{Name: "b"})
	gittest.Run(t, dir, "branch", "b", "a")

	if _, err := a.Create(ctx, repo, app.CreateOptions{Name: "x"}); !errors.Is(err, &stack.Error{Kind: stack.KindNotAtTop}) {
		t.Errorf("mid-stack: %v", err)
	}
	if _, err := a.Create(ctx, repo, app.CreateOptions{Name: "x", Insert: true}); !errors.Is(err, &stack.Error{Kind: stack.KindUnsupported}) {
		t.Errorf("insert: %v", err)
	}
	gittest.Run(t, dir, "switch", "-q", "-c", "loose", "main")
	if _, err := a.Create(ctx, repo, app.CreateOptions{Name: "x"}); !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Errorf("untracked: %v", err)
	}
	gittest.Run(t, dir, "switch", "-q", "b")
	if _, err := a.Create(ctx, repo, app.CreateOptions{Name: "bad name"}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("invalid name: %v", err)
	}
	if _, err := a.Create(ctx, repo, app.CreateOptions{Name: "a"}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("existing name: %v", err)
	}
	if _, err := a.Create(ctx, repo, app.CreateOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("no name: %v", err)
	}
	if _, err := a.Create(ctx, repo, app.CreateOptions{UseAI: true}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("ai without staged: %v", err)
	}
	gittest.WriteFile(t, dir, "z", "z")
	gittest.Run(t, dir, "add", "z")
	if _, err := a.Create(ctx, repo, app.CreateOptions{UseAI: true}); !errors.Is(err, &stack.Error{Kind: stack.KindUnsupported}) {
		t.Errorf("ai without drafter: %v", err)
	}
	if _, err := a.Create(ctx, repo, app.CreateOptions{Staging: app.StagePatch}); !errors.Is(err, &stack.Error{Kind: stack.KindInteractionRequired}) {
		t.Errorf("patch without tty: %v", err)
	}
}

func TestCreateWithAIAndStagingPrompt(t *testing.T) {
	_, fb, repo, dir, deps := mutFixture(t)
	ctx := context.Background()
	fa := &fakeAI{commit: ai.Commit{BranchName: "Add Widget", Message: "feat: add widget\n\nWhy."}}
	fp := &fakePrompter{confirm: true}
	var progressMsgs []string
	deps.AI = fa
	deps.Prompter = fp
	deps.Progress = func(ctx context.Context, msg string, fn func(context.Context) error) error {
		progressMsgs = append(progressMsgs, msg)
		return fn(ctx)
	}
	a := app.New(deps)

	gittest.WriteFile(t, dir, "w.txt", "w")
	gittest.Run(t, dir, "branch", "dom/add-widget", "a") // force a collision
	res, err := a.Create(ctx, repo, app.CreateOptions{UseAI: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.asked) != 1 {
		t.Errorf("staging prompt not shown: %v", fp.asked)
	}
	if res.Branch != "dom/add-widget-2" || res.Commit == nil || res.Commit.Subject != "feat: add widget" {
		t.Errorf("res = %+v", res)
	}
	if len(progressMsgs) != 1 || len(fa.inputs) != 1 {
		t.Errorf("progress/AI calls: %v %d", progressMsgs, len(fa.inputs))
	}
	in := fa.inputs[0]
	if !strings.Contains(in.Diff, "+w") || in.BranchPrefix != "dom/" || !slices.Contains(in.TakenBranches, "a") || len(in.RecentSubjects) == 0 {
		t.Errorf("AI input = %+v", in)
	}

	// -m with --ai: message kept, only the name is drafted.
	gittest.WriteFile(t, dir, "w2.txt", "w2")
	gittest.Run(t, dir, "add", "w2.txt")
	res, err = a.Create(ctx, repo, app.CreateOptions{UseAI: true, Message: []string{"my message"}})
	if err != nil || res.Commit.Subject != "my message" || fa.inputs[1].Message != "my message" {
		t.Errorf("ai with -m: %+v %v", res, err)
	}
	_ = fb
}

func TestModify(t *testing.T) {
	a, fb, repo, dir, _ := mutFixture(t)
	ctx := context.Background()
	// Stack: main -> a -> b, HEAD on a.
	fb.graph.Stacks[0].Branches = append(fb.graph.Stacks[0].Branches, stack.Branch{Name: "b"})
	gittest.Run(t, dir, "branch", "b", "a")

	if _, err := a.Modify(ctx, repo, app.ModifyOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("nothing staged: %v", err)
	}

	before := gittest.Run(t, dir, "rev-parse", "HEAD")
	gittest.WriteFile(t, dir, "a.txt", "a2")
	res, err := a.Modify(ctx, repo, app.ModifyOptions{Staging: app.StageUpdate})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Amended || res.Commit.SHA == before || res.Commit.Subject != "feat: a" || !slices.Equal(res.Restacked, []string{"b"}) {
		t.Errorf("amend: %+v", res)
	}
	if n := gittest.Run(t, dir, "rev-list", "--count", "main..HEAD"); n != "1" {
		t.Errorf("amend should keep one commit, got %s", n)
	}
	if !slices.Equal(fb.restacks, []stack.Scope{stack.ScopeUpstack}) {
		t.Errorf("restacks = %v", fb.restacks)
	}

	res, err = a.Modify(ctx, repo, app.ModifyOptions{Message: []string{"feat: a renamed"}})
	if err != nil || res.Commit.Subject != "feat: a renamed" || !res.Amended {
		t.Errorf("amend message only: %+v %v", res, err)
	}

	gittest.WriteFile(t, dir, "a3.txt", "a3")
	res, err = a.Modify(ctx, repo, app.ModifyOptions{Staging: app.StageAll, NewCommit: true, Message: []string{"second"}})
	if err != nil || res.Amended || res.Commit.Subject != "second" {
		t.Errorf("-c: %+v %v", res, err)
	}
	if n := gittest.Run(t, dir, "rev-list", "--count", "main..HEAD"); n != "2" {
		t.Errorf("-c should add a commit, got %s", n)
	}

	// Top of stack: no restack call.
	fb.restacks = nil
	gittest.Run(t, dir, "switch", "-q", "b")
	gittest.WriteFile(t, dir, "b.txt", "b")
	res, err = a.Modify(ctx, repo, app.ModifyOptions{Staging: app.StageAll, Message: []string{"b1"}})
	if err != nil || len(fb.restacks) != 0 || res.Restacked != nil {
		t.Errorf("top: %+v %v restacks=%v", res, err, fb.restacks)
	}
	// b had zero own commits (it was created at a's old tip... it now has the
	// commit "b1"); create an empty branch c on top and modify it.
	gittest.Run(t, dir, "switch", "-q", "-c", "c")
	fb.graph.Stacks[0].Branches = append(fb.graph.Stacks[0].Branches, stack.Branch{Name: "c"})
	if _, err := a.Modify(ctx, repo, app.ModifyOptions{Message: []string{"x"}}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("empty branch without changes: %v", err)
	}
	gittest.WriteFile(t, dir, "c.txt", "c")
	res, err = a.Modify(ctx, repo, app.ModifyOptions{Staging: app.StageAll, Message: []string{"c1"}})
	if err != nil || res.Amended || !res.ForcedNewCommit {
		t.Errorf("zero-commit edge case: %+v %v", res, err)
	}
	if subj := gittest.Run(t, dir, "log", "-1", "--format=%s", "b"); subj != "b1" {
		t.Errorf("parent commit was rewritten: %s", subj)
	}

	// Conflict during restack gets actionable steps.
	gittest.Run(t, dir, "switch", "-q", "a")
	fb.restackErr = &stack.Error{Kind: stack.KindConflict, Msg: "conflict", Files: []string{"a.txt"}}
	gittest.WriteFile(t, dir, "a.txt", "a4")
	_, err = a.Modify(ctx, repo, app.ModifyOptions{Staging: app.StageAll})
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindConflict || !slices.ContainsFunc(se.NextSteps, func(s string) bool { return s == "git stack modify --continue" }) {
		t.Errorf("conflict: %v", err)
	}
	if _, err := a.Modify(ctx, repo, app.ModifyOptions{Continue: true}); err != nil || fb.continued != 1 {
		t.Errorf("continue: %v %d", err, fb.continued)
	}
	if _, err := a.Modify(ctx, repo, app.ModifyOptions{Abort: true}); err != nil || fb.aborted != 1 {
		t.Errorf("abort: %v %d", err, fb.aborted)
	}

	gittest.Run(t, dir, "switch", "-q", "main")
	if _, err := a.Modify(ctx, repo, app.ModifyOptions{Message: []string{"x"}}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("trunk: %v", err)
	}
}

func TestRestack(t *testing.T) {
	a, fb, repo, dir, _ := mutFixture(t)
	ctx := context.Background()
	fb.graph.Stacks[0].Branches = append(fb.graph.Stacks[0].Branches, stack.Branch{Name: "b"}, stack.Branch{Name: "c"})
	gittest.Run(t, dir, "branch", "b", "a")
	gittest.Run(t, dir, "branch", "c", "a")
	gittest.Run(t, dir, "switch", "-q", "b")

	res, err := a.Restack(ctx, repo, app.RestackOptions{Scope: stack.ScopeUpstack})
	if err != nil || !slices.Equal(res.Branches, []string{"b", "c"}) || res.BottomBehindTrunk {
		t.Errorf("upstack: %+v %v", res, err)
	}
	res, _ = a.Restack(ctx, repo, app.RestackOptions{Scope: stack.ScopeDownstack})
	if !slices.Equal(res.Branches, []string{"a", "b"}) {
		t.Errorf("downstack: %+v", res)
	}
	res, _ = a.Restack(ctx, repo, app.RestackOptions{})
	if !slices.Equal(res.Branches, []string{"a", "b", "c"}) || res.Trunk != "main" || res.Bottom != "a" {
		t.Errorf("all: %+v", res)
	}
	if !slices.Equal(fb.restacks, []stack.Scope{stack.ScopeUpstack, stack.ScopeDownstack, stack.ScopeAll}) {
		t.Errorf("restacks = %v", fb.restacks)
	}

	// Trunk moves on: the bottom branch is reported as behind.
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "m.txt", "m", "main moves")
	gittest.Run(t, dir, "switch", "-q", "b")
	res, err = a.Restack(ctx, repo, app.RestackOptions{})
	if err != nil || !res.BottomBehindTrunk {
		t.Errorf("behind trunk: %+v %v", res, err)
	}

	if _, err := a.Restack(ctx, repo, app.RestackOptions{Continue: true}); err != nil || fb.continued != 1 {
		t.Errorf("continue: %v", err)
	}
	gittest.Run(t, dir, "switch", "-q", "main")
	if _, err := a.Restack(ctx, repo, app.RestackOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Errorf("trunk: %v", err)
	}
	_ = os.Getenv
	_ = filepath.Join
}
