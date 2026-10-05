package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// All is accepted for gt parity; every stack is synced anyway.
	All bool
}

// SyncedStack is one stack that was synced.
type SyncedStack struct {
	// Branch is the branch gh stack ran from (identifies the stack).
	Branch string `json:"branch"`
	// Worktree is where it ran: the worktree holding the stack's metadata.
	Worktree string `json:"worktree"`
	// CheckedOut is true when Branch had to be checked out in that worktree
	// for the sync (and the previous branch restored afterwards).
	CheckedOut bool `json:"checkedOut,omitempty"`
	// Aborted is true when gh stack refused to sync (the remote stack
	// diverged) without changing anything.
	Aborted bool `json:"aborted"`
	// Error is set when this stack's sync failed; the others still ran.
	Error string `json:"error,omitempty"`
}

// SyncResult reports the outcome.
type SyncResult struct {
	Output  string   `json:"output,omitempty"`
	Notices []string `json:"notices,omitempty"`
	// Aborted is true when any stack's sync was refused.
	Aborted bool `json:"aborted"`
	// Pruned is true when merged branches were deleted (by policy, flag or
	// confirmation at the prompt).
	Pruned bool `json:"pruned"`
	// Stacks lists what was synced, the current stack first.
	Stacks []SyncedStack `json:"stacks"`
}

// syncJob is one gh stack sync run.
type syncJob struct {
	stack *stack.Stack
	dir   string // worktree to run in
	// branch is the stack branch to run from; switchBack is the branch the
	// worktree was on and gets restored, when a checkout was needed.
	branch     string
	switchBack string
	detached   string // original HEAD when the worktree was detached
}

// Sync fetches, updates trunk, restacks and pushes every stack, the way
// `gt sync` does: the one you are on first, then every other stack whether
// it is checked out or not and whichever worktree holds it. gh stack syncs
// the stack of the branch checked out where it runs and only knows the stacks
// in that worktree's metadata, so a stack that is not checked out gets one of
// its branches checked out in its home worktree for the duration and the
// previous branch restored afterwards. Merged branches are deleted according
// to stack.sync.prune.
func (a *App) Sync(ctx context.Context, repo git.Repo, o SyncOptions) (SyncResult, error) {
	if a.d.Sync == nil {
		return SyncResult{}, stack.New(stack.KindUnsupported, "no sync backend configured")
	}
	var res SyncResult
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return SyncResult{}, err
	}
	if len(graph.Stacks) == 0 {
		return SyncResult{}, stack.New(stack.KindNotInStack, "there are no stacks to sync").
			WithSteps("check out your trunk and run git stack create to start one")
	}

	prune, err := a.syncPrune(ctx, repo, graph, o.Prune, &res)
	if err != nil {
		return SyncResult{}, err
	}
	res.Pruned = prune

	jobs, err := a.planSync(ctx, repo, graph, &res)
	if err != nil {
		return SyncResult{}, err
	}

	var outputs, failed []string
	for _, job := range jobs {
		out, err := a.runSyncJob(ctx, repo, job, prune)
		synced := SyncedStack{Branch: job.branch, Worktree: job.dir, CheckedOut: job.switchBack != "" || job.detached != ""}
		if err != nil {
			// Like gt, sync what can be synced and report the rest.
			if ctx.Err() != nil {
				return SyncResult{}, err
			}
			synced.Error = err.Error()
			failed = append(failed, job.branch)
			res.Stacks = append(res.Stacks, synced)
			continue
		}
		if strings.Contains(out.Output, "Sync aborted") {
			synced.Aborted = true
			res.Aborted = true
			res.Notices = append(res.Notices, fmt.Sprintf("the remote stack for %s has diverged; nothing was changed there. Run gh stack sync from %s in %s to choose how to reconcile",
				job.branch, job.branch, shortPath(job.dir)))
		}
		res.Stacks = append(res.Stacks, synced)
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
	if len(failed) > 0 {
		// The result is still returned so callers can show what did sync.
		var detail []string
		for _, s := range res.Stacks {
			if s.Error != "" {
				detail = append(detail, s.Branch+": "+s.Error)
			}
		}
		return res, stack.Newf(stack.KindUnknown, "%d of %d %s failed to sync (%s)",
			len(failed), len(jobs), pluralise(len(jobs), "stack", "stacks"), strings.Join(failed, ", ")).
			WithDetail(strings.Join(detail, "\n")).
			WithSteps("fix the cause and run git stack sync again; the other stacks are already in sync")
	}
	return res, nil
}

// runSyncJob runs one job under a headline, checking a branch out and back
// when the stack isn't checked out in its worktree.
func (a *App) runSyncJob(ctx context.Context, repo git.Repo, job syncJob, prune bool) (stack.SyncResult, error) {
	wtRepo := repo
	wtRepo.TopLevel = job.dir
	headline := "Syncing " + job.branch
	if job.dir != repo.TopLevel {
		headline += " in " + shortPath(job.dir)
	}
	var out stack.SyncResult
	err := a.progress(ctx, PhaseSync, headline, func(ctx context.Context) error {
		if job.switchBack != "" || job.detached != "" {
			if err := a.d.Git.Switch(ctx, wtRepo, job.branch); err != nil {
				return err
			}
			defer func() {
				// gh stack may have deleted (pruned) or moved things; put the
				// worktree back where it was if that still exists.
				if job.detached != "" {
					_ = a.d.Git.SwitchDetached(ctx, wtRepo, job.detached)
					return
				}
				if ok, _ := a.d.Git.BranchExists(ctx, wtRepo, job.switchBack); ok {
					if err := a.d.Git.Switch(ctx, wtRepo, job.switchBack); err != nil {
						a.d.Log.Warn("could not switch back", "branch", job.switchBack, "err", err)
					}
				}
			}()
		}
		dir := ""
		if job.dir != repo.TopLevel {
			dir = job.dir
		}
		var err error
		out, err = a.d.Sync.Sync(ctx, repo, stack.SyncOptions{Prune: prune, Dir: dir})
		return err
	})
	return out, err
}

