package git

import "context"

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
