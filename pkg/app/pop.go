package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// PopResult reports what a pop did.
type PopResult struct {
	Branch string `json:"branch"`
	Parent string `json:"parent"`
	// StackGone is true when the popped branch was the only one.
	StackGone bool     `json:"stackGone,omitempty"`
	Restacked []string `json:"restacked,omitempty"`
	// Closing is the popped branch's pull request, queued to close at the
	// next submit.
	Closing int      `json:"closing,omitempty"`
	Notices []string `json:"notices,omitempty"`
}

const popCommand = "git stack pop"

// popComment is left on the popped branch's pull request when it closes.
const popComment = "Closed by git stack pop; its changes went back into the working tree."

// Pop deletes the current branch and takes it out of its stack, leaving
// its changes staged on its parent. The branches above are restacked onto
// the parent first, while the popped branch is still checked out, and its
// pull request is queued to close at the next submit.
func (a *App) Pop(ctx context.Context, repo git.Repo) (PopResult, error) {
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return PopResult{}, err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		if errors.Is(err, git.ErrDetached) {
			return PopResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out the branch to pop first")
		}
		return PopResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return PopResult{}, err
	}
	s, idx, ok := graph.StackOf(current)
	if !ok {
		if graph.IsTrunk(current) {
			return PopResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; check out the branch to pop", current)
		}
		return PopResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", current).
			WithSteps("git stack init " + current + " to start tracking it")
	}
	if err := noMergedBranches(s); err != nil {
		return PopResult{}, err
	}
	parent, _ := graph.Parent(current)
	res := PopResult{Branch: current, Parent: parent, StackGone: len(s.Branches) == 1}
	local, err := a.localBranches(ctx, repo)
	if err != nil {
		return res, err
	}
	tip, parentTip := local[current].Head, local[parent].Head
	if !a.isAncestor(ctx, repo, parentTip, tip) || a.baseAbove(ctx, repo, s.Branches[idx].Base, parentTip, tip) {
		return res, stack.Newf(stack.KindInvalidArgs, "%s isn't on %s yet", current, parent).
			WithSteps("run git stack restack first")
	}
	changes, err := a.d.Git.LocalChanges(ctx, repo)
	if err != nil {
		return res, err
	}
	if len(changes) > 0 {
		return res, stack.Newf(stack.KindInvalidArgs, "pop needs a clean working tree; you have changes in %s", someNames(changes, 3)).
			WithSteps("commit or stash them first")
	}
	if wt := local[parent].Worktree; wt != "" && wt != repo.TopLevel {
		return res, stack.Newf(stack.KindInvalidArgs, "%s is checked out in %s, and popping %s needs to check it out here", parent, shortPath(wt), current).
			WithSteps("check out another branch there, then run " + popCommand + " again")
	}
	above := s.Names()[idx+1:]
	// The branch above keeps only its own commits: when it sits on the
	// popped branch, they start at the popped branch's tip.
	nextBase := ""
	if len(above) > 0 && a.isAncestor(ctx, repo, tip, local[above[0]].Head) {
		nextBase = tip
	}
	e := reshapeEdit{
		touched: []string{s.Bottom()},
		relink:  []string{s.Bottom()},
		dropped: []string{current},
		finish:  &reshapeFinish{Pop: current, Parent: parent},
		edit: func(g *stack.Graph) error {
			s, i, ok := g.StackOf(current)
			if !ok {
				return stack.Newf(stack.KindInvalidArgs, "%s left its stack while it was being popped", current)
			}
			if i+1 < len(s.Branches) && nextBase != "" {
				s.Branches[i+1].Base = nextBase
			}
			s.Branches = append(s.Branches[:i:i], s.Branches[i+1:]...)
			return nil
		},
	}
	if len(above) > 0 {
		e.segments = []restackSegment{{Parent: parent, Branches: above}}
	}
	if n := a.openPR(ctx, repo, s.Branches[idx]); n > 0 {
		e.closes = []pendingClose{{Number: n, Branch: current, Comment: popComment, Tip: tip}}
		res.Closing = n
	}
	headline := fmt.Sprintf("Popping %s; restacking %d %s onto %s", current, len(above), pluralise(len(above), "branch", "branches"), parent)
	rr, err := a.reshape(ctx, repo, popCommand, headline, e)
	res.Restacked = movedNames(rr)
	res.Notices = rr.Notices
	if err != nil {
		res.Closing = 0
	}
	return res, err
}

// openPR is the number of b's open or draft pull request, from the cached
// pull requests or else the stack metadata, or 0 when it has none.
func (a *App) openPR(ctx context.Context, repo git.Repo, b stack.Branch) int {
	prs, _ := a.loadPRs(ctx, repo, PRsCached)
	if pr, ok := PRsFor(prs)[b.Name]; ok {
		if pr.State == forge.StateOpen || pr.State == forge.StateDraft {
			return pr.Number
		}
		return 0
	}
	if b.PR != nil && !b.PR.Merged {
		return b.PR.Number
	}
	return 0
}

// finishPop points HEAD at the parent without touching the index or the
// working tree, so the popped branch's changes show as staged, then deletes
// the branch. Run again after it stopped part way, it only does what is
// left.
func (a *App) finishPop(ctx context.Context, repo git.Repo, f *reshapeFinish) error {
	cur, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil && !errors.Is(err, git.ErrDetached) {
		return err
	}
	switch cur {
	case f.Parent:
	case f.Pop:
		if err := a.d.Git.PointHEAD(ctx, repo, f.Parent); err != nil {
			return err
		}
	default:
		return stack.Newf(stack.KindInvalidArgs, "%s is no longer checked out, so its changes can't be staged on %s", f.Pop, f.Parent).
			WithSteps("git switch " + f.Pop)
	}
	exists, err := a.d.Git.BranchExists(ctx, repo, f.Pop)
	if err != nil || !exists {
		return err
	}
	return a.d.Git.DeleteBranch(ctx, repo, f.Pop)
}

// someNames lists up to n names ("x, y and z"), then how many more there
// are.
func someNames(names []string, n int) string {
	switch {
	case len(names) == 1:
		return names[0]
	case len(names) <= n:
		return joinNames(names[:len(names)-1]) + " and " + names[len(names)-1]
	default:
		return fmt.Sprintf("%s and %d more", joinNames(names[:n]), len(names)-n)
	}
}
