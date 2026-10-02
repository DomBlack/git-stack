package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/DomBlack/git-stack/pkg/forge"
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
	// Pruned is true when merged branches were deleted (asked for, or
	// confirmed at the prompt).
	Pruned bool `json:"pruned"`
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

	// gh stack only deletes merged branches with --prune, and would only ask
	// about them on a terminal it doesn't get from us. So we ask (Graphite
	// does too), or say what was kept when nobody can answer.
	prune := o.Prune
	if !prune {
		merged := a.mergedBranches(ctx, repo)
		switch {
		case len(merged) == 0:
		case a.d.Prompter != nil:
			ok, err := a.d.Prompter.Confirm(fmt.Sprintf("Delete %d merged %s (%s) once synced?",
				len(merged), pluralise(len(merged), "branch", "branches"), strings.Join(merged, ", ")), true)
			if err != nil {
				return SyncResult{}, err
			}
			prune = ok
		default:
			res.Notices = append(res.Notices, fmt.Sprintf("%d merged %s kept (%s); run git stack sync -f to delete them",
				len(merged), pluralise(len(merged), "branch", "branches"), strings.Join(merged, ", ")))
		}
	}
	res.Pruned = prune

	var out stack.SyncResult
	err := a.progress(ctx, PhaseSync, "Syncing with origin", func(ctx context.Context) error {
		var err error
		out, err = a.d.Sync.Sync(ctx, repo, stack.SyncOptions{Prune: prune})
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

// mergedBranches lists the branches of the current stack whose pull request
// has merged, from the backend's metadata and a fresh look at the forge.
func (a *App) mergedBranches(ctx context.Context, repo git.Repo) []string {
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		return nil
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return nil
	}
	s, _, ok := graph.StackOf(current)
	if !ok {
		return nil
	}
	prs := PRsFor(a.loadPRs(ctx, repo, PRsFresh))
	var merged []string
	for _, b := range s.Branches {
		if pr, has := prs[b.Name]; b.Merged() || (has && pr.State == forge.StateMerged) {
			merged = append(merged, b.Name)
		}
	}
	return merged
}
