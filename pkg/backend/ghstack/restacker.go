package ghstack

import (
	"context"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

var _ stack.Restacker = (*Backend)(nil)

// Restack runs the local-only form of `gh stack rebase`: no fetch and no
// trunk update. The bottom branch is never rebased onto trunk in this mode.
func (b *Backend) Restack(ctx context.Context, repo git.Repo, scope stack.Scope) error {
	if err := b.Preflight(ctx); err != nil {
		return err
	}
	args := []string{"rebase", "--no-trunk"}
	switch scope {
	case stack.ScopeAll:
	case stack.ScopeUpstack:
		args = append(args, "--upstack")
	case stack.ScopeDownstack:
		args = append(args, "--downstack")
	case stack.ScopeOnly:
		return stack.New(stack.KindUnsupported, "gh stack cannot rebase a single branch").
			WithSteps("use --upstack (this branch and everything above) or --downstack")
	}
	_, err := b.gh(ctx, repo, args...)
	return err
}

// Continue resumes an interrupted rebase.
func (b *Backend) Continue(ctx context.Context, repo git.Repo) error {
	if err := b.Preflight(ctx); err != nil {
		return err
	}
	_, err := b.gh(ctx, repo, "rebase", "--continue")
	return err
}

// Abort abandons an interrupted rebase and restores every branch.
func (b *Backend) Abort(ctx context.Context, repo git.Repo) error {
	if err := b.Preflight(ctx); err != nil {
		return err
	}
	_, err := b.gh(ctx, repo, "rebase", "--abort")
	return err
}
