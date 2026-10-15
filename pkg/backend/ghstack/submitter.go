package ghstack

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

var (
	_ stack.Submitter = (*Backend)(nil)
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

// Link runs `gh stack link` with pull request numbers. Numbers resolve to
// pull requests rather than branches, so gh stack pushes nothing.
func (b *Backend) Link(ctx context.Context, repo git.Repo, o stack.LinkOptions) (stack.SubmitResult, error) {
	if err := b.Preflight(ctx); err != nil {
		return stack.SubmitResult{}, err
	}
	args := []string{"link"}
	if o.Stack > 0 {
		args = append(args, strconv.Itoa(o.Stack))
	} else if o.Trunk != "" {
		args = append(args, "--base", o.Trunk)
	}
	for _, n := range o.PRs {
		args = append(args, strconv.Itoa(n))
	}
	res, err := b.ghRun(ctx, repo, b.out, args...)
	return stack.SubmitResult{Output: res.Err(), Streamed: b.out != nil}, err
}

// Unstack runs `gh stack unstack <number>`. With the number untracked
// locally gh stack only talks to GitHub.
func (b *Backend) Unstack(ctx context.Context, repo git.Repo, number int) error {
	if err := b.Preflight(ctx); err != nil {
		return err
	}
	err := b.gh(ctx, repo, "unstack", strconv.Itoa(number))
	// Exit 2 also covers "not a repository" and "not in a stack", so only a
	// message saying the stack was not found means it is already gone.
	if se, ok := errors.AsType[*stack.Error](err); ok && se.Kind == stack.KindNotInStack &&
		(strings.Contains(se.Detail, "not found") || strings.Contains(se.Msg, "not found")) {
		return nil // already gone on GitHub
	}
	return err
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
