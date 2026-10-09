package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Conflict is a commit that could not be replayed cleanly.
type Conflict struct {
	Commit string
	Files  []string
}

// ReplayResult is what Replay produced.
type ReplayResult struct {
	// Tip is the new tip (onto itself when there was nothing to replay), or
	// "" when Conflict is set.
	Tip string
	// Replayed counts the commits recreated.
	Replayed int
	// Skipped counts the commits dropped because their changes were already
	// in the new parent, as git rebase drops them.
	Skipped int
	// Conflict is set when the replay stopped; the commits before it were
	// recreated as unreferenced objects and nothing else happened.
	Conflict *Conflict
}

// Replay recreates commits, oldest first, on top of onto. It never touches a
// working tree, the index or a ref: it only creates objects, so the caller
// decides what to do with Tip. A merge commit is replayed against its first
// parent, which linearises it. A commit whose changes are already in the new
// parent would come out empty and is dropped instead. A root commit is
// replayed with an empty merge base.
func (c *Client) Replay(ctx context.Context, repo Repo, commits []string, onto string) (ReplayResult, error) {
	tip := onto
	tree, err := c.TreeOf(ctx, repo, onto)
	if err != nil {
		return ReplayResult{}, err
	}
	var out ReplayResult
	for _, sha := range commits {
		info, err := c.CommitInfo(ctx, repo, sha)
		if err != nil {
			return ReplayResult{}, err
		}
		mt, err := c.MergeTree(ctx, repo, info.Parent, tip, sha)
		if err != nil {
			return ReplayResult{}, err
		}
		if len(mt.Conflicts) > 0 {
			out.Conflict = &Conflict{Commit: sha, Files: mt.Conflicts}
			return out, nil
		}
		if mt.Tree == tree {
			out.Skipped++
			continue
		}
		if tip, err = c.CommitTree(ctx, repo, mt.Tree, tip, info); err != nil {
			return ReplayResult{}, err
		}
		tree = mt.Tree
		out.Replayed++
	}
	out.Tip = tip
	return out, nil
}

// rebaseEnv keeps a rebase from opening an editor or a todo list.
var rebaseEnv = []string{"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true"}

// RebaseMatches reports whether the active rebase is for branch, from tip
// onto onto. Both git rebase backends record these values in the worktree's
// git dir; a rebase started after an earlier one was aborted can differ in
// any of them. It only reads the state, leaving the rebase and index alone.
func (c *Client) RebaseMatches(ctx context.Context, repo Repo, branch, onto, tip string) (bool, error) {
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		res, err := c.gitIn(ctx, repo, "rev-parse", "--git-path", dir)
		if err != nil {
			return false, err
		}
		path := res.Out()
		if !filepath.IsAbs(path) {
			path = filepath.Join(repo.TopLevel, path)
		}
		head, err := os.ReadFile(filepath.Join(path, "head-name"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(string(head)) != "refs/heads/"+branch {
			return false, nil
		}
		for _, field := range []struct{ name, want string }{{"onto", onto}, {"orig-head", tip}} {
			data, err := os.ReadFile(filepath.Join(path, field.name))
			if err != nil {
				return false, err
			}
			if strings.TrimSpace(string(data)) != field.want {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}

// RebaseOnto runs `git rebase --onto onto upstream branch` without an
// editor. It checks branch out, so the working tree must be clean enough
// for git. stopped is true when git stopped on a conflict and left the
// rebase in progress for the caller to resolve; any other failure is an
// error.
func (c *Client) RebaseOnto(ctx context.Context, repo Repo, onto, upstream, branch string) (stopped bool, err error) {
	_, err = c.gitInput(ctx, repo, nil, rebaseEnv, "rebase", "--onto", onto, upstream, branch)
	return c.rebaseOutcome(ctx, repo, err)
}

// RebaseContinue resumes a stopped rebase after the conflicts were staged.
// stopped is true when it stopped again on unresolved conflicts; any other
// failure is an error.
func (c *Client) RebaseContinue(ctx context.Context, repo Repo) (stopped bool, err error) {
	_, err = c.gitInput(ctx, repo, nil, rebaseEnv, "rebase", "--continue")
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && strings.Contains(ee.Result.Err(), stagedChangesMarker) {
		// A pick whose commit failed (signing, say) is left staged and git
		// refuses to continue over it, telling you to commit it yourself.
		// Do that: git takes the author and message from its own rebase
		// state, and the rebase then drops the rescheduled pick as already
		// applied. --no-verify skips pre-commit and commit-msg, the two
		// hooks a pick skips too (both run prepare-commit-msg and
		// post-commit), so the commit sees the same hooks the pick would
		// have. Signing failing again comes back as a SigningError.
		if _, cerr := c.Commit(ctx, repo, CommitOptions{NoEdit: true, NoVerify: true}); cerr != nil {
			return false, cerr
		}
		_, err = c.gitInput(ctx, repo, nil, rebaseEnv, "rebase", "--continue")
	}
	return c.rebaseOutcome(ctx, repo, err)
}

// stagedChangesMarker is git rebase --continue's refusal when the previous
// pick was applied but never committed.
const stagedChangesMarker = "you have staged changes in your working tree"

// RebaseAbort abandons a rebase in progress; git puts the branch it was
// rebasing back and checks it out.
func (c *Client) RebaseAbort(ctx context.Context, repo Repo) error {
	_, err := c.gitIn(ctx, repo, "rebase", "--abort")
	return err
}

// rebaseOutcome classifies a rebase failure. A rebase still in progress
// after git exited non-zero is a stop for the caller to resolve, whether
// paths are left unmerged or rerere already staged a remembered resolution;
// anything else is an error carrying git's stderr. Callers refuse to start
// while another rebase is in progress, so one found here is always theirs.
func (c *Client) rebaseOutcome(ctx context.Context, repo Repo, err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return false, err
	}
	// A commit git could not sign stops the rebase too, with the pick
	// rescheduled and nothing unmerged, so it is a rebase in progress that
	// no amount of resolving would move on. Report the real cause; continue
	// picks it up again once signing works, and abort still puts it back.
	if stderr := strings.TrimSpace(ee.Result.Err()); strings.Contains(stderr, signingFailedMarker) {
		// Only a signing failure when signing is on; git prints the same
		// line when it can't write the object at all, which is a stop or
		// an error like any other below.
		if se := c.signingError(ctx, repo, stderr); se != nil {
			return false, se
		}
	}
	active, aerr := c.RebaseInProgress(ctx, repo)
	if aerr != nil {
		return false, aerr
	}
	if active {
		return true, nil
	}
	return false, fmt.Errorf("git rebase: %s", ee.Result.Err())
}
