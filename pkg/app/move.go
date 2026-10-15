package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// MoveOptions is one move of a branch to a new parent.
type MoveOptions struct {
	// Source is the branch to move; the current branch when empty.
	Source string
	// Onto is the new parent: a trunk or the top of another stack. Empty
	// asks Pick.
	Onto string
	// Only moves Source alone, leaving the branches above it where they
	// are (on Source's old parent).
	Only bool
	// Pick, when set and Onto is empty, chooses the destination from the
	// valid ones. It returns "" when the user cancelled.
	Pick func(dests []MoveDestination) (string, error)
}

// MoveDestination is a place a branch can be moved to.
type MoveDestination struct {
	Name    string `json:"name"`
	Trunk   string `json:"trunk"`
	IsTrunk bool   `json:"isTrunk"`
	// Stack is the bottom branch of the stack Name tops (empty for a trunk).
	Stack string `json:"stack,omitempty"`
}

// MoveResult reports what happened.
type MoveResult struct {
	Branch string `json:"branch"`
	// From is the parent the branch was on, Onto the one it is on now.
	From string `json:"from"`
	Onto string `json:"onto"`
	// Moved are the branches that went, bottom to top (Branch first).
	Moved []string `json:"moved"`
	// LeftBehind are the branches above Branch that stayed, with --only.
	LeftBehind []string `json:"leftBehind,omitempty"`
	// NewStack is true when Onto is a trunk and the branches started a new stack.
	NewStack  bool     `json:"newStack"`
	Cancelled bool     `json:"cancelled,omitempty"`
	Restacked []string `json:"restacked,omitempty"`
	Notices   []string `json:"notices,omitempty"`
}

const moveCommand = "git stack move"

// MoveDestinations lists where source could go: every trunk, then the top
// of every stack other than source's own, in graph order.
func (a *App) MoveDestinations(graph *stack.Graph, source string) []MoveDestination {
	own, _, _ := graph.StackOf(source)
	var out []MoveDestination
	for _, trunk := range graph.Trunks {
		out = append(out, MoveDestination{Name: trunk, Trunk: trunk, IsTrunk: true})
		for _, s := range graph.StacksOn(trunk) {
			if s == own || len(s.Branches) == 0 {
				continue
			}
			out = append(out, MoveDestination{Name: s.Top(), Trunk: trunk, Stack: s.Bottom()})
		}
	}
	return out
}

// Move puts a branch (and, unless Only, everything above it) on top of
// another stack or on a trunk as a new stack, then restacks what moved and
// what was left behind. Nothing is checked out.
func (a *App) Move(ctx context.Context, repo git.Repo, o MoveOptions) (MoveResult, error) {
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return MoveResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return MoveResult{}, err
	}
	source := o.Source
	if source == "" {
		source, err = a.d.Git.CurrentBranch(ctx, repo)
		if err != nil {
			if errors.Is(err, git.ErrDetached) {
				return MoveResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch or pass --source")
			}
			return MoveResult{}, err
		}
	}
	src, idx, ok := graph.StackOf(source)
	if !ok {
		if graph.IsTrunk(source) {
			return MoveResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; move works on stacked branches", source)
		}
		return MoveResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", source).
			WithSteps("git stack init " + source + " to start tracking it")
	}
	oldParent, _ := graph.Parent(source)
	res := MoveResult{Branch: source, From: oldParent}
	onto := o.Onto
	if onto == "" {
		if o.Pick == nil {
			return res, stack.New(stack.KindInteractionRequired, "no destination given and no terminal to pick one").
				WithSteps(moveCommand + " --onto <branch>")
		}
		onto, err = o.Pick(a.MoveDestinations(graph, source))
		if err != nil {
			return res, err
		}
		if onto == "" {
			res.Cancelled = true
			return res, nil
		}
	}
	res.Onto = onto
	if onto == source {
		return res, stack.Newf(stack.KindInvalidArgs, "%s can't be moved onto itself", source)
	}
	if onto == oldParent {
		return res, stack.Newf(stack.KindInvalidArgs, "%s is already on %s", source, onto)
	}
	var dst *stack.Stack
	switch ds, di, ok := graph.StackOf(onto); {
	case graph.IsTrunk(onto):
		if idx == 0 && src.Trunk == onto {
			return res, stack.Newf(stack.KindInvalidArgs, "%s is already on %s", source, onto)
		}
	case ok && ds == src:
		return res, stack.Newf(stack.KindInvalidArgs, "%s is in the same stack as %s; use git stack reorder to change the order", onto, source)
	case ok && di != len(ds.Branches)-1:
		return res, stack.Newf(stack.KindInvalidArgs, "gh stack keeps a stack as a single line, so a branch can only go on top of one; %s is above %s", ds.Branches[di+1].Name, onto).
			WithSteps("move onto "+ds.Top()+" instead", "or reorder that stack first")
	case ok:
		dst = ds
	default:
		exists, err := a.d.Git.BranchExists(ctx, repo, onto)
		if err != nil {
			return res, err
		}
		if !exists {
			return res, stack.Newf(stack.KindInvalidArgs, "no branch %s", onto)
		}
		return res, stack.Newf(stack.KindNotInStack, "%s is not in a stack", onto).
			WithSteps("git stack init " + onto + " first")
	}
	if err := noMergedBranches(src); err != nil {
		return res, err
	}
	if dst != nil {
		if err := noMergedBranches(dst); err != nil {
			return res, err
		}
	}
	end := len(src.Branches)
	if o.Only {
		end = idx + 1
	}
	moving := slices.Clone(src.Names()[idx:end])
	left := slices.Clone(src.Names()[end:])
	res.Moved, res.LeftBehind, res.NewStack = moving, left, dst == nil
	srcBottom := src.Bottom()
	touched := []string{srcBottom}
	if dst != nil {
		touched = append(touched, dst.Bottom())
	}
	e := reshapeEdit{
		touched:  touched,
		relink:   []string{srcBottom},
		segments: []restackSegment{{Parent: onto, Branches: moving}},
	}
	if len(left) > 0 {
		e.segments = append(e.segments, restackSegment{Parent: oldParent, Branches: left})
	}
	if dst == nil {
		e.created = []string{moving[0]}
	}
	e.edit = func(g *stack.Graph) error {
		s, i, ok := g.StackOf(source)
		if !ok {
			return stack.Newf(stack.KindInvalidArgs, "%s is no longer in a stack", source)
		}
		end := len(s.Branches)
		if o.Only {
			end = i + 1
		}
		taken := slices.Clone(s.Branches[i:end])
		s.Branches = slices.Concat(s.Branches[:i], s.Branches[end:])
		if dst == nil {
			g.Stacks = append(g.Stacks, stack.Stack{Trunk: onto, Branches: taken})
			return nil
		}
		d, _, ok := g.StackOf(onto)
		if !ok {
			return stack.Newf(stack.KindInvalidArgs, "%s is no longer in a stack", onto)
		}
		d.Branches = append(d.Branches, taken...)
		return nil
	}
	headline := fmt.Sprintf("Moving %s onto %s", joinNames(moving), onto)
	rr, err := a.reshape(ctx, repo, moveCommand, headline, e)
	res.Restacked = movedNames(rr)
	res.Notices = rr.Notices
	return res, err
}
