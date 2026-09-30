// Package git is a typed wrapper around the git command line. It shells out
// through pkg/exec; go-git is deliberately not used so behaviour matches the
// user's git exactly (hooks, config, worktrees, signing).
package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Client runs git commands.
type Client struct {
	run exec.Runner
}

// New returns a Client that runs git through r.
func New(r exec.Runner) *Client {
	return &Client{run: r}
}

// Runner exposes the underlying runner (used by adapters that run other
// tools with the same debug/TTY settings).
func (c *Client) Runner() exec.Runner { return c.run }

// Repo locates a repository. All paths are absolute.
type Repo struct {
	// TopLevel is the working tree root.
	TopLevel string
	// GitDir is the repository directory for this worktree
	// (e.g. .git or .git/worktrees/<name>).
	GitDir string
	// CommonDir is the shared repository directory (.git of the main worktree).
	CommonDir string
}

// IsLinkedWorktree reports whether the repo is a linked worktree.
func (r Repo) IsLinkedWorktree() bool { return r.GitDir != r.CommonDir }

// ErrNotRepo is returned by Discover outside a git repository.
var ErrNotRepo = errors.New("not a git repository")

// ErrDetached is returned when HEAD is not on a branch.
var ErrDetached = errors.New("HEAD is detached")

// baseEnv keeps git output parseable and non-interactive.
var baseEnv = []string{"GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}

func (c *Client) git(ctx context.Context, dir string, args ...string) (exec.Result, error) {
	return c.run.Run(ctx, exec.Cmd{Name: "git", Args: args, Dir: dir, Env: baseEnv})
}

// gitIn runs git inside the repo's working tree.
func (c *Client) gitIn(ctx context.Context, repo Repo, args ...string) (exec.Result, error) {
	return c.git(ctx, repo.TopLevel, args...)
}

// Passthrough runs git with the terminal attached (editors, `add -p`).
func (c *Client) Passthrough(ctx context.Context, repo Repo, args ...string) error {
	_, err := c.run.Run(ctx, exec.Cmd{Name: "git", Args: args, Dir: repo.TopLevel, Mode: exec.Passthrough})
	return err
}

// Discover resolves the repository containing dir.
func (c *Client) Discover(ctx context.Context, dir string) (Repo, error) {
	res, err := c.git(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 128 {
			return Repo{}, fmt.Errorf("%w: %s", ErrNotRepo, dir)
		}
		return Repo{}, err
	}
	lines := strings.Split(res.Out(), "\n")
	if len(lines) != 3 {
		return Repo{}, fmt.Errorf("git rev-parse: unexpected output %q", res.Out())
	}
	return Repo{TopLevel: lines[0], GitDir: lines[1], CommonDir: lines[2]}, nil
}

// CurrentBranch returns the checked-out branch name.
func (c *Client) CurrentBranch(ctx context.Context, repo Repo) (string, error) {
	res, err := c.gitIn(ctx, repo, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 1 {
			return "", ErrDetached
		}
		return "", err
	}
	return res.Out(), nil
}

// RevParse resolves rev to a full object id.
func (c *Client) RevParse(ctx context.Context, repo Repo, rev string) (string, error) {
	res, err := c.gitIn(ctx, repo, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", rev, err)
	}
	return res.Out(), nil
}

// IsAncestor reports whether ancestor is reachable from descendant.
func (c *Client) IsAncestor(ctx context.Context, repo Repo, ancestor, descendant string) (bool, error) {
	_, err := c.gitIn(ctx, repo, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CountCommits returns the number of commits in from..to.
func (c *Client) CountCommits(ctx context.Context, repo Repo, from, to string) (int, error) {
	res, err := c.gitIn(ctx, repo, "rev-list", "--count", from+".."+to)
	if err != nil {
		return 0, err
	}
	var n int
	if _, err := fmt.Sscan(res.Out(), &n); err != nil {
		return 0, fmt.Errorf("rev-list --count: unexpected output %q", res.Out())
	}
	return n, nil
}

// CheckRefFormat validates name as a branch name.
func (c *Client) CheckRefFormat(ctx context.Context, name string) error {
	_, err := c.git(ctx, "", "check-ref-format", "--branch", name)
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("%q is not a valid branch name", name)
		}
		return err
	}
	return nil
}

// ListCmds returns `git --list-cmds=<spec>` (e.g. "builtins" or "main,others").
func (c *Client) ListCmds(ctx context.Context, spec string) ([]string, error) {
	res, err := c.git(ctx, "", "--list-cmds="+spec)
	if err != nil {
		return nil, err
	}
	return strings.Fields(res.Out()), nil
}
