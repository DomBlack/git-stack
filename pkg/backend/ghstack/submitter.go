package ghstack

import (
	"context"
	"errors"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

var (
	_ stack.Submitter = (*Backend)(nil)
	_ stack.Syncer    = (*Backend)(nil)
)

// Submit runs `gh stack submit`. Non-interactive runs use --auto and are
// captured; interactive runs hand gh-stack the real terminal so its own
// PR editor can open.
func (b *Backend) Submit(ctx context.Context, repo git.Repo, o stack.SubmitOptions) (stack.SubmitResult, error) {
	if err := b.Preflight(ctx); err != nil {
		return stack.SubmitResult{}, err
	}
	args := []string{"submit"}
	if o.Publish {
		args = append(args, "--open")
	}
	if o.Interactive {
		return stack.SubmitResult{}, b.ghTTY(ctx, repo, args...)
	}
	args = append(args, "--auto")
	res, err := b.ghRun(ctx, repo, b.out, args...)
	return stack.SubmitResult{Output: res.Err(), Streamed: b.out != nil}, err
}

// Sync runs `gh stack sync [--prune]`.
func (b *Backend) Sync(ctx context.Context, repo git.Repo, o stack.SyncOptions) (stack.SyncResult, error) {
	if err := b.Preflight(ctx); err != nil {
		return stack.SyncResult{}, err
	}
	args := []string{"sync"}
	if o.Prune {
		args = append(args, "--prune")
	}
	if o.Dir != "" {
		// gh stack syncs the stack of whatever is checked out where it runs.
		repo.TopLevel = o.Dir
	}
	res, err := b.ghRun(ctx, repo, b.out, args...)
	return stack.SyncResult{Output: res.Err(), Streamed: b.out != nil}, err
}

// ghTTY runs `gh stack <args>` with the terminal attached. Only the CLI's
// runner can do this; the MCP runner returns ErrTTYUnavailable, which is
// mapped to an interaction error.
func (b *Backend) ghTTY(ctx context.Context, repo git.Repo, args ...string) error {
	_, err := b.run.Run(ctx, exec.Cmd{
		Name:  "gh",
		Args:  append([]string{"stack"}, args...),
		Dir:   repo.TopLevel,
		Env:   []string{"GH_NO_UPDATE_NOTIFIER=1"},
		Mode:  exec.Passthrough,
		Unset: []string{"GH_FORCE_TTY"},
	})
	if err != nil {
		if errors.Is(err, exec.ErrTTYUnavailable) {
			return stack.New(stack.KindInteractionRequired, "gh stack's editor needs a terminal").
				WithSteps("pass --no-edit to submit without editing", "or use --ai / provide titles and bodies")
		}
		return mapError(args, err)
	}
	return nil
}
