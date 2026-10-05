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
