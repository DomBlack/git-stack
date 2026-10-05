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
	Tip      string
	Replayed int
	// Conflict is set when the replay stopped; the commits before it were
	// recreated as unreferenced objects and nothing else happened.
	Conflict *Conflict
}

// Replay recreates commits, oldest first, on top of onto. It never touches a
// working tree, the index or a ref: it only creates objects, so the caller
// decides what to do with Tip. A merge commit is replayed against its first
// parent, which linearises it.
func (c *Client) Replay(ctx context.Context, repo Repo, commits []string, onto string) (ReplayResult, error) {
	tip := onto
	for i, sha := range commits {
		info, err := c.CommitInfo(ctx, repo, sha)
		if err != nil {
			return ReplayResult{}, err
		}
		mt, err := c.MergeTree(ctx, repo, info.Parent, tip, sha)
		if err != nil {
			return ReplayResult{}, err
		}
		if len(mt.Conflicts) > 0 {
			return ReplayResult{Replayed: i, Conflict: &Conflict{Commit: sha, Files: mt.Conflicts}}, nil
		}
		if tip, err = c.CommitTree(ctx, repo, mt.Tree, tip, info); err != nil {
			return ReplayResult{}, err
		}
	}
	return ReplayResult{Tip: tip, Replayed: len(commits)}, nil
}
