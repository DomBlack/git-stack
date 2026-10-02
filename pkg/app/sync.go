package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// SyncOptions mirrors `gt sync`.
type SyncOptions struct {
	// Prune deletes merged branches whatever stack.sync.prune says (gt -f).
	Prune bool
	// All is accepted for gt parity; every worktree's stack is synced anyway.
	All bool
}

// SyncedWorktree is one worktree that was synced.
type SyncedWorktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	// Aborted is true when gh stack refused to sync this worktree (the
	// remote stack diverged) without changing anything.
	Aborted bool `json:"aborted"`
}

// SyncResult reports the outcome.
type SyncResult struct {
	Output  string   `json:"output,omitempty"`
	Notices []string `json:"notices,omitempty"`
	// Aborted is true when any worktree's sync was refused.
	Aborted bool `json:"aborted"`
	// Pruned is true when merged branches were deleted (by policy, flag or
	// confirmation at the prompt).
	Pruned bool `json:"pruned"`
	// Worktrees lists what was synced, the current worktree first.
	Worktrees []SyncedWorktree `json:"worktrees"`
}

// Sync fetches, updates trunk, restacks and pushes every stack that is
// checked out somewhere: the current worktree first, then each linked
// worktree that is on one of its stacks (gh stack keeps stacks per worktree
// and syncs the one that is checked out where it runs). Merged branches are
// deleted according to stack.sync.prune.
func (a *App) Sync(ctx context.Context, repo git.Repo, o SyncOptions) (SyncResult, error) {
	if a.d.Sync == nil {
		return SyncResult{}, stack.New(stack.KindUnsupported, "no sync backend configured")
	}
	var res SyncResult
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return SyncResult{}, err
	}

	prune, err := a.syncPrune(ctx, repo, graph, o.Prune, &res)
	if err != nil {
		return SyncResult{}, err
	}
	res.Pruned = prune

	targets, err := a.syncTargets(ctx, repo, graph, &res)
	if err != nil {
		return SyncResult{}, err
	}
	if len(targets) == 0 {
		return SyncResult{}, stack.New(stack.KindNotInStack, "no checked out branch is in a stack; nothing to sync").
			WithSteps("check out a stacked branch (git stack checkout) and run git stack sync again")
	}

	var outputs []string
	for _, wt := range targets {
		headline := "Syncing with origin"
		if wt.Path != repo.TopLevel {
			headline = "Syncing worktree " + shortPath(wt.Path)
		}
		var out stack.SyncResult
		err := a.progress(ctx, PhaseSync, headline, func(ctx context.Context) error {
			var err error
			dir := ""
			if wt.Path != repo.TopLevel {
				dir = wt.Path
			}
			out, err = a.d.Sync.Sync(ctx, repo, stack.SyncOptions{Prune: prune, Dir: dir})
			return err
		})
		if err != nil {
			return SyncResult{}, err
		}
		synced := SyncedWorktree{Path: wt.Path, Branch: wt.Branch}
		if strings.Contains(out.Output, "Sync aborted") {
			synced.Aborted = true
			res.Aborted = true
			res.Notices = append(res.Notices, fmt.Sprintf("the remote stack for %s has diverged; nothing was changed there. Run gh stack sync in that worktree to choose how to reconcile", shortPath(wt.Path)))
		}
		res.Worktrees = append(res.Worktrees, synced)
		if !out.Streamed && out.Output != "" {
			outputs = append(outputs, out.Output)
		}
	}
	res.Output = strings.Join(outputs, "\n")

	if a.d.Forge != nil {
		if _, err := a.RefreshPRs(ctx, repo); err != nil {
			a.d.Log.Debug("refresh after sync", "err", err)
		}
	}
	return res, nil
}

// syncPrune decides whether merged branches get deleted: the -f flag or the
// `always` policy say yes, `never` says no, and `ask` asks on a terminal or
// keeps them with a notice when nobody can answer.
func (a *App) syncPrune(ctx context.Context, repo git.Repo, graph *stack.Graph, force bool, res *SyncResult) (bool, error) {
	if force {
		return true, nil
	}
	switch a.d.Config.SyncPrune {
	case config.SyncPruneNever:
		return false, nil
	case config.SyncPruneAsk:
	default:
		return true, nil
	}
	merged := a.mergedBranches(ctx, repo, graph)
	if len(merged) == 0 {
		return false, nil
	}
	what := fmt.Sprintf("%d merged %s (%s)", len(merged), pluralise(len(merged), "branch", "branches"), strings.Join(merged, ", "))
	if a.d.Prompter == nil {
		res.Notices = append(res.Notices, what+" kept; run git stack sync -f to delete them, or set stack.sync.prune to always")
		return false, nil
	}
	return a.d.Prompter.Confirm("Delete "+what+" once synced?", true)
}

// syncTargets picks the worktrees to run gh stack sync in: those whose
// checked out branch is in a stack (or that sit on a trunk with exactly one
// stack), current worktree first. Stacks nobody has checked out get a notice.
func (a *App) syncTargets(ctx context.Context, repo git.Repo, graph *stack.Graph, res *SyncResult) ([]git.Worktree, error) {
	wts, err := a.d.Git.Worktrees(ctx, repo)
	if err != nil {
		return nil, err
	}
	var targets []git.Worktree
	covered := map[*stack.Stack]bool{}
	for _, wt := range wts {
		if wt.Bare || wt.Detached || wt.Branch == "" {
			continue
		}
		var s *stack.Stack
		if st, _, ok := graph.StackOf(wt.Branch); ok {
			s = st
		} else if graph.IsTrunk(wt.Branch) {
			if on := graph.StacksOn(wt.Branch); len(on) == 1 && on[0].Worktree == wt.Path {
				s = on[0]
			}
		}
		if s == nil {
			continue
		}
		covered[s] = true
		if wt.Path == repo.TopLevel {
			targets = append([]git.Worktree{wt}, targets...)
		} else {
			targets = append(targets, wt)
		}
	}
	for i := range graph.Stacks {
		s := &graph.Stacks[i]
		if !covered[s] && len(s.Branches) > 0 {
			where := ""
			if s.Worktree != "" {
				where = " in " + shortPath(s.Worktree)
			}
			res.Notices = append(res.Notices, fmt.Sprintf("stack %s is not checked out%s; it was not synced", s.Bottom(), where))
		}
	}
	return targets, nil
}

// mergedBranches lists every stacked branch whose pull request has merged,
// from the backend's metadata and a fresh look at the forge.
func (a *App) mergedBranches(ctx context.Context, repo git.Repo, graph *stack.Graph) []string {
	prs := PRsFor(a.loadPRs(ctx, repo, PRsFresh))
	var merged []string
	for _, s := range graph.Stacks {
		for _, b := range s.Branches {
			if pr, has := prs[b.Name]; b.Merged() || (has && pr.State == forge.StateMerged) {
				merged = append(merged, b.Name)
			}
		}
	}
	return merged
}

// shortPath shows a worktree path relative to $HOME when it is under it.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return p
}
