package git

import (
	"context"
	"errors"
	"fmt"

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
	return c.rebaseOutcome(ctx, repo, err)
}

// RebaseAbort abandons a rebase in progress; git puts the branch it was
// rebasing back and checks it out.
func (c *Client) RebaseAbort(ctx context.Context, repo Repo) error {
	_, err := c.gitIn(ctx, repo, "rebase", "--abort")
	return err
}

// rebaseOutcome classifies a rebase failure. A rebase still in progress
// with unmerged paths is a conflict stop; anything else is an error carrying
// git's stderr.
func (c *Client) rebaseOutcome(ctx context.Context, repo Repo, err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return false, err
	}
	active, aerr := c.RebaseInProgress(ctx, repo)
	if aerr != nil {
		return false, aerr
	}
	if active {
		files, ferr := c.ConflictedFiles(ctx, repo)
		if ferr != nil {
			return false, ferr
		}
		if len(files) > 0 {
			return true, nil
		}
	}
	return false, fmt.Errorf("git rebase: %s", ee.Result.Err())
}
