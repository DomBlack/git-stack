package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Branch describes a local branch.
type Branch struct {
	Name string
	// Head is the tip commit id.
	Head string
	// CommitTime is the committer date of the tip.
	CommitTime time.Time
	// Worktree is the path of the worktree the branch is checked out in, or
	// empty.
	Worktree string
	// Upstream is the tracking branch (e.g. origin/feat), or empty.
	Upstream string
}

// Branches lists local branches sorted by name.
func (c *Client) Branches(ctx context.Context, repo Repo) ([]Branch, error) {
	const format = "%(refname:short)%00%(objectname)%00%(committerdate:unix)%00%(worktreepath)%00%(upstream:short)"
	res, err := c.gitIn(ctx, repo, "for-each-ref", "--format="+format, "--sort=refname", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var out []Branch
	for line := range strings.SplitSeq(res.Out(), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x00")
		if len(f) != 5 {
			return nil, fmt.Errorf("for-each-ref: unexpected line %q", line)
		}
		b := Branch{Name: f[0], Head: f[1], Worktree: f[3], Upstream: f[4]}
		if secs, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			b.CommitTime = time.Unix(secs, 0)
		}
		out = append(out, b)
	}
	return out, nil
}

// BranchExists reports whether a local branch exists.
func (c *Client) BranchExists(ctx context.Context, repo Repo, name string) (bool, error) {
	_, err := c.gitIn(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Switch checks out an existing branch.
func (c *Client) Switch(ctx context.Context, repo Repo, name string) error {
	_, err := c.gitIn(ctx, repo, "switch", "--no-guess", name)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("switch to %q: %s", name, ee.Result.Err())
		}
		return err
	}
	return nil
}

// CreateBranch creates name at start without checking it out.
func (c *Client) CreateBranch(ctx context.Context, repo Repo, name, start string) error {
	_, err := c.gitIn(ctx, repo, "branch", "--no-track", name, start)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("create branch %q: %s", name, ee.Result.Err())
		}
		return err
	}
	return nil
}
