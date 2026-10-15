package app

import (
	"context"
	"errors"
	"slices"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// InitOptions adopts existing branches into a new stack.
type InitOptions struct {
	// Branches run bottom to top; the current branch when empty.
	Branches []string
	// Base is the trunk; when empty the graph's only trunk, else the
	// repository's default branch.
	Base string
}

// InitResult reports the stack that was recorded.
type InitResult struct {
	Trunk    string   `json:"trunk"`
	Branches []string `json:"branches"`
	// NeedsRestack lists the branches whose parent's tip is not in their
	// history; init records them as they are and leaves the rebase to
	// git stack restack.
	NeedsRestack []string `json:"needsRestack,omitempty"`
}

// Init records a new stack from branches that already exist. It changes
// no ref and checks nothing out.
func (a *App) Init(ctx context.Context, repo git.Repo, o InitOptions) (InitResult, error) {
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return InitResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return InitResult{}, err
	}
	branches := o.Branches
	if len(branches) == 0 {
		current, err := a.d.Git.CurrentBranch(ctx, repo)
		if err != nil {
			if errors.Is(err, git.ErrDetached) {
				return InitResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; name the branches to track")
			}
			return InitResult{}, err
		}
		if graph.IsTrunk(current) {
			return InitResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; name the branches to track", current).
				WithSteps("git stack init <branch>... (bottom to top)")
		}
		branches = []string{current}
	}
	trunk, err := a.initTrunk(ctx, repo, graph, o.Base)
	if err != nil {
		return InitResult{}, err
	}
	for i, name := range branches {
		if slices.Contains(branches[:i], name) {
			return InitResult{}, stack.Newf(stack.KindInvalidArgs, "%s is listed twice", name)
		}
		if name == trunk || graph.IsTrunk(name) {
			return InitResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk and can't be in a stack", name)
		}
		if s, _, ok := graph.StackOf(name); ok {
			return InitResult{}, stack.Newf(stack.KindInvalidArgs, "%s is already in the stack starting at %s", name, s.Bottom())
		}
		exists, err := a.d.Git.BranchExists(ctx, repo, name)
		if err != nil {
			return InitResult{}, err
		}
		if !exists {
			return InitResult{}, stack.Newf(stack.KindInvalidArgs, "no branch %s", name).
				WithSteps("git stack init only adopts branches that exist; git stack create makes new ones")
		}
	}
	local, err := a.localBranches(ctx, repo)
	if err != nil {
		return InitResult{}, err
	}
	res := InitResult{Trunk: trunk, Branches: branches}
	parent := trunk
	for _, name := range branches {
		if !a.isAncestor(ctx, repo, local[parent].Head, local[name].Head) {
			res.NeedsRestack = append(res.NeedsRestack, name)
		}
		parent = name
	}
	e := reshapeEdit{created: []string{branches[0]}, edit: func(g *stack.Graph) error {
		s := stack.Stack{Trunk: trunk}
		for _, name := range branches {
			s.Branches = append(s.Branches, stack.Branch{Name: name, Head: local[name].Head})
		}
		g.Stacks = append(g.Stacks, s)
		return nil
	}}
	if _, err := a.reshape(ctx, repo, "git stack init", "", e); err != nil {
		return InitResult{}, err
	}
	return res, nil
}

// initTrunk resolves the trunk for a new stack.
func (a *App) initTrunk(ctx context.Context, repo git.Repo, graph *stack.Graph, base string) (string, error) {
	switch {
	case base != "":
	case len(graph.Trunks) == 1:
		base = graph.Trunks[0]
	default:
		def, ok, err := a.d.Git.DefaultBranch(ctx, repo)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", stack.New(stack.KindInvalidArgs, "can't tell which branch is the trunk").
				WithSteps("pass --base <trunk>")
		}
		base = def
	}
	exists, err := a.d.Git.BranchExists(ctx, repo, base)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", stack.Newf(stack.KindInvalidArgs, "no branch %s to use as the trunk", base)
	}
	return base, nil
}
