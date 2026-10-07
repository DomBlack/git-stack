package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// prsCacheName is the cache document holding the PR snapshot.
const prsCacheName = "prs"

// PRSnapshot is the cached forge state.
type PRSnapshot struct {
	PRs []forge.PullRequest `json:"prs"`
}

// PRMode selects how View obtains pull-request state.
type PRMode int

const (
	// PRsNone leaves rows with the backend's snapshot only.
	PRsNone PRMode = iota
	// PRsCached applies the cache regardless of age; never touches the network.
	PRsCached
	// PRsFresh refreshes from the forge when the cache is stale or missing,
	// falling back to the cache on failure.
	PRsFresh
)

// ErrNoForge is returned when no forge adapter is configured.
var ErrNoForge = stack.New(stack.KindUnsupported, "no forge configured for pull requests")

// CachedPRs returns the cached snapshot and its freshness.
func (a *App) CachedPRs(repo git.Repo) ([]forge.PullRequest, cache.State, time.Time) {
	if a.d.Cache == nil {
		return nil, cache.Missing, time.Time{}
	}
	snap, state, saved, err := cache.Read[PRSnapshot](a.d.Cache, prsCacheName, a.d.Config.CacheTTL)
	if err != nil {
		a.d.Log.Debug("read PR cache", "err", err)
		return nil, cache.Missing, time.Time{}
	}
	return snap.PRs, state, saved
}

// RefreshPRs fetches pull requests from the forge and updates the cache.
func (a *App) RefreshPRs(ctx context.Context, repo git.Repo) ([]forge.PullRequest, error) {
	if a.d.Forge == nil {
		return nil, ErrNoForge
	}
	prs, err := a.d.Forge.ListPRs(ctx, repo)
	if err != nil {
		return nil, err
	}
	if a.d.Cache != nil {
		if err := cache.Write(a.d.Cache, prsCacheName, PRSnapshot{PRs: prs}); err != nil {
			a.d.Log.Debug("write PR cache", "err", err)
		}
	}
	return prs, nil
}

// PRsFor picks the pull request to show for each branch: an open or draft
// PR wins over merged, which wins over closed; ties go to the most recent.
func PRsFor(prs []forge.PullRequest) map[string]forge.PullRequest {
	rank := func(s forge.State) int {
		switch s {
		case forge.StateOpen, forge.StateDraft:
			return 3
		case forge.StateMerged:
			return 2
		case forge.StateClosed:
			return 1
		default:
			return 0
		}
	}
	out := make(map[string]forge.PullRequest)
	for _, pr := range prs {
		cur, ok := out[pr.Head]
		if !ok || rank(pr.State) > rank(cur.State) || (rank(pr.State) == rank(cur.State) && pr.UpdatedAt.After(cur.UpdatedAt)) {
			out[pr.Head] = pr
		}
	}
	return out
}

// ApplyPRs decorates rows with forge state. Rows whose branch has no PR in
// prs keep the backend snapshot, if any.
func (v *View) ApplyPRs(prs []forge.PullRequest) {
	byHead := PRsFor(prs)
	for i := range v.Rows {
		if pr, ok := byHead[v.Rows[i].Name]; ok {
			p := pr
			v.Rows[i].PR = &p
		}
	}
}

// loadPRs implements ViewOptions.PRs for View.
func (a *App) loadPRs(ctx context.Context, repo git.Repo, mode PRMode) []forge.PullRequest {
	if mode == PRsNone {
		return nil
	}
	prs, state, _ := a.CachedPRs(repo)
	if mode == PRsFresh && state != cache.Fresh && a.d.Forge != nil {
		fresh, err := a.RefreshPRs(ctx, repo)
		if err == nil {
			return fresh
		}
		if !errors.Is(err, context.Canceled) {
			a.d.Log.Warn("could not refresh pull requests; using cached state", "err", err)
		}
	}
	return prs
}

// PRURLs returns a lookup from pull request number to web URL for
// decorating output. It only uses local state: the URLs recorded in the
// stack metadata and the PR cache, then the forge's own URL scheme for a
// number neither knows (a PR gh stack has only just created). It never
// touches the network, and answers "" for a number it can't place.
func (a *App) PRURLs(ctx context.Context, repo git.Repo) func(number int) string {
	var (
		mu    sync.Mutex
		known map[int]string
	)
	load := func() {
		known = map[int]string{}
		prs, _, _ := a.CachedPRs(repo)
		for _, pr := range prs {
			if pr.URL != "" {
				known[pr.Number] = pr.URL
			}
		}
		if a.d.Meta != nil {
			if g, err := a.d.Meta.Load(ctx, repo); err == nil {
				for _, s := range g.Stacks {
					for _, b := range s.Branches {
						if b.PR != nil && b.PR.URL != "" {
							known[b.PR.Number] = b.PR.URL
						}
					}
				}
			}
		}
	}
	return func(number int) string {
		mu.Lock()
		defer mu.Unlock()
		if known == nil {
			load()
		}
		if u, ok := known[number]; ok {
			return u
		}
		u := ""
		if a.d.Forge != nil {
			var err error
			if u, err = a.d.Forge.PullRequestURL(ctx, repo, number); err != nil {
				a.d.Log.Debug("pull request URL", "number", number, "err", err)
			}
		}
		known[number] = u
		return u
	}
}
