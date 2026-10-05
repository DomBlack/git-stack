package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Why a branch was deleted.
const (
	ReasonMerged  = "merged"
	ReasonClosed  = "closed"
	ReasonInTrunk = "in-trunk"
	ReasonGone    = "gone" // already deleted locally; only the metadata changed
)

// Why a candidate was kept.
const (
	KeptPolicy        = "policy"
	KeptOtherWorktree = "other-worktree"
	KeptDirty         = "dirty"
	KeptNotInTrunk    = "not-in-trunk"
	KeptNoTrunk       = "no-trunk"
	KeptUnpushed      = "unpushed"
)

// candidate is a branch that may be deleted.
type candidate struct {
	name, head, reason, trunk string
}

// syncCleanup deletes branches whose work is done: tracked branches whose
// PR merged or closed or whose tip is in trunk (including one trunk was fast
// forwarded onto, which its recorded base gives away), and untracked branches whose
// merged PR was for exactly the commit they're on. Branches already gone
// locally are forgotten by the metadata whatever the policy.
func (a *App) syncCleanup(ctx context.Context, st *syncState, o SyncOptions, res *SyncResult) error {
	cands, gone := a.cleanupCandidates(ctx, st, res)
	approved := []candidate{}
	if len(cands) > 0 {
		ok, err := a.cleanupConsent(o, cands, res)
		if err != nil {
			return err
		}
		if ok {
			approved = cands
		}
	}
	if len(approved) == 0 && len(gone) == 0 {
		return nil
	}
	return a.progress(ctx, PhaseSync, "Cleaning up merged branches", func(ctx context.Context) error {
		removed := slices.Clone(gone)
		loopErr := a.deleteApproved(ctx, st, approved, res, &removed)
		// The metadata must forget whatever was deleted even if the loop stopped early.
		updateErr := a.d.Meta.Update(ctx, st.repo, func(g *stack.Graph) error {
			for i := range g.Stacks {
				g.Stacks[i].Branches = slices.DeleteFunc(g.Stacks[i].Branches, func(b stack.Branch) bool {
					return slices.Contains(removed, b.Name)
				})
			}
			return nil
		})
		if updateErr == nil {
			if graph, err := a.d.Meta.Load(ctx, st.repo); err != nil {
				updateErr = err
			} else {
				st.graph = graph
			}
		}
		return errors.Join(loopErr, updateErr)
	})
}

// deleteApproved deletes the approved candidates, appending each deleted name
// to removed. It stops at the first error.
func (a *App) deleteApproved(ctx context.Context, st *syncState, approved []candidate, res *SyncResult, removed *[]string) error {
	for _, c := range approved {
		lb := st.local[c.name]
		switch lb.Worktree {
		case "":
		case st.repo.TopLevel:
			if st.isDirty(ctx, a.d.Git, lb.Worktree) {
				res.Kept = append(res.Kept, KeptBranch{Name: c.name, Reason: KeptDirty})
				res.notice("%s is checked out here with uncommitted changes, so it was kept; commit or stash them and sync again", c.name)
				continue
			}
			trunk, ok := st.local[c.trunk]
			if !ok {
				res.Kept = append(res.Kept, KeptBranch{Name: c.name, Reason: KeptNoTrunk})
				res.notice("%s is checked out here and its trunk %s has no local branch, so it was kept", c.name, c.trunk)
				continue
			}
			if trunk.Worktree != "" && trunk.Worktree != st.repo.TopLevel {
				if err := a.d.Git.SwitchDetached(ctx, st.repo, trunk.Head); err != nil {
					return err
				}
				res.notice("%s was checked out here; HEAD is now detached at %s (%s) because %s is checked out in %s", c.name, c.trunk, short(trunk.Head), c.trunk, shortPath(trunk.Worktree))
			} else {
				if err := a.d.Git.Switch(ctx, st.repo, c.trunk); err != nil {
					return err
				}
				st.local[c.trunk] = withWorktree(trunk, st.repo.TopLevel)
			}
		default:
			res.Kept = append(res.Kept, KeptBranch{Name: c.name, Reason: KeptOtherWorktree})
			res.notice("%s is checked out in %s, so it was kept; sync from there or remove the worktree", c.name, shortPath(lb.Worktree))
			continue
		}
		if err := a.d.Git.DeleteBranch(ctx, st.repo, c.name); err != nil {
			return err
		}
		delete(st.local, c.name)
		res.Deleted = append(res.Deleted, DeletedBranch{Name: c.name, Head: c.head, Reason: c.reason})
		*removed = append(*removed, c.name)
	}
	return nil
}

// short abbreviates a commit id for messages.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// withWorktree records that a branch is now checked out at path.
func withWorktree(b git.Branch, path string) git.Branch {
	b.Worktree = path
	return b
}

