package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// ReorderOptions is one reorder of the current stack.
type ReorderOptions struct {
	// Order is the new order, bottom to top; every branch of the stack
	// exactly once. Empty asks Pick.
	Order []string
	// Pick, when set and Order is empty, chooses the order (the CLI's
	// list). It returns nil when the user cancelled.
	Pick func(trunk string, branches []string, current string) ([]string, error)
}

// ReorderResult reports what happened.
type ReorderResult struct {
	// Stack is the stack's bottom branch before the reorder.
	Stack  string   `json:"stack"`
	Before []string `json:"before"`
	After  []string `json:"after"`
	// Changed is false when the order was already the one asked for.
	Changed bool `json:"changed"`
	// Cancelled is true when Pick returned nothing.
	Cancelled bool `json:"cancelled,omitempty"`
	// Restacked lists the branches that were rebased, bottom to top.
	Restacked []string `json:"restacked,omitempty"`
	Notices   []string `json:"notices,omitempty"`
}

const reorderCommand = "git stack reorder"

// Reorder changes the order of the branches in the current stack and
// restacks it. Nothing is checked out.
func (a *App) Reorder(ctx context.Context, repo git.Repo, o ReorderOptions) (ReorderResult, error) {
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return ReorderResult{}, err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		if errors.Is(err, git.ErrDetached) {
			return ReorderResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch first")
		}
		return ReorderResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return ReorderResult{}, err
	}
	s, _, ok := graph.StackOf(current)
	if !ok {
		if graph.IsTrunk(current) {
			return ReorderResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; check out a branch of the stack to reorder", current)
		}
		return ReorderResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", current).
			WithSteps("git stack init " + current + " to start tracking it")
	}
	if err := noMergedBranches(s); err != nil {
		return ReorderResult{}, err
	}
	names := s.Names()
	res := ReorderResult{Stack: s.Bottom(), Before: names}
	if len(names) < 2 {
		return res, stack.Newf(stack.KindInvalidArgs, "the stack has only %s; nothing to reorder", names[0])
	}
	order := o.Order
	if len(order) == 0 {
		if o.Pick == nil {
			return res, stack.New(stack.KindInteractionRequired, "no order given and no terminal to pick one").
				WithSteps("pass the new order as arguments, bottom to top: " + reorderCommand + " <branch>...")
		}
		order, err = o.Pick(s.Trunk, names, current)
		if err != nil {
			return res, err
		}
		if order == nil {
			res.Cancelled = true
			res.After = names
			return res, nil
		}
	}
	if err := validateOrder(names, order); err != nil {
		return res, err
	}
	res.After = slices.Clone(order)
	if slices.Equal(order, names) {
		return res, nil
	}
	res.Changed = true
	// Branches below the first change are untouched; restack from there.
	first := 0
	for first < len(names) && names[first] == order[first] {
		first++
	}
	parent := s.Trunk
	if first > 0 {
		parent = order[first-1]
	}
	bottom := s.Bottom()
	e := reshapeEdit{
		touched:  []string{bottom},
		relink:   []string{bottom},
		segments: []restackSegment{{Parent: parent, Branches: order[first:]}},
		edit: func(g *stack.Graph) error {
			s, _, ok := g.StackOf(bottom)
			if !ok {
				return stack.Newf(stack.KindInvalidArgs, "%s is no longer in a stack", bottom)
			}
			if len(s.Branches) != len(order) {
				return stack.New(stack.KindInvalidArgs, "the stack changed while it was being reordered; try again")
			}
			byName := map[string]stack.Branch{}
			for _, b := range s.Branches {
				byName[b.Name] = b
			}
			out := make([]stack.Branch, 0, len(order))
			for _, n := range order {
				b, ok := byName[n]
				if !ok {
					return stack.Newf(stack.KindInvalidArgs, "%s left the stack while it was being reordered", n)
				}
				out = append(out, b)
			}
			s.Branches = out
			return nil
		},
	}
	n := len(order) - first
	rr, err := a.reshape(ctx, repo, reorderCommand, fmt.Sprintf("Reordering the stack; restacking %d %s", n, pluralise(n, "branch", "branches")), e)
	res.Restacked = movedNames(rr)
	res.Notices = rr.Notices
	return res, err
}

// validateOrder checks that want is have in some order: every branch once.
func validateOrder(have, want []string) error {
	seen := map[string]bool{}
	for _, n := range want {
		if !slices.Contains(have, n) {
			return stack.Newf(stack.KindInvalidArgs, "%s is not in this stack", n).
				WithSteps("the stack is: " + joinNames(have))
		}
		if seen[n] {
			return stack.Newf(stack.KindInvalidArgs, "%s is listed twice", n)
		}
		seen[n] = true
	}
	var missing []string
	for _, n := range have {
		if !seen[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return stack.Newf(stack.KindInvalidArgs, "the order leaves out %s", joinNames(missing)).
			WithSteps("list every branch of the stack, bottom to top")
	}
	return nil
}

// noMergedBranches refuses to reshape a stack with a merged pull request
// in it: sync removes those, and they would have to stay put anyway.
func noMergedBranches(s *stack.Stack) error {
	for _, b := range s.Branches {
		if b.Merged() {
			return stack.Newf(stack.KindInvalidArgs, "%s has merged; run git stack sync first", b.Name)
		}
	}
	return nil
}
