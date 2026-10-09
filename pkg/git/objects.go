package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// CommitInfo is what it takes to recreate a commit somewhere else.
type CommitInfo struct {
	Author, Email string
	// Date is the author date in strict ISO 8601 (git's %aI).
	Date    string
	Message string
	Tree    string
	// Parent is the first parent, "" for a root commit.
	Parent string
}

// CommitInfo reads rev's author, message, tree and first parent.
func (c *Client) CommitInfo(ctx context.Context, repo Repo, rev string) (CommitInfo, error) {
	res, err := c.gitIn(ctx, repo, "log", "-1", "--format=%an%x00%ae%x00%aI%x00%T%x00%P%x00%B", rev)
	if err != nil {
		return CommitInfo{}, err
	}
	f := strings.SplitN(string(res.Stdout), "\x00", 6)
	if len(f) != 6 {
		return CommitInfo{}, fmt.Errorf("commit info for %s: unexpected output", rev)
	}
	info := CommitInfo{Author: f[0], Email: f[1], Date: f[2], Tree: f[3], Message: strings.TrimRight(f[5], "\n")}
	if parents := strings.Fields(f[4]); len(parents) > 0 {
		info.Parent = parents[0]
	}
	return info, nil
}

// TreeOf returns the tree of the commit rev.
func (c *Client) TreeOf(ctx context.Context, repo Repo, rev string) (string, error) {
	res, err := c.gitIn(ctx, repo, "rev-parse", "--verify", "--quiet", rev+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("tree of %s: %w", rev, err)
	}
	return res.Out(), nil
}

// MergeTreeResult is a three way merge computed without a working tree:
// the merged tree, and the conflicting paths when it was not clean (the
// tree then contains conflict markers and must not be committed).
type MergeTreeResult struct {
	Tree      string
	Conflicts []string
}

// MergeTree merges ours and theirs with base as the common ancestor.
func (c *Client) MergeTree(ctx context.Context, repo Repo, base, ours, theirs string) (MergeTreeResult, error) {
	res, err := c.gitIn(ctx, repo, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", "--merge-base="+base, ours, theirs)
	if err == nil {
		return MergeTreeResult{Tree: strings.TrimRight(res.Out(), "\x00")}, nil
	}
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok || ee.Result.ExitCode != 1 {
		return MergeTreeResult{}, err
	}
	// Exit 1 is a conflict: the tree, then each conflicted path, all NUL
	// terminated (raw, where the newline form would C quote odd paths).
	lines := splitNUL(string(ee.Result.Stdout))
	if len(lines) == 0 {
		return MergeTreeResult{}, fmt.Errorf("merge-tree: no output")
	}
	out := MergeTreeResult{Tree: lines[0]}
	seen := map[string]bool{}
	for _, p := range lines[1:] {
		if p != "" && !seen[p] {
			seen[p] = true
			out.Conflicts = append(out.Conflicts, p)
		}
	}
	return out, nil
}

// CommitTree creates a commit of tree on parent with info's author, author
// date and message. The committer is the current user. The commit is not
// signed: unlike commit and rebase, commit-tree ignores commit.gpgsign and
// only signs when asked with -S, which we don't pass, so a replay never
// needs the signing key.
func (c *Client) CommitTree(ctx context.Context, repo Repo, tree, parent string, info CommitInfo) (string, error) {
	env := []string{"GIT_AUTHOR_NAME=" + info.Author, "GIT_AUTHOR_EMAIL=" + info.Email, "GIT_AUTHOR_DATE=" + info.Date}
	res, err := c.gitInput(ctx, repo, strings.NewReader(info.Message+"\n"), env, "commit-tree", tree, "-p", parent, "-F", "-")
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", fmt.Errorf("commit-tree: %s", ee.Result.Err())
		}
		return "", err
	}
	return res.Out(), nil
}

// PatchIDs maps the patch id of every non merge commit in from..to to that
// commit, so two histories can be compared for the same changes regardless
// of where they were rebased. The ids are verbatim: unlike --stable they
// count whitespace, so a whitespace only amend is a different change.
func (c *Client) PatchIDs(ctx context.Context, repo Repo, from, to string) (map[string]string, error) {
	return c.patchIDs(ctx, repo, from+".."+to)
}

// PatchIDsExcluding is PatchIDs for the commits reachable from to but from
// none of exclude.
func (c *Client) PatchIDsExcluding(ctx context.Context, repo Repo, to string, exclude ...string) (map[string]string, error) {
	revs := []string{to}
	for _, x := range exclude {
		revs = append(revs, "^"+x)
	}
	return c.patchIDs(ctx, repo, revs...)
}

func (c *Client) patchIDs(ctx context.Context, repo Repo, revs ...string) (map[string]string, error) {
	args := append([]string{"log", "-p", "--no-color", "--no-merges", "--format=commit %H"}, revs...)
	log, err := c.gitIn(ctx, repo, append(args, "--")...)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(bytes.TrimSpace(log.Stdout)) == 0 {
		return out, nil
	}
	res, err := c.gitInput(ctx, repo, bytes.NewReader(log.Stdout), nil, "patch-id", "--verbatim")
	if err != nil {
		return nil, err
	}
	for line := range strings.SplitSeq(res.Out(), "\n") {
		if id, sha, ok := strings.Cut(line, " "); ok {
			out[id] = sha
		}
	}
	return out, nil
}
