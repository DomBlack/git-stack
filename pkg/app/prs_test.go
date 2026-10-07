package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

type fakeForge struct {
	prs   []forge.PullRequest
	err   error
	calls int
}

func (f *fakeForge) ListPRs(context.Context, git.Repo) ([]forge.PullRequest, error) {
	f.calls++
	return f.prs, f.err
}
func (f *fakeForge) CreatePR(context.Context, git.Repo, forge.CreatePR) (forge.PullRequest, error) {
	return forge.PullRequest{}, nil
}
func (f *fakeForge) UpdatePR(context.Context, git.Repo, int, forge.UpdatePR) error { return nil }
func (f *fakeForge) MergeStack(context.Context, git.Repo, int, forge.MergeMethod) (forge.MergeOutcome, error) {
	return forge.MergeOutcome{}, nil
}

func TestPRsForPrefersOpenThenMergedThenRecent(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prs := []forge.PullRequest{
		{Number: 1, Head: "a", State: forge.StateClosed, UpdatedAt: t1.Add(time.Hour)},
		{Number: 2, Head: "a", State: forge.StateMerged, UpdatedAt: t1},
		{Number: 3, Head: "b", State: forge.StateClosed, UpdatedAt: t1},
		{Number: 4, Head: "b", State: forge.StateClosed, UpdatedAt: t1.Add(time.Hour)},
		{Number: 5, Head: "c", State: forge.StateDraft, UpdatedAt: t1},
		{Number: 6, Head: "c", State: forge.StateMerged, UpdatedAt: t1.Add(time.Hour)},
	}
	got := app.PRsFor(prs)
	if got["a"].Number != 2 || got["b"].Number != 4 || got["c"].Number != 5 {
		t.Errorf("PRsFor = %v", got)
	}
}

func TestViewWithCachedAndFreshPRs(t *testing.T) {
	a, repo, _ := fixture(t)
	ff := &fakeForge{prs: []forge.PullRequest{{Number: 5, Head: "b", State: forge.StateOpen, URL: "u5"}, {Number: 9, Head: "c", State: forge.StateDraft}}}
	cfg := config.Defaults()
	cfg.CacheTTL = time.Hour
	a = app.New(app.Deps{Git: gitClient(), Meta: metaOf(a, t), Forge: ff, Cache: cache.New(repo), Config: cfg})
	ctx := context.Background()

	// Cached mode never hits the forge, even with an empty cache.
	v, err := a.View(ctx, repo, app.ViewOptions{PRs: app.PRsCached})
	if err != nil {
		t.Fatal(err)
	}
	if ff.calls != 0 {
		t.Fatal("PRsCached must not call the forge")
	}
	if r, _ := v.Row("b"); r.PR == nil || r.PR.Number != 5 || r.PR.State != forge.StateUnknown {
		t.Errorf("backend snapshot expected before refresh: %+v", r.PR)
	}

	v, err = a.View(ctx, repo, app.ViewOptions{PRs: app.PRsFresh})
	if err != nil {
		t.Fatal(err)
	}
	if ff.calls != 1 {
		t.Fatalf("PRsFresh should refresh once, got %d", ff.calls)
	}
	if r, _ := v.Row("b"); r.PR == nil || r.PR.State != forge.StateOpen || r.PR.URL != "u5" {
		t.Errorf("b = %+v", r.PR)
	}
	if r, _ := v.Row("c"); r.PR == nil || r.PR.Number != 9 || r.PR.State != forge.StateDraft {
		t.Errorf("c = %+v", r.PR)
	}

	// Fresh cache: no second forge call.
	if _, err := a.View(ctx, repo, app.ViewOptions{PRs: app.PRsFresh}); err != nil || ff.calls != 1 {
		t.Errorf("fresh cache should be reused: calls=%d err=%v", ff.calls, err)
	}
	prs, state, _ := a.CachedPRs(repo)
	if state != cache.Fresh || len(prs) != 2 {
		t.Errorf("CachedPRs = %d %v", len(prs), state)
	}

	// Forge failure with a stale cache falls back to the cached state.
	cfg.CacheTTL = 0
	ff.err = errors.New("offline")
	v, err = a.View(ctx, repo, app.ViewOptions{PRs: app.PRsFresh})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := v.Row("c"); r.PR == nil || r.PR.Number != 9 {
		t.Errorf("stale fallback: %+v", r.PR)
	}
	if _, err := a.RefreshPRs(ctx, repo); err == nil {
		t.Error("RefreshPRs should surface the forge error")
	}

	none := app.New(app.Deps{Git: gitClient(), Meta: metaOf(a, t), Cache: cache.New(repo)})
	if _, err := none.RefreshPRs(ctx, repo); !errors.Is(err, &stack.Error{Kind: stack.KindUnsupported}) {
		t.Errorf("no forge: %v", err)
	}
}

func gitClient() *git.Client { return git.New(exec.New()) }

// metaOf reuses the fixture's metadata through the public surface.
func metaOf(a *app.App, t *testing.T) stack.Metadata {
	t.Helper()
	return memMeta{stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{
		{Name: "a"}, {Name: "b", PR: &stack.PRRef{Number: 5, URL: "u"}}, {Name: "c"},
	}}})}
}

func (f *fakeForge) PullRequestURL(_ context.Context, _ git.Repo, n int) (string, error) {
	return fmt.Sprintf("https://forge.test/o/r/pull/%d", n), nil
}

func TestPRURLsUsesLocalStateThenTheForge(t *testing.T) {
	a, repo, _ := fixture(t)
	ff := &fakeForge{}
	a = app.New(app.Deps{Git: gitClient(), Meta: metaOf(a, t), Forge: ff, Cache: cache.New(repo)})
	url := a.PRURLs(context.Background(), repo)
	for _, tc := range []struct {
		number int
		want   string
	}{
		{5, "u"}, // recorded in the stack metadata
		{77, "https://forge.test/o/r/pull/77"},
	} {
		if got := url(tc.number); got != tc.want {
			t.Errorf("PR %d: got %q, want %q", tc.number, got, tc.want)
		}
	}
	if ff.calls != 0 {
		t.Error("PRURLs must never list pull requests from the forge")
	}
}
