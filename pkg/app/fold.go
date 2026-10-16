package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// FoldOptions is one fold of the current branch into its parent.
type FoldOptions struct {
	// Keep keeps the current branch's name and pull request instead of
	// the parent's.
	Keep bool
}

// FoldResult reports what happened.
type FoldResult struct {
	// Into is the branch that survives, Folded the one that's gone.
	Into   string `json:"into"`
	Folded string `json:"folded"`
	// Closing is the pull request queued to close at the next submit, 0 if
	// none.
	Closing int      `json:"closing,omitempty"`
	Notices []string `json:"notices,omitempty"`
}

const foldCommand = "git stack fold"

// Fold merges the current branch and its parent into one branch, the
// parent's (or with Keep the current one's), and queues the other's pull
// request to be closed at the next submit. The current branch must sit on
// its parent, so nothing needs restacking: the branches above already have
// every commit involved and only their recorded parent changes.
func (a *App) Fold(ctx context.Context, repo git.Repo, o FoldOptions) (FoldResult, error) {
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return FoldResult{}, err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		if errors.Is(err, git.ErrDetached) {
			return FoldResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch first")
		}
		return FoldResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return FoldResult{}, err
	}
	s, idx, ok := graph.StackOf(current)
	if !ok {
		if graph.IsTrunk(current) {
			return FoldResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; fold works on stacked branches", current)
		}
		return FoldResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", current).
			WithSteps("git stack init " + current + " to start tracking it")
	}
	if idx == 0 {
		return FoldResult{}, stack.Newf(stack.KindInvalidArgs, "%s is the bottom of its stack; there's nothing to fold it into", current)
	}
	if err := noMergedBranches(s); err != nil {
		return FoldResult{}, err
	}
	parent := s.Branches[idx-1]
	local, err := a.localBranches(ctx, repo)
	if err != nil {
		return FoldResult{}, err
	}
	pl, ok := local[parent.Name]
	if !ok {
		return FoldResult{}, stack.Newf(stack.KindInvalidArgs, "%s, the branch below %s, no longer exists", parent.Name, current).
			WithSteps("run git stack sync first")
	}
	cTip, pTip := local[current].Head, pl.Head
	if !a.isAncestor(ctx, repo, pTip, cTip) || a.baseAbove(ctx, repo, s.Branches[idx].Base, pTip, cTip) {
		return FoldResult{}, stack.Newf(stack.KindInvalidArgs, "%s isn't on %s yet", current, parent.Name).
			WithSteps("run git stack restack first")
	}
	// P's ref moves (or with Keep is deleted), so it can't be checked out
	// anywhere; C is checked out here, which the switch takes care of.
	if pl.Worktree != "" {
		return FoldResult{}, stack.Newf(stack.KindInvalidArgs, "%s is checked out in %s", parent.Name, shortPath(pl.Worktree)).
			WithSteps("switch that checkout to another branch first")
	}
	res := FoldResult{Into: parent.Name, Folded: current}
	if o.Keep {
		res.Into, res.Folded = current, parent.Name
	}
	// With Keep, C takes P's base. P's own commits start there; without a
	// recorded one, at the merge base with P's parent.
	keepBase := parent.Base
	if o.Keep && keepBase == "" {
		below := s.Trunk
		if idx > 1 {
			below = s.Branches[idx-2].Name
		}
		if lb, ok := local[below]; ok {
			if keepBase, err = a.d.Git.MergeBase(ctx, repo, pTip, lb.Head); err != nil {
				return FoldResult{}, err
			}
		}
	}

	var queued []int
	if c := a.foldClose(ctx, repo, graph, res, local[res.Folded].Head); c != nil {
		if err := addPendingClose(repo, []pendingClose{*c}); err != nil {
			return res, err
		}
		queued = []int{c.Number}
	}
	bottom := s.Bottom()
	snap := snapshotOf(graph, []string{bottom})
	e := reshapeEdit{
		touched: []string{bottom},
		relink:  []string{bottom},
		edit: func(g *stack.Graph) error {
			s, i, ok := g.StackOf(current)
			if !ok || i == 0 || s.Branches[i-1].Name != parent.Name {
				return stack.New(stack.KindInvalidArgs, "the stack changed while it was being folded; try again")
			}
			if o.Keep {
				s.Branches[i].Base = keepBase
				s.Branches = slices.Delete(s.Branches, i-1, i)
				return nil
			}
			s.Branches[i-1].Head = cTip
			s.Branches = slices.Delete(s.Branches, i, i+1)
			return nil
		},
	}
	// undo puts the shape back and forgets the close, for a failure before
	// the fold is visible.
	undo := func(err error) error {
		if derr := dropPendingClose(repo, queued); derr != nil {
			err = errors.Join(err, derr)
		}
		if rerr := a.restoreReshape(ctx, repo, snap); rerr != nil {
			return asStackError(err).WithSteps(fmt.Sprintf("the stack's new shape was kept because putting it back failed (%v)", rerr))
		}
		return asStackError(err).WithSteps("nothing was changed")
	}
	if _, err := a.reshape(ctx, repo, foldCommand, "", e); err != nil {
		if derr := dropPendingClose(repo, queued); derr != nil {
			err = errors.Join(err, derr)
		}
		return res, err
	}
	if o.Keep {
		if err := a.d.Git.DeleteBranch(ctx, repo, parent.Name); err != nil {
			return res, undo(err)
		}
	} else {
		ref := "refs/heads/" + parent.Name
		if err := a.d.Git.UpdateRefs(ctx, repo, []git.RefUpdate{{Ref: ref, New: cTip, Old: pTip}}); err != nil {
			return res, undo(err)
		}
		// P and C are the same commit now, so the switch leaves the working
		// tree alone and carries any local changes along.
		if err := a.d.Git.Switch(ctx, repo, parent.Name); err != nil {
			if rerr := a.d.Git.UpdateRefs(ctx, repo, []git.RefUpdate{{Ref: ref, New: pTip, Old: cTip}}); rerr != nil {
				return res, asStackError(errors.Join(err, rerr)).WithSteps(fmt.Sprintf("%s was moved to %s's tip and could not be put back", parent.Name, current))
			}
			return res, undo(err)
		}
		if err := a.d.Git.DeleteBranch(ctx, repo, current); err != nil {
			res.Notices = append(res.Notices, fmt.Sprintf("%s could not be deleted (%v); git branch -D %s removes it", current, err, current))
		}
	}
	if len(queued) > 0 {
		res.Closing = queued[0]
	}
	return res, nil
}

