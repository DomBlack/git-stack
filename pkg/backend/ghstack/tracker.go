package ghstack

import (
	"context"
	"errors"
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

// AddTop runs `gh stack add <name>` without committing (we commit natively).
func (b *Backend) AddTop(ctx context.Context, repo git.Repo, name string) error {
	if err := b.Preflight(ctx); err != nil {
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

// ImportStack runs `gh stack checkout <target>`. It only ever gets a target
// git-stack doesn't already track, so gh stack's own local lookups miss and
// it asks GitHub for the stack.
func (b *Backend) ImportStack(ctx context.Context, repo git.Repo, target string) error {
	if err := b.Preflight(ctx); err != nil {
		return err
	}
	err := b.gh(ctx, repo, "checkout", target)
	se, ok := errors.AsType[*stack.Error](err)
	if !ok {
		return err
	}
	switch se.Kind {
	case stack.KindNotInStack:
		return stack.Newf(stack.KindNotInStack, "%s is not in a stack on GitHub", target).
			WithDetail(se.Detail).WithCause(se.Cause)
	case stack.KindConflict:
		// Exit 3 from checkout is a local stack holding some of the
		// branches in a different order, not a rebase.
		return stack.New(stack.KindInvalidArgs, "a local stack holds some of these branches in a different order than GitHub").
			WithSteps("git stack untrack the local branches, then run git stack get again").
			WithDetail(se.Detail).WithCause(se.Cause)
	}
	return err
}
