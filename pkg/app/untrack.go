package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// UntrackOptions stops tracking a branch.
type UntrackOptions struct {
	// Branch is the branch to untrack; the current branch when empty.
	Branch string
	// Only untracks Branch alone: the branches above it stay in the stack,
	// now on Branch's parent, and are restacked there.
	Only bool
}

// UntrackResult reports what happened.
type UntrackResult struct {
	Branch string `json:"branch"`
	// Untracked are the branches no longer in a stack, bottom to top
	// (Branch first).
	Untracked []string `json:"untracked"`
	// Parent is where the branches above Branch sit now, with Only and
	// branches above it.
	Parent    string   `json:"parent,omitempty"`
	Restacked []string `json:"restacked,omitempty"`
	// NextSteps say how to track the branches again.
	NextSteps []string `json:"nextSteps,omitempty"`
	Notices   []string `json:"notices,omitempty"`
}

const untrackCommand = "git stack untrack"

// Untrack takes a branch, and unless Only everything above it, out of its
// stack without touching the branches themselves: a stack is a single line,
// so it can't keep a gap. With Only the branches above close the gap and
// are restacked onto the untracked branch's parent, dropping its commits.
// A stack GitHub knows is queued to be dissolved at the next submit; no
// pull request is closed.
func (a *App) Untrack(ctx context.Context, repo git.Repo, o UntrackOptions) (UntrackResult, error) {
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return UntrackResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return UntrackResult{}, err
	}
	name := o.Branch
	if name == "" {
		name, err = a.d.Git.CurrentBranch(ctx, repo)
		if err != nil {
			if errors.Is(err, git.ErrDetached) {
				return UntrackResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch or name one")
			}
			return UntrackResult{}, err
		}
	}
	s, idx, ok := graph.StackOf(name)
	if !ok {
		// With no stacks left on it the graph no longer knows the trunk;
		// the repository's default branch is one all the same.
		def, _, _ := a.d.Git.DefaultBranch(ctx, repo)
		if graph.IsTrunk(name) || name == def {
			return UntrackResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; untrack works on stacked branches", name)
		}
		exists, err := a.d.Git.BranchExists(ctx, repo, name)
		if err != nil {
			return UntrackResult{}, err
		}
		if !exists {
			return UntrackResult{}, stack.Newf(stack.KindInvalidArgs, "no branch %s", name)
		}
		return UntrackResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", name)
	}
	if err := noMergedBranches(s); err != nil {
		return UntrackResult{}, err
	}
	end := len(s.Branches)
	if o.Only {
		end = idx + 1
	}
	parent, _ := graph.Parent(name)
	above := slices.Clone(s.Names()[end:])
	res := UntrackResult{Branch: name, Untracked: slices.Clone(s.Names()[idx:end])}
	res.NextSteps = []string{"git stack init " + strings.Join(res.Untracked, " ")}
	bottom := s.Bottom()
	e := reshapeEdit{
		touched: []string{bottom},
		relink:  []string{bottom},
		dropped: res.Untracked,
		edit: func(g *stack.Graph) error {
			s, i, ok := g.StackOf(name)
			if !ok {
				return stack.Newf(stack.KindInvalidArgs, "%s is no longer in a stack", name)
			}
			end := len(s.Branches)
			if o.Only {
				end = i + 1
			}
			s.Branches = slices.Delete(s.Branches, i, end)
			return nil
		},
	}
	if len(above) > 0 {
		res.Parent = parent
		e.segments = []restackSegment{{Parent: parent, Branches: above}}
	}
	headline := fmt.Sprintf("Restacking %s onto %s", joinNames(above), parent)
	rr, err := a.reshape(ctx, repo, untrackCommand, headline, e)
	res.Restacked = movedNames(rr)
	res.Notices = rr.Notices
	return res, err
}
