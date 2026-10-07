package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Tip resolves a full ref name (refs/heads/x, refs/remotes/origin/x) to a
// commit id. ok is false when the ref does not exist.
func (c *Client) Tip(ctx context.Context, repo Repo, ref string) (string, bool, error) {
	res, err := c.gitIn(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return res.Out(), true, nil
}

// RefUpdate moves Ref to New. When Old is set the ref must currently be at
// Old, and the whole transaction fails if any Old is stale.
type RefUpdate struct {
	Ref, New, Old string
}

// UpdateRefs applies every update in one transaction: all move or none do.
func (c *Client) UpdateRefs(ctx context.Context, repo Repo, updates []RefUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	var in strings.Builder
	in.WriteString("start\n")
	for _, u := range updates {
		if u.Old != "" {
			fmt.Fprintf(&in, "update %s %s %s\n", u.Ref, u.New, u.Old)
		} else {
			fmt.Fprintf(&in, "update %s %s\n", u.Ref, u.New)
		}
	}
	in.WriteString("prepare\ncommit\n")
	// The transaction takes every ref lock in prepare, before writing
	// anything, so a ref lock someone else holds is safe to wait out.
	args := []string{"update-ref", "--stdin"}
	_, err := c.retryLocked(ctx, args, func(string) bool { return false }, true, func() (exec.Result, error) {
		return c.gitInput(ctx, repo, strings.NewReader(in.String()), nil, args...)
	})
	if err != nil {
		if LockPath(err) != "" {
			return err
		}
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("update refs: %s", ee.Result.Err())
		}
		return err
	}
	return nil
}

// DeleteBranch deletes a local branch whether or not it is merged. git
// refuses when the branch is checked out in any worktree; callers check.
func (c *Client) DeleteBranch(ctx context.Context, repo Repo, name string) error {
	_, err := c.gitIn(ctx, repo, "branch", "-D", "--quiet", name)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("delete branch %q: %s", name, ee.Result.Err())
		}
		return err
	}
	return nil
}

// MergeBase returns the best common ancestor of a and b.
func (c *Client) MergeBase(ctx context.Context, repo Repo, a, b string) (string, error) {
	res, err := c.gitIn(ctx, repo, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return res.Out(), nil
}

// RevList lists the commits reachable from to but not from, oldest first.
func (c *Client) RevList(ctx context.Context, repo Repo, from, to string) ([]string, error) {
	res, err := c.gitIn(ctx, repo, "rev-list", "--reverse", from+".."+to)
	if err != nil {
		return nil, err
	}
	if res.Out() == "" {
		return nil, nil
	}
	return strings.Split(res.Out(), "\n"), nil
}

// MergeFF fast forwards the branch checked out in repo's worktree to rev and
// updates the tree. It refuses, changing nothing, when rev is not a
// descendant or a dirty file would be overwritten.
func (c *Client) MergeFF(ctx context.Context, repo Repo, rev string) error {
	_, err := c.gitIn(ctx, repo, "merge", "--ff-only", "--quiet", rev)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && LockPath(err) == "" {
			if changed, untracked := parseOverwrite(ee.Result.Err()); len(changed)+len(untracked) > 0 {
				return &OverwriteError{Changed: changed, Untracked: untracked}
			}
			return fmt.Errorf("fast forward: %s", firstLine(ee.Result.Err()))
		}
		return err
	}
	return nil
}

// ResetHard moves the checked out branch, the index and the tree to rev.
// git deletes an untracked file in the way of a tracked one; callers check
// with AddedPaths and Untracked first.
func (c *Client) ResetHard(ctx context.Context, repo Repo, rev string) error {
	_, err := c.gitIn(ctx, repo, "reset", "--hard", "--quiet", rev)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && LockPath(err) == "" {
			return fmt.Errorf("reset: %s", firstLine(ee.Result.Err()))
		}
		return err
	}
	return nil
}

// ResetKeep moves HEAD and the checked out branch to rev and updates the
// working tree, keeping local changes to files rev and HEAD agree on. git
// refuses, and nothing moves, when a locally changed file differs between
// the two. Staged new files are re-added afterwards because reset --keep
// drops them from the index; the caller refuses up front when the move adds
// the same path, so re-adding cannot clobber anything.
func (c *Client) ResetKeep(ctx context.Context, repo Repo, rev string) error {
	res, err := c.gitIn(ctx, repo, "diff", "--cached", "--name-only", "-z", "--no-renames", "--diff-filter=A")
	if err != nil {
		return err
	}
	var added []string
	for p := range strings.SplitSeq(res.Out(), "\x00") {
		if p != "" {
			added = append(added, p)
		}
	}
	if _, err := c.gitIn(ctx, repo, "reset", "--keep", "-q", rev); err != nil {
		return err
	}
	if len(added) == 0 {
		return nil
	}
	_, err = c.gitIn(ctx, repo, append([]string{"add", "--"}, added...)...)
	return err
}

// Reflog lists every commit ref has pointed at that the reflog still
// remembers, newest first. A ref with no reflog, or no ref at all, gives
// nothing.
func (c *Client) Reflog(ctx context.Context, repo Repo, ref string) ([]string, error) {
	if _, ok, err := c.Tip(ctx, repo, ref); err != nil || !ok {
		return nil, err
	}
	res, err := c.gitIn(ctx, repo, "log", "-g", "--format=%H", ref, "--")
	if err != nil {
		return nil, err
	}
	if res.Out() == "" {
		return nil, nil
	}
	return strings.Split(res.Out(), "\n"), nil
}

// firstLine is the first line of git's message without its "error: " or
// "fatal: " prefix, for errors shown on one line.
func firstLine(msg string) string {
	msg, _, _ = strings.Cut(strings.TrimSpace(msg), "\n")
	for _, p := range []string{"error: ", "fatal: "} {
		msg = strings.TrimPrefix(msg, p)
	}
	return strings.TrimSpace(msg)
}

// OverwriteError is git refusing to move a checkout because doing so would
// overwrite files in it: local changes to tracked files (Changed) and
// untracked files where the update writes (Untracked). Paths are relative
// to the checkout. git touched nothing.
type OverwriteError struct {
	Changed, Untracked []string
}

func (e *OverwriteError) Error() string {
	var parts []string
	if n := len(e.Changed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", n))
	}
	if n := len(e.Untracked); n > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", n))
	}
	return "fast forward would overwrite " + strings.Join(parts, " and ") + " files"
}

// parseOverwrite reads the file lists out of git's "would be overwritten"
// refusal: a header line, then one tab indented path per line (C quoted
// when it has unusual characters), then a hint.
func parseOverwrite(stderr string) (changed, untracked []string) {
	var cur *[]string
	for line := range strings.SplitSeq(stderr, "\n") {
		switch {
		case strings.Contains(line, "Your local changes to the following files would be overwritten"):
			cur = &changed
		case strings.Contains(line, "untracked working tree files would be overwritten"):
			cur = &untracked
		case strings.HasPrefix(line, "\t") && cur != nil:
			*cur = append(*cur, unquotePath(strings.TrimPrefix(line, "\t")))
		default:
			cur = nil
		}
	}
	return changed, untracked
}

// unquotePath undoes git's C style quoting of a path ("a\303\251.txt").
func unquotePath(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}
