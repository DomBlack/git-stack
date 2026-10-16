package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestBranchPRComesFromLocalStateFirst(t *testing.T) {
	_, repo, _ := fixture(t)
	ff := &fakeForge{}
	graph := stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{
		{Name: "a", PR: &stack.PRRef{Number: 3}}, {Name: "b", PR: &stack.PRRef{Number: 5, URL: "u5"}}, {Name: "c"},
	}}})
	c := cache.New(repo)
	if err := cache.Write(c, "prs", app.PRSnapshot{PRs: []forge.PullRequest{
		{Number: 9, Head: "c", State: forge.StateDraft, URL: "u9"},
	}}); err != nil {
		t.Fatal(err)
	}
	a := app.New(app.Deps{Git: gitClient(), Meta: memMeta{graph}, Forge: ff, Cache: c, Config: config.Defaults()})
	ctx := context.Background()

	for _, tc := range []struct {
		branch string
		want   forge.PullRequest
	}{
		{"", forge.PullRequest{Number: 5, Head: "b", URL: "u5"}},                             // the current branch, from gh stack's metadata
		{"c", forge.PullRequest{Number: 9, Head: "c", URL: "u9", State: forge.StateDraft}},   // the PR cache
		{"a", forge.PullRequest{Number: 3, Head: "a", URL: "https://forge.test/o/r/pull/3"}}, // a number with no URL
	} {
		got, err := a.BranchPR(ctx, repo, tc.branch)
		if err != nil {
			t.Fatalf("%q: %v", tc.branch, err)
		}
		if got.Number != tc.want.Number || got.Head != tc.want.Head || got.URL != tc.want.URL || got.State != tc.want.State {
			t.Errorf("%q: got %+v, want %+v", tc.branch, got, tc.want)
		}
	}
	if ff.calls != 0 {
		t.Errorf("a PR known locally must not ask the forge (%d calls)", ff.calls)
	}
}

func TestBranchPRAsksTheForgeWhenNothingLocalKnows(t *testing.T) {
	a, repo, _ := fixture(t)
	ff := &fakeForge{prs: []forge.PullRequest{{Number: 12, Head: "c", State: forge.StateOpen, URL: "u12"}}}
	var phases []app.Phase
	a = app.New(app.Deps{Git: gitClient(), Meta: metaOf(a, t), Forge: ff, Cache: cache.New(repo), Config: config.Defaults(),
		Progress: func(ctx context.Context, p app.Phase, _ string, fn func(context.Context) error) error {
			phases = append(phases, p)
			return fn(ctx)
		}})
	ctx := context.Background()

	got, err := a.BranchPR(ctx, repo, "c")
	if err != nil || got.Number != 12 || got.URL != "u12" {
		t.Fatalf("c: %+v %v", got, err)
	}
	if ff.calls != 1 || !slices.Equal(phases, []app.Phase{app.PhaseRefresh}) {
		t.Errorf("want one refresh under its own step: %d calls, phases %v", ff.calls, phases)
	}

	_, err = a.BranchPR(ctx, repo, "a")
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Msg != "a has no pull request yet" || !slices.Contains(se.NextSteps, "git stack submit") {
		t.Errorf("a: %v", err)
	}

	ff.err = errors.New("offline")
	if _, err := a.BranchPR(ctx, repo, "a"); err == nil || !errors.Is(err, ff.err) {
		t.Errorf("a failed refresh should say so: %v", err)
	}
}

func TestBranchPRRefusesTrunksAndUntrackedBranches(t *testing.T) {
	a, repo, _ := fixture(t)
	ff := &fakeForge{}
	a = app.New(app.Deps{Git: gitClient(), Meta: metaOf(a, t), Forge: ff, Cache: cache.New(repo), Config: config.Defaults()})
	ctx := context.Background()
	for branch, want := range map[string]stack.Kind{"main": stack.KindInvalidArgs, "loose": stack.KindNotInStack, "nope": stack.KindNotInStack} {
		_, err := a.BranchPR(ctx, repo, branch)
		if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != want {
			t.Errorf("%s: %v, want kind %v", branch, err, want)
		}
	}
	if ff.calls != 0 {
		t.Errorf("refusals must not ask the forge (%d calls)", ff.calls)
	}
}