// cleanupCandidates finds what could go. gone lists tracked branches that
// no longer exist locally.
func (a *App) cleanupCandidates(ctx context.Context, st *syncState, res *SyncResult) (cands []candidate, gone []string) {
	for _, s := range st.graph.Stacks {
		trunkTip := st.local[s.Trunk].Head
		for _, b := range s.Branches {
			lb, exists := st.local[b.Name]
			if !exists {
				gone = append(gone, b.Name)
				res.Deleted = append(res.Deleted, DeletedBranch{Name: b.Name, Reason: ReasonGone})
				continue
			}
			pr, hasPR := st.prs[b.Name]
			if hasPR && (pr.State == forge.StateMerged || pr.State == forge.StateClosed) && pr.HeadSHA != "" && pr.HeadSHA != lb.Head &&
				!a.prSawAll(ctx, st, lb.Head, pr.HeadSHA, trunkTip) {
				res.Kept = append(res.Kept, KeptBranch{Name: b.Name, Reason: KeptUnpushed})
				res.notice("%s is not at the commit its PR was %s at (local %s, PR %s); kept in case there is work on it, delete it by hand with git branch -D %s",
					b.Name, pr.State, short(lb.Head), short(pr.HeadSHA), b.Name)
				continue
			}
			switch {
			case b.Merged() || (hasPR && pr.State == forge.StateMerged):
				if hasPR && pr.MergeCommit != "" && !a.ancestor(ctx, st, pr.MergeCommit, trunkTip) {
					res.Kept = append(res.Kept, KeptBranch{Name: b.Name, Reason: KeptNotInTrunk})
					res.notice("%s merged into %s but that isn't in %s yet, so it was kept", b.Name, pr.Base, s.Trunk)
					continue
				}
				cands = append(cands, candidate{b.Name, lb.Head, ReasonMerged, s.Trunk})
			case hasPR && pr.State == forge.StateClosed:
				cands = append(cands, candidate{b.Name, lb.Head, ReasonClosed, s.Trunk})
			case a.ancestor(ctx, st, lb.Head, trunkTip) && (lb.Head != trunkTip || a.movedPastBase(ctx, st, b, lb.Head)):
				cands = append(cands, candidate{b.Name, lb.Head, ReasonInTrunk, s.Trunk})
			}
		}
	}
	// Untracked branches: only when a merged PR was for the exact commit
	// they're on, so a branch recreated under an old name is safe.
	for name, lb := range st.local {
		if st.graph.Tracked(name) || st.graph.IsTrunk(name) || slices.Contains(st.trunks, name) {
			continue
		}
		pr, ok := st.prs[name]
		if !ok || pr.State != forge.StateMerged || pr.HeadSHA != lb.Head {
			continue
		}
		trunk := st.trunks[0]
		if slices.Contains(st.trunks, pr.Base) {
			trunk = pr.Base
		}
		if pr.MergeCommit != "" && !a.ancestor(ctx, st, pr.MergeCommit, st.local[trunk].Head) {
			continue
		}
		cands = append(cands, candidate{name, lb.Head, ReasonMerged, trunk})
	}
	slices.SortFunc(cands, func(x, y candidate) int { return strings.Compare(x.name, y.name) })
	return cands, gone
}

// movedPastBase tells a branch that was fast forwarded into trunk from one
// that was created empty: both sit exactly on the trunk tip, but only the
// first has a recorded base that is an older commit it has since moved past.
// A branch with no recorded base, or one reset by hand so its base is no
// longer behind it, is given the benefit of the doubt and kept.
func (a *App) movedPastBase(ctx context.Context, st *syncState, b stack.Branch, tip string) bool {
	return b.Base != "" && b.Base != tip && a.ancestor(ctx, st, b.Base, tip)
}

// prSawAll reports whether every change on a branch's local tip, other than
// what came from trunk, is in its PR's head: a branch restacked locally
// since its last push differs from the PR by commit id but not by patch id.
// Anything it can't tell, such as a PR head we don't have locally, is "no".
func (a *App) prSawAll(ctx context.Context, st *syncState, local, prHead, trunkTip string) bool {
	if _, ok, err := a.d.Git.Tip(ctx, st.repo, prHead); err != nil || !ok {
		return false
	}
	base, err := a.d.Git.MergeBase(ctx, st.repo, local, prHead)
	if err != nil {
		return false
	}
	exclude := []string{prHead}
	if trunkTip != "" {
		exclude = append(exclude, trunkTip)
	}
	localIDs, err := a.d.Git.PatchIDsExcluding(ctx, st.repo, local, exclude...)
	if err != nil {
		return false
	}
	prIDs, err := a.d.Git.PatchIDs(ctx, st.repo, base, prHead)
	if err != nil {
		return false
	}
	for id := range localIDs {
		if _, ok := prIDs[id]; !ok {
			return false
		}
	}
	return true
}

// cleanupConsent decides whether the candidates may go: -f or -d say yes,
// the policy decides otherwise, and ask asks once on a terminal.
func (a *App) cleanupConsent(o SyncOptions, cands []candidate, res *SyncResult) (bool, error) {
	if o.Force || o.DeleteAll {
		return true, nil
	}
	keep := func(reason string) {
		for _, c := range cands {
			res.Kept = append(res.Kept, KeptBranch{Name: c.name, Reason: reason})
		}
	}
	names := make([]string, len(cands))
	for i, c := range cands {
		names[i] = c.name + ": " + c.reason
	}
	what := fmt.Sprintf("%d %s (%s)", len(cands), pluralise(len(cands), "branch", "branches"), strings.Join(names, ", "))
	switch {
	case a.d.Config.SyncPrune == config.SyncPruneNever:
		keep(KeptPolicy)
		res.notice("%s kept because stack.sync.prune is never; run git stack sync -d to delete them", what)
		return false, nil
	case a.d.Config.SyncPrune != config.SyncPruneAsk:
		return true, nil
	case a.d.Prompter == nil:
		keep(KeptPolicy)
		res.notice("%s kept; run git stack sync -d to delete them, or set stack.sync.prune to always", what)
		return false, nil
	}
	ok, err := a.d.Prompter.Confirm("Delete "+what+"?", true)
	if err != nil {
		return false, err
	}
	if !ok {
		keep(KeptPolicy)
	}
	return ok, nil
}
