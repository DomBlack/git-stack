package git

import (
	"context"
	"fmt"
	"strings"
)

// Commit is a commit as a list shows it: its id and subject.
type Commit struct {
	SHA     string
	Subject string
}

// Commits lists the commits reachable from tip but from none of exclude,
// newest first. Revisions that don't resolve (a deleted parent branch, a
// recorded base that was garbage collected) are ignored rather than failing
// the read, and a missing tip lists nothing. It's a read that can run while
// another git process works in any worktree, so it takes no optional locks,
// and the output is NUL separated since a subject can hold anything.
func (c *Client) Commits(ctx context.Context, repo Repo, tip string, exclude ...string) ([]Commit, error) {
	args := []string{"--no-optional-locks", "log", "--ignore-missing", "--no-show-signature", "-z", "--format=%H%x00%s", tip}
	for _, e := range exclude {
		if e != "" {
			args = append(args, "^"+e)
		}
	}
	res, err := c.gitIn(ctx, repo, append(args, "--")...)
	if err != nil {
		return nil, err
	}
	out := string(res.Stdout)
	if out == "" {
		return nil, nil
	}
	// -z ends each commit with a NUL and the format puts one between the
	// id and the subject, so once the last terminator is gone the fields
	// simply alternate, an empty subject included.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("commits of %s: unexpected output", tip)
	}
	commits := make([]Commit, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		commits = append(commits, Commit{SHA: fields[i], Subject: fields[i+1]})
	}
	return commits, nil
}
