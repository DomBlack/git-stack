package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/ai"
	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// submitFake simulates gh stack submit: every branch without a PR gets one
// in the forge, drafts unless Publish.
type submitFake struct {
	forge    *recordingForge
	graph    *stack.Graph
	opts     []stack.SubmitOptions
	output   string // Submit's backend output
	streamed bool   // reported as already relayed live
	// baseOnTrunk makes new PRs target trunk instead of their parent, the
	// way gh stack does when the branches below are queued for merge.
	baseOnTrunk bool
}

func (s *submitFake) Submit(_ context.Context, _ git.Repo, o stack.SubmitOptions) (stack.SubmitResult, error) {
	s.opts = append(s.opts, o)
	for _, st := range s.graph.Stacks {
		parent := st.Trunk
		for _, b := range st.Branches {
			if _, ok := app.PRsFor(s.forge.prs)[b.Name]; !ok {
				state := forge.StateDraft
				if o.Publish {
					state = forge.StateOpen
				}
				base := parent
				if s.baseOnTrunk {
					base = st.Trunk
				}
				s.forge.prs = append(s.forge.prs, forge.PullRequest{Number: 100 + len(s.forge.prs), Head: b.Name, Base: base, State: state, Title: "auto: " + b.Name, URL: "u/" + b.Name})
			}
			parent = b.Name
		}
	}
	out := s.output
	if out == "" {
		out = "✓ pushed"
	}
	return stack.SubmitResult{Output: out, Streamed: s.streamed}, nil
}

type recordingForge struct {
	prs     []forge.PullRequest
	updates map[int]forge.UpdatePR
	lists   int
}

func (f *recordingForge) ListPRs(context.Context, git.Repo) ([]forge.PullRequest, error) {
	f.lists++
	return slices.Clone(f.prs), nil
}
func (f *recordingForge) CreatePR(context.Context, git.Repo, forge.CreatePR) (forge.PullRequest, error) {
	return forge.PullRequest{}, nil
}
func (f *recordingForge) UpdatePR(_ context.Context, _ git.Repo, n int, in forge.UpdatePR) error {
	if f.updates == nil {
		f.updates = map[int]forge.UpdatePR{}
	}
	// Merge, so a base fix after a title/body update keeps both visible.
	cur := f.updates[n]
	if in.Title != nil {
		cur.Title = in.Title
	}
	if in.Body != nil {
		cur.Body = in.Body
	}
	if in.Base != nil {
		cur.Base = in.Base
	}
	if in.Ready != nil {
		cur.Ready = in.Ready
	}
	f.updates[n] = cur
	return nil
}

type prAI struct{ inputs []ai.PRInput }

func (p *prAI) DraftCommit(context.Context, ai.CommitInput) (ai.Commit, error) {
	return ai.Commit{}, nil
}
func (p *prAI) DraftPR(_ context.Context, in ai.PRInput) (ai.PullRequest, error) {
	p.inputs = append(p.inputs, in)
	return ai.PullRequest{Title: "AI: " + in.Branch, Body: "why " + in.Branch}, nil
}

type selectPrompter struct{ choice int }

func (s selectPrompter) Confirm(string, bool) (bool, error)   { return true, nil }
func (s selectPrompter) Select(string, []string) (int, error) { return s.choice, nil }

// submitFixture: main -> a -> b with a already having an open PR; HEAD on b.
func submitFixture(t *testing.T) (app.Deps, *submitFake, *recordingForge, git.Repo, string) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	gittest.Commit(t, dir, "a.txt", "a", "feat: a\n\nbody a")
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "b.txt", "b", "feat: b")
	gittest.WriteFile(t, dir, ".github/pull_request_template.md", "## Why\n")
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	graph := stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}}})
	fg := &recordingForge{prs: []forge.PullRequest{{Number: 7, Head: "a", Base: "main", State: forge.StateOpen, URL: "u/a"}}}
	sf := &submitFake{forge: fg, graph: graph}
	cfg := config.Defaults()
	cfg.CacheTTL = 0 // always refresh in tests
	deps := app.Deps{Git: g, Meta: memMeta{graph}, Submit: sf, Forge: fg, Cache: cache.New(repo), Config: cfg}
	return deps, sf, fg, repo, dir
}

