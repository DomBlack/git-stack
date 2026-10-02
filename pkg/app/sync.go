package app

import (
	"context"
	"strings"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// SyncOptions mirrors `gt sync`.
type SyncOptions struct {
	// Prune deletes local branches whose PRs were merged (gt -f).
	Prune bool
	// All asks for every stack; gh stack can only sync the current one.
	All bool
}

// SyncResult reports the outcome.
type SyncResult struct {
	Output  string   `json:"output,omitempty"`
	Notices []string `json:"notices,omitempty"`
	// Aborted is true when the backend refused to sync (e.g. the remote
	// stack diverged) without changing anything.
	Aborted bool `json:"aborted"`
}

// Sync fetches, updates trunk, restacks and optionally prunes the current
// stack through the backend.
func (a *App) Sync(ctx context.Context, repo git.Repo, o SyncOptions) (SyncResult, error) {
	if a.d.Sync == nil {
		return SyncResult{}, stack.New(stack.KindUnsupported, "no sync backend configured")
	}
	var res SyncResult
	if o.All {
		res.Notices = append(res.Notices, "gh stack syncs the current stack only; --all was ignored")
	}
	var out stack.SyncResult
	err := a.progress(ctx, PhaseSync, "Syncing with origin", func(ctx context.Context) error {
		var err error
		out, err = a.d.Sync.Sync(ctx, repo, stack.SyncOptions{Prune: o.Prune})
		return err
	})
	if err != nil {
		return SyncResult{}, err
	}
	if !out.Streamed {
		res.Output = out.Output
	}
	if strings.Contains(out.Output, "Sync aborted") {
		res.Aborted = true
		res.Notices = append(res.Notices, "the remote stack has diverged; nothing was changed. Run `gh stack sync` in a terminal to choose how to reconcile")
	}
	if a.d.Forge != nil {
		if _, err := a.RefreshPRs(ctx, repo); err != nil {
			a.d.Log.Debug("refresh after sync", "err", err)
		}
	}
	return res, nil
}
