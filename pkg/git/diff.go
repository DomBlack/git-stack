package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// StagedDiff returns the diff of the index against HEAD.
func (c *Client) StagedDiff(ctx context.Context, repo Repo) (string, error) {
	res, err := c.gitIn(ctx, repo, "diff", "--cached", "--no-color", "--no-ext-diff")
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

// DiffRange returns the diff between base and head (base...head semantics
// are not used: this is the exact tree difference, which is what a rebased
// branch shows against its parent).
func (c *Client) DiffRange(ctx context.Context, repo Repo, base, head string) (string, error) {
	res, err := c.gitIn(ctx, repo, "diff", "--no-color", "--no-ext-diff", base, head)
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

// Subjects returns the commit subjects of rev's history, newest first, at
// most n.
func (c *Client) Subjects(ctx context.Context, repo Repo, rev string, n int) ([]string, error) {
	res, err := c.gitIn(ctx, repo, "log", "--format=%s", "-n", itoa(n), rev, "--")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(res.Out()), nil
}

// Messages returns the full commit messages in from..to, oldest first.
func (c *Client) Messages(ctx context.Context, repo Repo, from, to string) ([]string, error) {
	res, err := c.gitIn(ctx, repo, "log", "--reverse", "--format=%B%x00", from+".."+to, "--")
	if err != nil {
		return nil, err
	}
	var out []string
	for msg := range strings.SplitSeq(string(res.Stdout), "\x00") {
		if m := strings.TrimSpace(msg); m != "" {
			out = append(out, m)
		}
	}
	return out, nil
}

// RebaseInProgress reports whether git has a rebase in progress.
func (c *Client) RebaseInProgress(ctx context.Context, repo Repo) (bool, error) {
	for _, p := range []string{"rebase-merge", "rebase-apply"} {
		res, err := c.gitIn(ctx, repo, "rev-parse", "--git-path", p)
		if err != nil {
			return false, err
		}
		path := res.Out()
		if !filepath.IsAbs(path) {
			path = filepath.Join(repo.TopLevel, path)
		}
		if _, err := os.Stat(path); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// ConflictedFiles lists paths with unresolved merge conflicts.
func (c *Client) ConflictedFiles(ctx context.Context, repo Repo) ([]string, error) {
	res, err := c.gitIn(ctx, repo, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(res.Out()), nil
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + intString(n))
}

func intString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