func TestSubmitDryRunAndDefaults(t *testing.T) {
	deps, sf, fg, repo, _ := submitFixture(t)
	a := app.New(deps)
	ctx := context.Background()

	res, err := a.Submit(ctx, repo, app.SubmitOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(sf.opts) != 0 || !res.Draft {
		t.Errorf("dry run must not submit; non-interactive default is draft: %+v %v", res, sf.opts)
	}
	if len(res.PullRequests) != 2 || res.PullRequests[0].Number != 7 || res.PullRequests[0].WouldCreate || !res.PullRequests[1].WouldCreate {
		t.Errorf("plan = %+v", res.PullRequests)
	}
	if len(fg.updates) != 0 {
		t.Error("dry run must not touch the forge")
	}
}

func TestSubmitNoEditCreatesDraftsAndReportsNumbers(t *testing.T) {
	deps, sf, fg, repo, _ := submitFixture(t)
	a := app.New(deps)
	res, err := a.Submit(context.Background(), repo, app.SubmitOptions{NoEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.opts) != 1 || sf.opts[0].Interactive || sf.opts[0].Publish {
		t.Errorf("submit opts = %+v", sf.opts)
	}
	b := res.PullRequests[1]
	if !b.Created || b.Number == 0 || b.State != forge.StateDraft || b.TextUpdated {
		t.Errorf("b = %+v", b)
	}
	if res.PullRequests[0].Created || res.Output != "✓ pushed" {
		t.Errorf("a should be an update: %+v", res)
	}
	if len(fg.updates) != 0 {
		t.Error("no texts were supplied; nothing to update")
	}
}

func TestSubmitWithAITextsAndPublish(t *testing.T) {
	deps, sf, fg, repo, _ := submitFixture(t)
	pa := &prAI{}
	deps.AI = pa
	a := app.New(deps)
	res, err := a.Submit(context.Background(), repo, app.SubmitOptions{UseAI: true, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(pa.inputs) != 1 || pa.inputs[0].Branch != "b" || pa.inputs[0].Parent != "a" {
		t.Fatalf("AI inputs = %+v", pa.inputs)
	}
	in := pa.inputs[0]
	if !strings.Contains(in.Diff, "+b") || len(in.Commits) != 1 || in.Commits[0] != "feat: b" || in.Template != "## Why\n" || len(in.RecentSubjects) == 0 {
		t.Errorf("AI input = %+v", in)
	}
	if !sf.opts[0].Publish || sf.opts[0].Interactive {
		t.Errorf("publish should map to Publish=true, non-interactive: %+v", sf.opts)
	}
	b := res.PullRequests[1]
	if !b.Created || !b.TextUpdated || b.State != forge.StateOpen {
		t.Errorf("b = %+v", b)
	}
	up, ok := fg.updates[b.Number]
	if !ok || *up.Title != "AI: b" || *up.Body != "why b" {
		t.Errorf("update = %+v", up)
	}
	if _, ok := fg.updates[7]; ok {
		t.Error("existing PR text must not be rewritten")
	}
}

func TestSubmitSuppliedTextsAndDraftChoice(t *testing.T) {
	deps, sf, fg, repo, _ := submitFixture(t)
	deps.Prompter = selectPrompter{choice: 1}
	a := app.New(deps)
	res, err := a.Submit(context.Background(), repo, app.SubmitOptions{Texts: map[string]app.PRText{"b": {Title: "T", Body: "B"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Draft || !sf.opts[0].Publish {
		t.Errorf("prompt chose ready-for-review: %+v", res)
	}
	if sf.opts[0].Interactive {
		t.Error("supplied texts must skip the editor")
	}
	if up := fg.updates[res.PullRequests[1].Number]; up.Title == nil || *up.Title != "T" {
		t.Errorf("text not applied: %+v", fg.updates)
	}

	// Interactive editor path: prompter present, no texts, no --no-edit.
	deps2, sf2, _, repo2, _ := submitFixture(t)
	deps2.Prompter = selectPrompter{choice: 0}
	if _, err := app.New(deps2).Submit(context.Background(), repo2, app.SubmitOptions{}); err != nil {
		t.Fatal(err)
	}
	if !sf2.opts[0].Interactive || sf2.opts[0].Publish {
		t.Errorf("editor path: %+v", sf2.opts)
	}

	// Config default skips the prompt.
	deps3, sf3, _, repo3, _ := submitFixture(t)
	deps3.Config.SubmitDefault = config.SubmitPublish
	deps3.Prompter = selectPrompter{choice: 0}
	if _, err := app.New(deps3).Submit(context.Background(), repo3, app.SubmitOptions{NoEdit: true}); err != nil {
		t.Fatal(err)
	}
	if !sf3.opts[0].Publish {
		t.Errorf("config default publish ignored: %+v", sf3.opts)
	}
}

func TestSubmitErrors(t *testing.T) {
	deps, _, _, repo, dir := submitFixture(t)
	a := app.New(deps)
	ctx := context.Background()
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{UpdateOnly: true}); !errors.Is(err, &stack.Error{Kind: stack.KindUnsupported}) {
		t.Errorf("update-only: %v", err)
	}
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{Draft: true, Publish: true}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("draft+publish: %v", err)
	}
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{UseAI: true, NoEdit: true}); !errors.Is(err, &stack.Error{Kind: stack.KindUnsupported}) {
		t.Errorf("ai without drafter: %v", err)
	}
	gittest.Run(t, dir, "switch", "-q", "main")
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Errorf("trunk: %v", err)
	}
}