// planSync decides where and from which branch each stack is synced. Order:
// the current worktree's checked out stack, the rest of the current
// worktree's stacks, then other worktrees. Stacks that cannot be synced
// (every branch checked out elsewhere, or a dirty worktree that would need a
// checkout) are reported in notices.
func (a *App) planSync(ctx context.Context, repo git.Repo, graph *stack.Graph, res *SyncResult) ([]syncJob, error) {
	wts, err := a.d.Git.Worktrees(ctx, repo)
	if err != nil {
		return nil, err
	}
	// gh stack keeps a branch in its metadata after --prune has deleted it,
	// so only branches that still exist can be checked out and synced from.
	locals, err := a.d.Git.Branches(ctx, repo)
	if err != nil {
		return nil, err
	}
	exists := make(map[string]bool, len(locals))
	for _, b := range locals {
		exists[b.Name] = true
	}
	byPath := map[string]git.Worktree{}
	checkedOut := map[string]string{} // branch -> worktree path
	for _, wt := range wts {
		byPath[wt.Path] = wt
		if wt.Branch != "" {
			checkedOut[wt.Branch] = wt.Path
		}
	}
	dirty := map[string]bool{} // worktree path -> has uncommitted changes (cached)
	isDirty := func(dir string) bool {
		if v, ok := dirty[dir]; ok {
			return v
		}
		r := repo
		r.TopLevel = dir
		staged, _ := a.d.Git.HasStagedChanges(ctx, r)
		unstaged, _ := a.d.Git.HasUnstagedChanges(ctx, r)
		dirty[dir] = staged || unstaged
		return dirty[dir]
	}

	// Order: current worktree (checked out stack, then the rest), then other
	// worktrees (checked out stacks, then the rest).
	var first, mine, others, othersSwitch []syncJob
	for i := range graph.Stacks {
		s := &graph.Stacks[i]
		if len(s.Branches) == 0 {
			continue
		}
		home := s.Worktree
		if home == "" {
			home = repo.TopLevel
		}
		wt, ok := byPath[home]
		if !ok {
			res.Notices = append(res.Notices, fmt.Sprintf("stack %s lives in %s, which is not a worktree any more; it was not synced", s.Bottom(), shortPath(home)))
			continue
		}
		var alive []stack.Branch
		for _, b := range s.Branches {
			if exists[b.Name] {
				alive = append(alive, b)
			}
		}
		if len(alive) == 0 {
			if slices.ContainsFunc(s.Branches, func(b stack.Branch) bool { return !b.Merged() }) {
				res.Notices = append(res.Notices, fmt.Sprintf("stack %s: its branches no longer exist locally but their pull requests have not merged; it was not synced", s.Bottom()))
			} else {
				// Every PR merged and every branch pruned: the stack is
				// finished and gh stack just hasn't forgotten it.
				a.d.Log.Debug("skipping finished stack", "stack", s.Bottom())
			}
			continue
		}
		job := syncJob{stack: s, dir: home}
		if s.Index(wt.Branch) >= 0 {
			job.branch = wt.Branch
		} else {
			// Pick a branch of the stack nobody has checked out, top first,
			// preferring one whose PR is still open: prune would delete a
			// merged one from under us mid-sync.
			job.branch = pickSyncBranch(alive, checkedOut, false)
			if job.branch == "" {
				job.branch = pickSyncBranch(alive, checkedOut, true)
			}
			switch {
			case job.branch == "":
				res.Notices = append(res.Notices, fmt.Sprintf("stack %s: every branch is checked out in another worktree; sync it from there", s.Bottom()))
				continue
			case isDirty(home):
				res.Notices = append(res.Notices, fmt.Sprintf("stack %s is not checked out and %s has uncommitted changes; commit or stash them and sync again", s.Bottom(), shortPath(home)))
				continue
			case wt.Detached:
				job.detached = wt.Head
			default:
				job.switchBack = wt.Branch
			}
		}
		needsCheckout := job.switchBack != "" || job.detached != ""
		switch {
		case home == repo.TopLevel && !needsCheckout:
			first = append(first, job)
		case home == repo.TopLevel:
			mine = append(mine, job)
		case !needsCheckout:
			others = append(others, job)
		default:
			othersSwitch = append(othersSwitch, job)
		}
	}
	return slices.Concat(first, mine, others, othersSwitch), nil
}

// pickSyncBranch returns the topmost branch of alive that is not checked
// out in any worktree, skipping merged ones unless allowMerged; "" if none.
func pickSyncBranch(alive []stack.Branch, checkedOut map[string]string, allowMerged bool) string {
	for j := len(alive) - 1; j >= 0; j-- {
		b := alive[j]
		if _, taken := checkedOut[b.Name]; taken || (b.Merged() && !allowMerged) {
			continue
		}
		return b.Name
	}
	return ""
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
