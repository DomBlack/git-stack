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
	_, err := b.gh(ctx, repo, args...)
	return err
}

// AddTop runs `gh stack add <name>` without committing (we commit natively).
func (b *Backend) AddTop(ctx context.Context, repo git.Repo, name string) error {
	if err := b.Preflight(ctx); err != nil {
		return err
	}
	_, err := b.gh(ctx, repo, "add", name)
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
