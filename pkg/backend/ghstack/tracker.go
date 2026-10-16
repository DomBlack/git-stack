package ghstack

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

var _ stack.Tracker = (*Backend)(nil)

// InitStack runs `gh stack init --base <trunk> <branches...>`.
func (b *Backend) InitStack(ctx context.Context, repo git.Repo, trunk string, branches []string) error {
	if err := b.Preflight(ctx); err != nil {
		return err
	}
	args := append([]string{"init", "--base", trunk}, branches...)
	return b.gh(ctx, repo, args...)
}

// AddTop runs `gh stack add <name>` without committing (we commit natively),
// once the current branch's stack is in this worktree's file where gh stack
// will look for it.
func (b *Backend) AddTop(ctx context.Context, repo git.Repo, name string) error {
	if err := b.Preflight(ctx); err != nil {
		return err
	}
	if err := b.adoptCurrent(ctx, repo); err != nil {
		return err
	}
	err := b.gh(ctx, repo, "add", name)
	if err != nil {
		if se, ok := errors.AsType[*stack.Error](err); ok && se.Kind == stack.KindInvalidArgs &&
			strings.Contains(se.Detail, "top of the stack") {
			return stack.New(stack.KindNotAtTop, "gh stack can only add branches at the top of the stack").
				WithSteps("run `git stack top` and create the branch there",
					"or restructure the stack with `gh stack modify` (interactive)").
				WithDetail(se.Detail).WithCause(se.Cause)
		}
		return err
	}
	return nil
}

// adoptCurrent adopts the stack of the branch checked out here, if any.
// When every stack is already in this worktree's file there is nothing to
// move, so git isn't asked which branch that is.
func (b *Backend) adoptCurrent(ctx context.Context, repo git.Repo) error {
	r, err := b.read(repo)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(r.where, func(w recRef) bool { return r.files[w.file].src.gitDir != repo.GitDir }) {
		return nil
	}
	branch, err := b.git.CurrentBranch(ctx, repo)
	if errors.Is(err, git.ErrDetached) {
		return nil
	}
	if err != nil {
		return err
	}
	return b.adopt(ctx, repo, branch)
}
