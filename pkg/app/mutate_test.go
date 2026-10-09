package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/ai"
	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// fakeBackend implements Metadata and Tracker in memory, doing the git
// branch work for real so the use cases can be exercised end to end.
type fakeBackend struct {
	git   *git.Client
	graph *stack.Graph
	// updateErr makes Update fail, for the paths that undo a change.
	updateErr error
}

func (f *fakeBackend) Load(context.Context, git.Repo) (*stack.Graph, error) { return f.graph, nil }

func (f *fakeBackend) Update(_ context.Context, _ git.Repo, fn func(*stack.Graph) error) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if err := fn(f.graph); err != nil {
		return err
	}
	f.graph.Stacks = slices.DeleteFunc(f.graph.Stacks, func(s stack.Stack) bool { return len(s.Branches) == 0 })
	return nil
}

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
	deps := app.Deps{Git: g, Meta: fb, Tracker: fb, Config: cfg}
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
	deps.Progress = func(ctx context.Context, _ app.Phase, msg string, fn func(context.Context) error) error {
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
	// Two phases: the Claude draft, then creating the branch through the backend.
	if len(progressMsgs) != 2 || progressMsgs[1] != "Creating branch dom/add-widget-2" || len(fa.inputs) != 1 {
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
	// Stack: main -> a -> b, both with a commit of their own and b's base
	// recorded, HEAD on a.
	fb.graph.Stacks[0].Branches = append(fb.graph.Stacks[0].Branches, stack.Branch{Name: "b", Base: gittest.Run(t, dir, "rev-parse", "a")})
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "b.txt", "b", "feat: b")
	gittest.Run(t, dir, "switch", "-q", "a")

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
	// gittest.Run fails the test when git exits non-zero.
	gittest.Run(t, dir, "merge-base", "--is-ancestor", "a", "b")

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
	gittest.Run(t, dir, "merge-base", "--is-ancestor", "a", "b")

	// Top of stack: nothing to restack.
	gittest.Run(t, dir, "switch", "-q", "b")
	gittest.WriteFile(t, dir, "b.txt", "b2")
	res, err = a.Modify(ctx, repo, app.ModifyOptions{Staging: app.StageAll, Message: []string{"b1"}})
	if err != nil || res.Restacked != nil {
		t.Errorf("top: %+v %v", res, err)
	}
	// An empty branch c on top: modify has to create its first commit.
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

	// Nothing interrupted, so there is nothing to continue or abort.
	if _, err := a.Modify(ctx, repo, app.ModifyOptions{Continue: true}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("continue: %v", err)
	}
	if _, err := a.Modify(ctx, repo, app.ModifyOptions{Abort: true}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("abort: %v", err)
	}

	gittest.Run(t, dir, "switch", "-q", "main")
	if _, err := a.Modify(ctx, repo, app.ModifyOptions{Message: []string{"x"}}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("trunk: %v", err)
	}
}

func TestRestack(t *testing.T) {
	a, _, repo, _, _ := mutFixture(t)
	res, err := a.Restack(context.Background(), repo, app.RestackOptions{})
	if err != nil || !slices.Equal(res.InPlace, []string{"a"}) || len(res.Moved) != 0 {
		t.Errorf("restack: %+v %v", res, err)
	}
}

func TestCreateExplainsSigningFailure(t *testing.T) {
	cases := []struct {
		name   string
		config [][]string
		want   string
	}{
		{"ssh key file", [][]string{{"gpg.format", "ssh"}, {"gpg.ssh.program", "false"}, {"user.signingkey", "KEYDIR/id_ed25519.pub"}}, "ssh-add KEYDIR/id_ed25519"},
		{"ssh key file with a space", [][]string{{"gpg.format", "ssh"}, {"gpg.ssh.program", "false"}, {"user.signingkey", "KEYDIR/my keys/id_ed25519.pub"}}, "ssh-add 'KEYDIR/my keys/id_ed25519'"},
		{"ssh literal key", [][]string{{"gpg.format", "ssh"}, {"gpg.ssh.program", "false"}, {"user.signingkey", "key::ssh-ed25519 AAAAC3Nza"}}, "ssh-add"},
		{"ssh literal key without key::", [][]string{{"gpg.format", "ssh"}, {"gpg.ssh.program", "false"}, {"user.signingkey", "ssh-ed25519 AAAAC3Nza"}}, "with ssh-add"},
		{"ssh relative key file", [][]string{{"gpg.format", "ssh"}, {"gpg.ssh.program", "false"}, {"user.signingkey", "keys/id_ed25519.pub"}}, "ssh-add REPO/keys/id_ed25519"},
		{"gpg", [][]string{{"gpg.program", "false"}}, "gpg"},
		{"x509", [][]string{{"gpg.format", "x509"}, {"gpg.x509.program", "false"}}, "gpgsm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, repo, dir, _ := mutFixture(t)
			keyDir := t.TempDir()
			gittest.WriteFile(t, keyDir, "id_ed25519.pub", "ssh-ed25519 AAAAC3Nza test\n")
			gittest.WriteFile(t, keyDir, "my keys/id_ed25519.pub", "ssh-ed25519 AAAAC3Nza test\n")
			gittest.WriteFile(t, dir, "keys/id_ed25519.pub", "ssh-ed25519 AAAAC3Nza test\n")
			gittest.Run(t, dir, "config", "commit.gpgsign", "true")
			for _, kv := range tc.config {
				gittest.Run(t, dir, "config", kv[0], strings.ReplaceAll(kv[1], "KEYDIR", keyDir))
			}
			gittest.WriteFile(t, dir, "b.txt", "b")
			gittest.Run(t, dir, "add", "b.txt")

			_, err := a.Create(context.Background(), repo, app.CreateOptions{Name: "feat/b", Message: []string{"Add b"}})
			se, ok := errors.AsType[*stack.Error](err)
			if !ok || se.Kind != stack.KindSigningFailed {
				t.Fatalf("err = %v, want signing_failed", err)
			}
			want := strings.NewReplacer("KEYDIR", keyDir, "REPO", repo.TopLevel).Replace(tc.want)
			if !slices.ContainsFunc(se.NextSteps, func(s string) bool { return strings.Contains(s, want) }) {
				t.Errorf("next steps %q do not mention %q", se.NextSteps, want)
			}
			if !slices.ContainsFunc(se.NextSteps, func(s string) bool { return strings.Contains(s, "commit.gpgsign false") }) {
				t.Errorf("next steps %q do not offer turning signing off", se.NextSteps)
			}
			if se.Detail == "" {
				t.Error("Detail should carry git's stderr")
			}
		})
	}
}

