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
	// Every field ends in NUL, so a worktree path with a newline in it can't
	// split a record; for-each-ref still puts a newline after each one,
	// which lands at the start of the next record's name (ref names can't
	// contain newlines).
	const fields = 5
	const format = "%(refname:short)%00%(objectname)%00%(committerdate:unix)%00%(worktreepath)%00%(upstream:short)%00"
	res, err := c.gitIn(ctx, repo, "for-each-ref", "--format="+format, "--sort=refname", "refs/heads/")
	if err != nil {
		return nil, err
	}
	all := strings.Split(string(res.Stdout), "\x00")
	all = all[:len(all)-1] // after the last NUL there's only the final newline
	if len(all)%fields != 0 {
		return nil, fmt.Errorf("for-each-ref: unexpected output %q", res.Stdout)
	}
	var out []Branch
	for i := 0; i < len(all); i += fields {
		f := all[i : i+fields]
		f[0] = strings.TrimPrefix(f[0], "\n")
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

// DefaultBranch guesses the repository's trunk the way gh-stack does: the
// branch origin/HEAD points at, else a local main or master. The boolean is
// false when nothing matches.
func (c *Client) DefaultBranch(ctx context.Context, repo Repo) (string, bool, error) {
	res, err := c.gitIn(ctx, repo, "symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD")
	if err == nil {
		if _, name, ok := strings.Cut(res.Out(), "/"); ok && name != "" {
			if exists, err := c.BranchExists(ctx, repo, name); err != nil {
				return "", false, err
			} else if exists {
				return name, true, nil
			}
		}
	} else if ee, ok := errors.AsType[*exec.ExitError](err); !ok || ee.Result.ExitCode != 1 {
		return "", false, err
	}
	for _, name := range []string{"main", "master"} {
		exists, err := c.BranchExists(ctx, repo, name)
		if err != nil {
			return "", false, err
		}
		if exists {
			return name, true, nil
		}
	}
	return "", false, nil
}

// SwitchDetached checks out rev with a detached HEAD.
func (c *Client) SwitchDetached(ctx context.Context, repo Repo, rev string) error {
	_, err := c.gitIn(ctx, repo, "switch", "--detach", rev)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("switch --detach %q: %s", rev, ee.Result.Err())
		}
		return err
	}
	return nil
}

// Worktree is one entry of `git worktree list`.
type Worktree struct {
	// Path is the working tree root.
	Path string
	Head string
	// Branch is the checked out branch (short name), empty when detached or bare.
	Branch   string
	Detached bool
	Bare     bool
}

// Worktrees lists the main checkout and every linked worktree.
func (c *Client) Worktrees(ctx context.Context, repo Repo) ([]Worktree, error) {
	// -z (git 2.36+; we need 2.40) ends every attribute in NUL and every
	// record in an extra NUL, so paths with newlines come through whole.
	res, err := c.gitIn(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var out []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for line := range strings.SplitSeq(string(res.Stdout), "\x00") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			cur.Bare = true
		}
	}
	flush()
	return out, nil
}
