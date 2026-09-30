package app

import (
	"context"
	"errors"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Navigate moves along the current stack (up/down/top/bottom) and checks out
// the destination. The pure computation lives in stack.Navigate.
func (a *App) Navigate(ctx context.Context, repo git.Repo, req stack.NavRequest) (stack.NavResult, error) {
	if req.From == "" {
		current, err := a.d.Git.CurrentBranch(ctx, repo)
		if err != nil {
			if errors.Is(err, git.ErrDetached) {
				return stack.NavResult{}, stack.New(stack.KindNotInStack, "HEAD is detached").
					WithSteps("check out a stacked branch first: git stack checkout <branch>")
			}
			return stack.NavResult{}, err
		}
		req.From = current
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return stack.NavResult{}, err
	}
	res, err := stack.Navigate(graph, req)
	if err != nil {
		return stack.NavResult{}, err
	}
	if res.Moved == 0 {
		return res, nil
	}
	if err := a.Checkout(ctx, repo, res.Target); err != nil {
		return stack.NavResult{}, err
	}
	return res, nil
}

// Checkout switches to a local branch, explaining worktree conflicts.
func (a *App) Checkout(ctx context.Context, repo git.Repo, name string) error {
	branches, err := a.d.Git.Branches(ctx, repo)
	if err != nil {
		return err
	}
	var found *git.Branch
	for i := range branches {
		if branches[i].Name == name {
			found = &branches[i]
			break
		}
	}
	if found == nil {
		return stack.Newf(stack.KindInvalidArgs, "no local branch named %q", name).
			WithSteps("run `git stack checkout` to pick from the stack tree")
	}
	if found.Worktree != "" && found.Worktree != repo.TopLevel {
		return stack.Newf(stack.KindInvalidArgs, "%s is checked out in another worktree: %s", name, found.Worktree).
			WithSteps("cd " + found.Worktree)
	}
	return a.d.Git.Switch(ctx, repo, name)
}

// Trunk returns the trunk to use from the current position: the trunk of the
// current stack, the current branch if it is a trunk, or the only trunk.
func (a *App) Trunk(ctx context.Context, repo git.Repo) (string, error) {
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return "", err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil && !errors.Is(err, git.ErrDetached) {
		return "", err
	}
	if graph.IsTrunk(current) {
		return current, nil
	}
	if s, _, ok := graph.StackOf(current); ok {
		return s.Trunk, nil
	}
	switch len(graph.Trunks) {
	case 0:
		return "", stack.New(stack.KindNotInStack, "no stacks in this repository").
			WithSteps("run `git stack create` from your trunk branch to start one")
	case 1:
		return graph.Trunks[0], nil
	default:
		return "", stack.Newf(stack.KindDisambiguate, "several trunks are in use (%v)", graph.Trunks).
			WithSteps("check out a stacked branch first, or name the trunk explicitly")
	}
}
