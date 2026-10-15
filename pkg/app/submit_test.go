package app_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	links    []stack.LinkOptions
	output   string // Submit's backend output
	streamed bool   // reported as already relayed live
	// baseOnTrunk makes new PRs target trunk instead of their parent, the
	// way gh stack does when the branches below are queued for merge.
	baseOnTrunk bool
	// unstacked records every Unstack call; unstackErr is what it returns.
	unstacked  []int
	unstackErr error
	// unstackFn, when set, decides the result per number instead.
	unstackFn func(number int) error
}

func (s *submitFake) Unstack(_ context.Context, _ git.Repo, number int) error {
	s.unstacked = append(s.unstacked, number)
	if s.unstackFn != nil {
		return s.unstackFn(number)
	}
	return s.unstackErr
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

func (s *submitFake) Link(_ context.Context, _ git.Repo, o stack.LinkOptions) (stack.SubmitResult, error) {
	s.links = append(s.links, o)
	return stack.SubmitResult{Output: "✓ linked"}, nil
}

type recordingForge struct {
	prs     []forge.PullRequest
	created []forge.CreatePR
	updates map[int]forge.UpdatePR
	lists   int
	// merges records every MergeStack call as (number, method).
	merges []mergeCall
	// mergeErr, when set, is what MergeStack returns.
	mergeErr error
	// checks is what Checks reports, and checksErr what it fails with;
	// checked records the numbers of every Checks call.
	checks    map[int]forge.CheckSummary
	checksErr error
	checked   [][]int
}

func (f *recordingForge) Checks(_ context.Context, _ git.Repo, numbers []int) (map[int]forge.CheckSummary, error) {
	f.checked = append(f.checked, slices.Clone(numbers))
	return f.checks, f.checksErr
}

type mergeCall struct {
	number int
	method forge.MergeMethod
}

func (f *recordingForge) MergeStack(_ context.Context, _ git.Repo, n int, m forge.MergeMethod) (forge.MergeOutcome, error) {
	f.merges = append(f.merges, mergeCall{n, m})
	if f.mergeErr != nil {
		return forge.MergeOutcome{}, f.mergeErr
	}
	return forge.MergeOutcome{Status: forge.MergeMerged, SHA: "feedface"}, nil
}

func (f *recordingForge) ListPRs(context.Context, git.Repo) ([]forge.PullRequest, error) {
	f.lists++
	return slices.Clone(f.prs), nil
}
func (f *recordingForge) CreatePR(_ context.Context, _ git.Repo, in forge.CreatePR) (forge.PullRequest, error) {
	f.created = append(f.created, in)
	state := forge.StateOpen
	if in.Draft {
		state = forge.StateDraft
	}
	pr := forge.PullRequest{Number: 100 + len(f.prs), Head: in.Head, Base: in.Base, State: state, Title: in.Title, URL: "u/" + in.Head}
	f.prs = append(f.prs, pr)
	return pr, nil
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
	if !res.DryRun || len(sf.opts) != 0 || res.Draft {
		t.Errorf("dry run must not submit; the default is ready for review: %+v %v", res, sf.opts)
	}
	if len(res.PullRequests) != 2 || res.PullRequests[0].Number != 7 || res.PullRequests[0].WouldCreate || !res.PullRequests[1].WouldCreate {
		t.Errorf("plan = %+v", res.PullRequests)
	}
	if len(fg.updates) != 0 {
		t.Error("dry run must not touch the forge")
	}
}

func TestSubmitDissolvesPendingRemoteStacks(t *testing.T) {
	deps, sf, _, repo, _ := submitFixture(t)
	a := app.New(deps)
	ctx := context.Background()
	path := filepath.Join(repo.CommonDir, "git-stack", "remote-stacks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// x still starts a stack of its own; "gone" is in no stack any more.
	mm := deps.Meta.(memMeta)
	mm.g.Stacks = append(mm.g.Stacks, stack.Stack{Trunk: "main", Branches: []stack.Branch{{Name: "x"}}})
	pending := `{"dissolve":[{"id":"S1","number":7,"stack":"a"},{"id":"S2","number":8,"stack":"x"},{"id":"S3","number":9,"stack":"gone"}]}`
	if err := os.WriteFile(path, []byte(pending), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Submit(ctx, repo, app.SubmitOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if len(sf.unstacked) != 0 {
		t.Fatalf("a dry run must not dissolve anything: %v", sf.unstacked)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != pending {
		t.Fatalf("a dry run must leave the pending file alone: %q %v", data, err)
	}

	sf.unstackErr = errors.New("boom")
	res, err := a.Submit(ctx, repo, app.SubmitOptions{Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sf.unstacked, []int{7, 8, 9}) {
		t.Errorf("unstacked %v", sf.unstacked)
	}
	if n := strings.Join(res.Notices, "\n"); !strings.Contains(n, "could not dissolve stack #7") || !strings.Contains(n, "#8") {
		t.Errorf("notices = %v", res.Notices)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "S1") || !strings.Contains(string(data), "S2") {
		t.Errorf("failed dissolves should stay pending: %q %v", data, err)
	}

	sf.unstackErr = nil
	res, err = a.Submit(ctx, repo, app.SubmitOptions{Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Join(res.Notices, "\n")
	for _, want := range []string{
		"dissolved stack #7 on GitHub; it is created again in its new order below",
		"dissolved stack #8 on GitHub because its pull requests were regrouped by a reorder or move; submit from x to group them again",
		"dissolved stack #9 on GitHub; its pull requests now belong to the stack they were moved to",
	} {
		if !strings.Contains(n, want) {
			t.Errorf("missing %q in notices %v", want, res.Notices)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("nothing should be pending: %v", err)
	}
}

// One entry failing leaves only that entry pending; both outcomes are
// reported.
func TestSubmitKeepsOnlyTheFailedDissolvePending(t *testing.T) {
	deps, sf, _, repo, _ := submitFixture(t)
	a := app.New(deps)
	path := filepath.Join(repo.CommonDir, "git-stack", "remote-stacks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	pending := `{"dissolve":[{"id":"S1","number":7,"stack":"a"},{"id":"S2","number":8,"stack":"a"}]}`
	if err := os.WriteFile(path, []byte(pending), 0o644); err != nil {
		t.Fatal(err)
	}
	sf.unstackFn = func(n int) error {
		if n == 8 {
			return errors.New("boom")
		}
		return nil
	}
	res, err := a.Submit(context.Background(), repo, app.SubmitOptions{Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Join(res.Notices, "\n")
	if !strings.Contains(n, "dissolved stack #7 on GitHub") || !strings.Contains(n, "could not dissolve stack #8") {
		t.Errorf("notices = %v", res.Notices)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "S1") || !strings.Contains(string(data), "S2") {
		t.Errorf("only #8 should stay pending: %q %v", data, err)
	}
}

// A pending file that cannot be read is named, with how to clear it, and
// the submit carries on.
func TestSubmitNamesAnUnreadablePendingFile(t *testing.T) {
	deps, sf, _, repo, _ := submitFixture(t)
	a := app.New(deps)
	path := filepath.Join(repo.CommonDir, "git-stack", "remote-stacks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := a.Submit(context.Background(), repo, app.SubmitOptions{Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Join(res.Notices, "\n")
	if !strings.Contains(n, path) || !strings.Contains(n, "delete it to forget the stacks waiting to be dissolved") {
		t.Errorf("notices = %v", res.Notices)
	}
	if len(sf.opts) != 1 || len(sf.unstacked) != 0 {
		t.Errorf("submit should still run: opts=%v unstacked=%v", sf.opts, sf.unstacked)
	}
}

// A saved restack state means a reorder, move or restack is half done:
// submitting now would push half moved branches.
func TestSubmitRefusesWhileARestackIsSaved(t *testing.T) {
	deps, sf, _, repo, _ := submitFixture(t)
	a := app.New(deps)
	path := filepath.Join(repo.GitDir, "git-stack", "restack.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"command":"git stack reorder","conflict":"b"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := a.Submit(context.Background(), repo, app.SubmitOptions{Publish: true})
	if !errors.Is(err, &stack.Error{Kind: stack.KindRebaseActive}) {
		t.Errorf("submit = %v, want rebase_active", err)
	}
	if len(sf.opts) != 0 || len(sf.unstacked) != 0 {
		t.Errorf("nothing may be pushed or dissolved: %v %v", sf.opts, sf.unstacked)
	}
}

func TestSubmitNoEditCreatesReadyPRsAndReportsNumbers(t *testing.T) {
	deps, sf, fg, repo, _ := submitFixture(t)
	a := app.New(deps)
	res, err := a.Submit(context.Background(), repo, app.SubmitOptions{NoEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.opts) != 1 || sf.opts[0].Interactive || !sf.opts[0].Publish {
		t.Errorf("submit opts = %+v", sf.opts)
	}
	b := res.PullRequests[1]
	if !b.Created || b.Number == 0 || b.State != forge.StateOpen || b.TextUpdated {
		t.Errorf("b = %+v", b)
	}

	// stack.submit.default draft brings drafts back.
	deps, sf, _, repo, _ = submitFixture(t)
	deps.Config.SubmitDefault = config.SubmitDraft
	res, err = app.New(deps).Submit(context.Background(), repo, app.SubmitOptions{NoEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Draft || sf.opts[0].Publish || res.PullRequests[1].State != forge.StateDraft {
		t.Errorf("config draft ignored: %+v %+v", res, sf.opts)
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
	deps.Config.SubmitDefault = config.SubmitAsk
	deps.Prompter = selectPrompter{choice: 0} // ready for review
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

	// Interactive editor path: prompter present, no texts, no --no-edit;
	// the prompt picks draft this time.
	deps2, sf2, _, repo2, _ := submitFixture(t)
	deps2.Config.SubmitDefault = config.SubmitAsk
	deps2.Prompter = selectPrompter{choice: 1}
	if _, err := app.New(deps2).Submit(context.Background(), repo2, app.SubmitOptions{}); err != nil {
		t.Fatal(err)
	}
	if !sf2.opts[0].Interactive || sf2.opts[0].Publish {
		t.Errorf("editor path: %+v", sf2.opts)
	}

	// The default (publish) never asks, even with a prompter around.
	deps3, sf3, _, repo3, _ := submitFixture(t)
	deps3.Prompter = neverPrompter{t: t}
	if _, err := app.New(deps3).Submit(context.Background(), repo3, app.SubmitOptions{NoEdit: true}); err != nil {
		t.Fatal(err)
	}
	if !sf3.opts[0].Publish {
		t.Errorf("default publish ignored: %+v", sf3.opts)
	}
}

func TestSubmitErrors(t *testing.T) {
	deps, _, _, repo, dir := submitFixture(t)
	a := app.New(deps)
	ctx := context.Background()
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

func TestSubmitRefusesToOverwriteSomeoneElsesCommits(t *testing.T) {
	deps, sf, _, repo, dir := submitFixture(t)
	gittest.InitRemote(t, dir)
	a := app.New(deps)
	ctx := context.Background()

	// Our own rewrite (an amend, a restack) leaves the remote on a commit the
	// branch used to point at, so that's fine to force push over.
	gittest.Run(t, dir, "switch", "-q", "b")
	gittest.WriteFile(t, dir, "b.txt", "b, amended")
	gittest.Run(t, dir, "commit", "-q", "-a", "--amend", "-m", "feat: b")
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{NoEdit: true}); err != nil {
		t.Fatalf("own rewrite refused: %v", err)
	}

	// Someone else pushes to a.
	gittest.Run(t, dir, "switch", "-q", "--detach", "origin/a")
	gittest.Commit(t, dir, "theirs.txt", "theirs", "their fix")
	gittest.Run(t, dir, "push", "-q", "origin", "HEAD:refs/heads/a")
	gittest.Run(t, dir, "switch", "-q", "b")

	sf.opts = nil
	_, err := a.Submit(ctx, repo, app.SubmitOptions{NoEdit: true})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "a (1 commit on origin)") {
		t.Fatalf("expected a refusal naming a, got %v", err)
	}
	if len(sf.opts) != 0 {
		t.Error("refused submit still pushed")
	}
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{NoEdit: true, Force: true}); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if len(sf.opts) != 1 {
		t.Errorf("--force should submit, got %d submits", len(sf.opts))
	}
}

func (f *recordingForge) PullRequestURL(_ context.Context, _ git.Repo, n int) (string, error) {
	return fmt.Sprintf("https://forge.test/o/r/pull/%d", n), nil
}

// cancelRefresh is a Progress hook that behaves like Ctrl-C on the PR
// refresh's spinner: it cancels only the step's own context.
func cancelRefresh(ctx context.Context, phase app.Phase, _ string, fn func(context.Context) error) error {
	if phase != app.PhaseRefresh {
		return fn(ctx)
	}
	child, cancel := context.WithCancel(ctx)
	cancel()
	if err := fn(child); err != nil {
		return err
	}
	return child.Err() // as Reporter.Step does when the spinner saw Ctrl-C
}

func TestCancelledPRRefreshStopsTheCommand(t *testing.T) {
	deps, sf, fg, repo, _ := submitFixture(t)
	deps.Progress = cancelRefresh
	a := app.New(deps)
	ctx := context.Background()

	if _, err := a.View(ctx, repo, app.ViewOptions{PRs: app.PRsFresh}); !errors.Is(err, context.Canceled) {
		t.Errorf("View after a cancelled refresh = %v, want context.Canceled", err)
	}
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{NoEdit: true}); !errors.Is(err, context.Canceled) {
		t.Errorf("Submit after a cancelled refresh = %v, want context.Canceled", err)
	}
	if len(sf.opts) != 0 || len(fg.updates) != 0 {
		t.Errorf("nothing may be submitted after Ctrl-C: submits=%d updates=%d", len(sf.opts), len(fg.updates))
	}
}

// partialFixture extends submitFixture with c on top of b, pushes all three
// to a bare origin, then amends b and c so both need a force push. HEAD is
// on b.
func partialFixture(t *testing.T) (app.Deps, *submitFake, *recordingForge, git.Repo, string) {
	t.Helper()
	deps, sf, fg, repo, dir := submitFixture(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "c")
	gittest.Commit(t, dir, "c.txt", "c", "feat: c")
	gittest.InitRemote(t, dir)
	gittest.Run(t, dir, "commit", "-q", "--amend", "-m", "feat: c again")
	gittest.Run(t, dir, "switch", "-q", "b")
	gittest.Run(t, dir, "commit", "-q", "--amend", "-m", "feat: b\n\nbody b")
	sf.graph.Stacks[0].Branches = append(sf.graph.Stacks[0].Branches, stack.Branch{Name: "c"})
	return deps, sf, fg, repo, dir
}

func remoteTip(t *testing.T, dir, branch string) string {
	t.Helper()
	out, _ := gittest.RunErr(t, dir, "ls-remote", "origin", "refs/heads/"+branch)
	sha, _, _ := strings.Cut(out, "\t")
	return sha
}

func TestSubmitDownLeavesTheOnesAboveAlone(t *testing.T) {
	deps, sf, fg, repo, dir := partialFixture(t)
	res, err := app.New(deps).Submit(context.Background(), repo, app.SubmitOptions{Down: true, NoEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.opts) != 0 {
		t.Errorf("gh stack submit pushes every branch, so it must not run: %+v", sf.opts)
	}
	if got, want := remoteTip(t, dir, "b"), gittest.Run(t, dir, "rev-parse", "b"); got != want {
		t.Errorf("b on origin = %s, want %s", got, want)
	}
	if remoteTip(t, dir, "c") == gittest.Run(t, dir, "rev-parse", "c") {
		t.Error("c is above b and must not be pushed")
	}
	want := []forge.CreatePR{{Head: "b", Base: "a", Title: "feat: b", Body: "## Why\n"}}
	if !slices.Equal(fg.created, want) {
		t.Errorf("created = %+v, want %+v", fg.created, want)
	}
	if len(sf.links) != 1 || sf.links[0].Stack != 0 || sf.links[0].Trunk != "main" || !slices.Equal(sf.links[0].PRs, []int{7, 101}) {
		t.Errorf("links = %+v", sf.links)
	}
	if len(res.PullRequests) != 2 || !res.PullRequests[1].Created || res.PullRequests[1].Number != 101 || res.PullRequests[0].Created {
		t.Errorf("result = %+v", res.PullRequests)
	}
}

func TestSubmitPartUsesSuppliedTextsAndTheKnownStack(t *testing.T) {
	deps, sf, fg, repo, _ := partialFixture(t)
	sf.graph.Stacks[0].Number = 5
	res, err := app.New(deps).Submit(context.Background(), repo, app.SubmitOptions{
		Down:  true,
		Draft: true,
		Texts: map[string]app.PRText{"b": {Title: "Mine", Body: "why"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fg.created) != 1 || fg.created[0].Title != "Mine" || fg.created[0].Body != "why" || !fg.created[0].Draft {
		t.Errorf("created = %+v", fg.created)
	}
	if len(fg.updates) != 0 {
		t.Errorf("the text went in at creation; nothing to update: %+v", fg.updates)
	}
	if !res.PullRequests[1].TextUpdated {
		t.Errorf("b = %+v", res.PullRequests[1])
	}
	if len(sf.links) != 1 || sf.links[0].Stack != 5 {
		t.Errorf("links = %+v", sf.links)
	}
}

func TestSubmitNamedBranchWithAPRNeedsNoLink(t *testing.T) {
	deps, sf, fg, repo, _ := partialFixture(t)
	res, err := app.New(deps).Submit(context.Background(), repo, app.SubmitOptions{Branch: "a", NoEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.opts) != 0 || len(sf.links) != 0 || len(fg.created) != 0 {
		t.Errorf("a has its PR and no stack exists yet: opts %+v links %+v created %+v", sf.opts, sf.links, fg.created)
	}
	if len(res.PullRequests) != 1 || res.PullRequests[0].Number != 7 {
		t.Errorf("result = %+v", res.PullRequests)
	}
}

func TestSubmitDefaultsToTheWholeStack(t *testing.T) {
	deps, sf, fg, repo, _ := partialFixture(t)
	res, err := app.New(deps).Submit(context.Background(), repo, app.SubmitOptions{NoEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.opts) != 1 || len(sf.links) != 0 || len(fg.created) != 0 {
		t.Errorf("the whole stack goes through gh stack submit: opts %+v links %+v created %+v", sf.opts, sf.links, fg.created)
	}
	if len(res.PullRequests) != 3 {
		t.Errorf("result = %+v", res.PullRequests)
	}
}

func TestSubmitTargetErrors(t *testing.T) {
	deps, _, _, repo, _ := partialFixture(t)
	a := app.New(deps)
	ctx := context.Background()
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{Branch: "nope"}); kindOf(err) != stack.KindNotInStack {
		t.Errorf("unknown branch: %v", err)
	}
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{Branch: "main"}); kindOf(err) != stack.KindNotInStack {
		t.Errorf("trunk: %v", err)
	}
}

func kindOf(err error) stack.Kind {
	if se, ok := errors.AsType[*stack.Error](err); ok {
		return se.Kind
	}
	return stack.KindUnknown
}
