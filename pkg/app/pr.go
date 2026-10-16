package app

import (
	"context"
	"errors"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// BranchPR finds the pull request of a stacked branch (the current one when
// branch is ""). Local state answers first, the PR cache then gh stack's
// metadata, the same order View applies them in; the forge is only asked
// when neither knows the branch's PR, i.e. one opened outside git-stack or
// since the cache was last filled. The result always has a URL.
func (a *App) BranchPR(ctx context.Context, repo git.Repo, branch string) (forge.PullRequest, error) {
	if branch == "" {
		current, err := a.d.Git.CurrentBranch(ctx, repo)
		if err != nil {
			if errors.Is(err, git.ErrDetached) {
				return forge.PullRequest{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; name a stacked branch").
					WithSteps("git stack pr <branch>")
			}
			return forge.PullRequest{}, err
		}
		branch = current
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return forge.PullRequest{}, err
	}
	s, idx, ok := graph.StackOf(branch)
	if !ok {
		if graph.IsTrunk(branch) {
			return forge.PullRequest{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; pull requests belong to the branches stacked on it", branch)
		}
		local, err := a.localBranches(ctx, repo)
		if err != nil {
			return forge.PullRequest{}, err
		}
		if _, ok := local[branch]; !ok {
			return forge.PullRequest{}, stack.Newf(stack.KindNotInStack, "there's no branch called %s", branch)
		}
		return forge.PullRequest{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", branch).
			WithSteps("git stack init " + branch + " to start tracking it")
	}

	cached, _, _ := a.CachedPRs(repo)
	pr, ok := PRsFor(cached)[branch]
	if !ok {
		if ref := s.Branches[idx].PR; ref != nil {
			pr, ok = forge.PullRequest{Number: ref.Number, URL: ref.URL, Head: branch}, true
		}
	}
	if !ok {
		if a.d.Forge == nil {
			return forge.PullRequest{}, ErrNoForge
		}
		var fresh []forge.PullRequest
		if err := a.progress(ctx, PhaseRefresh, "Fetching pull requests", func(ctx context.Context) error {
			fresh, err = a.RefreshPRs(ctx, repo)
			return err
		}); err != nil {
			return forge.PullRequest{}, err
		}
		if pr, ok = PRsFor(fresh)[branch]; !ok {
			return forge.PullRequest{}, stack.Newf(stack.KindInvalidArgs, "%s has no pull request yet", branch).
				WithSteps("git stack submit")
		}
	}
	if pr.URL == "" {
		pr.URL = a.PRURLs(ctx, repo)(pr.Number)
	}
	if pr.URL == "" {
		return forge.PullRequest{}, stack.Newf(stack.KindUnsupported, "can't work out the address of #%d (%s)", pr.Number, branch)
	}
	return pr, nil
}