// foldClose is the close to queue for the folded branch's open pull
// request, or nil when it has none. tip is the folded branch's local tip.
func (a *App) foldClose(ctx context.Context, repo git.Repo, graph *stack.Graph, res FoldResult, tip string) *pendingClose {
	prs, _ := a.loadPRs(ctx, repo, PRsCached)
	byHead := PRsFor(prs)
	open := func(name string) int {
		if pr, ok := byHead[name]; ok {
			if pr.State == forge.StateOpen || pr.State == forge.StateDraft {
				return pr.Number
			}
			return 0
		}
		// Not in the cache yet (submitted since the last refresh); gh
		// stack's own record has it.
		if s, i, ok := graph.StackOf(name); ok {
			if pr := s.Branches[i].PR; pr != nil && !pr.Merged {
				return pr.Number
			}
		}
		return 0
	}
	n := open(res.Folded)
	if n == 0 {
		return nil
	}
	comment := "Folded into " + res.Into
	if into := open(res.Into); into != 0 {
		comment = fmt.Sprintf("Folded into #%d (%s)", into, res.Into)
	}
	// The lease for the remote delete is the tip last pushed, as far as the
	// tracking ref knows; else the local tip.
	remote := a.d.Git.RemoteFor(ctx, repo, res.Folded)
	if pushed, err := a.d.Git.RevParse(ctx, repo, "refs/remotes/"+remote+"/"+res.Folded); err == nil {
		tip = pushed
	}
	return &pendingClose{Number: n, Branch: res.Folded, Comment: comment, Tip: tip}
}