// rejectCommits installs a pre-commit hook that fails every commit.
func rejectCommits(t *testing.T, dir string) {
	t.Helper()
	hooks := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\necho nope >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestCreateUndoesTheBranchWhenTheCommitFails(t *testing.T) {
	ctx := context.Background()

	t.Run("on top of a stack", func(t *testing.T) {
		a, fb, repo, dir, _ := mutFixture(t)
		rejectCommits(t, dir)
		gittest.WriteFile(t, dir, "b.txt", "b")
		gittest.Run(t, dir, "add", "b.txt")

		_, err := a.Create(ctx, repo, app.CreateOptions{Name: "feat/b", Message: []string{"Add b"}})
		se, ok := errors.AsType[*stack.Error](err)
		if !ok || !strings.Contains(se.Detail+se.Msg, "nope") {
			t.Fatalf("err = %v, want the hook's output", err)
		}
		if !slices.ContainsFunc(se.NextSteps, func(s string) bool { return strings.Contains(s, "git stack create") }) {
			t.Errorf("next steps %q should say to run create again", se.NextSteps)
		}
		if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "a" {
			t.Errorf("on %s, want back on a", cur)
		}
		if exists, _ := fb.git.BranchExists(ctx, repo, "feat/b"); exists {
			t.Error("feat/b should have been deleted")
		}
		if names := fb.graph.Stacks[0].Names(); !slices.Equal(names, []string{"a"}) {
			t.Errorf("stack = %v, want feat/b forgotten", names)
		}
		if out := gittest.Run(t, dir, "diff", "--cached", "--name-only"); out != "b.txt" {
			t.Errorf("staged = %q, want b.txt still staged", out)
		}
	})

	t.Run("new stack from trunk", func(t *testing.T) {
		a, fb, repo, dir, _ := mutFixture(t)
		gittest.Run(t, dir, "switch", "-q", "main")
		fb.graph = stack.NewGraph(nil)
		rejectCommits(t, dir)
		gittest.WriteFile(t, dir, "b.txt", "b")
		gittest.Run(t, dir, "add", "b.txt")

		if _, err := a.Create(ctx, repo, app.CreateOptions{Name: "feat/b", Message: []string{"Add b"}}); err == nil {
			t.Fatal("expected the commit to fail")
		}
		if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "main" {
			t.Errorf("on %s, want back on main", cur)
		}
		if len(fb.graph.Stacks) != 0 {
			t.Errorf("stacks = %+v, want the empty stack gone", fb.graph.Stacks)
		}
	})

	t.Run("undo fails", func(t *testing.T) {
		a, fb, repo, dir, _ := mutFixture(t)
		rejectCommits(t, dir)
		fb.updateErr = errors.New("metadata locked")
		gittest.WriteFile(t, dir, "b.txt", "b")
		gittest.Run(t, dir, "add", "b.txt")

		_, err := a.Create(ctx, repo, app.CreateOptions{Name: "feat/b", Message: []string{"Add b"}})
		se, ok := errors.AsType[*stack.Error](err)
		if !ok {
			t.Fatalf("err = %v, want *stack.Error", err)
		}
		if steps := strings.Join(se.NextSteps, "\n"); !strings.Contains(steps, "feat/b was left behind") || !strings.Contains(steps, "git stack modify") {
			t.Errorf("next steps %q should say feat/b is left behind and how to commit on it", se.NextSteps)
		}
		if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "feat/b" {
			t.Errorf("on %s, want still on feat/b", cur)
		}
	})
}

func TestCreateUndoesTheBranchWhenTheCommitIsCancelled(t *testing.T) {
	a, fb, repo, dir, _ := mutFixture(t)
	// A hook that outlives the deadline: the commit fails with the
	// context's error, and the undo must still run on a context of its own.
	hooks := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.WriteFile(t, dir, "b.txt", "b")
	gittest.Run(t, dir, "add", "b.txt")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := a.Create(ctx, repo, app.CreateOptions{Name: "feat/b", Message: []string{"Add b"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "a" {
		t.Errorf("on %s, want back on a", cur)
	}
	if exists, _ := fb.git.BranchExists(context.Background(), repo, "feat/b"); exists {
		t.Error("feat/b should have been deleted")
	}
	if names := fb.graph.Stacks[0].Names(); !slices.Equal(names, []string{"a"}) {
		t.Errorf("stack = %v, want feat/b forgotten", names)
	}
}
