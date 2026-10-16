package git

import (
	"context"
	"errors"
	"fmt"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Fetch updates the remote-tracking refs of remote and prunes the ones whose
// branch is gone. Nothing local moves.
func (c *Client) Fetch(ctx context.Context, repo Repo, remote string) error {
	_, err := c.gitIn(ctx, repo, "fetch", "--prune", "--quiet", remote)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("fetch %s: %s", remote, ee.Result.Err())
		}
		return err
	}
	return nil
}

// RemoteFor is the remote a branch tracks (branch.<name>.remote), or origin
// when it tracks nothing.
func (c *Client) RemoteFor(ctx context.Context, repo Repo, branch string) string {
	v, ok, err := c.ConfigGet(ctx, repo, ScopeLocal, "branch."+branch+".remote")
	if err == nil && ok && v != "" && v != "." {
		return v
	}
	return "origin"
}

// PushLease is a branch to push and the remote tip it may replace. An empty
// Expect means the branch must not exist on the remote yet.
type PushLease struct {
	Branch string
	Expect string
}

// DeleteRemoteBranch deletes branch on remote. With expect set, the delete
// is leased against it, so a branch that has moved on the remote since is
// refused rather than deleted; with it empty the branch is deleted whatever
// its tip.
func (c *Client) DeleteRemoteBranch(ctx context.Context, repo Repo, remote, branch, expect string) error {
	ref := "refs/heads/" + branch
	args := []string{"push", "--quiet"}
	if expect != "" {
		args = append(args, "--force-with-lease="+ref+":"+expect)
	}
	args = append(args, remote, ":"+ref)
	if _, err := c.gitIn(ctx, repo, args...); err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("delete %s on %s: %s", branch, remote, ee.Result.Err())
		}
		return err
	}
	return nil
}

// Push force pushes each branch to the same name on remote, all or nothing.
// Each one is leased against its Expect, so a remote that has moved since
// the caller looked is refused rather than overwritten.
func (c *Client) Push(ctx context.Context, repo Repo, remote string, leases []PushLease) error {
	if len(leases) == 0 {
		return nil
	}
	args := []string{"push", "--atomic", "--quiet"}
	refspecs := make([]string, 0, len(leases))
	for _, l := range leases {
		ref := "refs/heads/" + l.Branch
		args = append(args, "--force-with-lease="+ref+":"+l.Expect)
		refspecs = append(refspecs, ref+":"+ref)
	}
	args = append(append(args, remote), refspecs...)
	if _, err := c.gitIn(ctx, repo, args...); err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("push to %s: %s", remote, ee.Result.Err())
		}
		return err
	}
	return nil
}
