package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
)

// syncPruneWorktrees forgets worktrees whose directory was deleted without
// git worktree remove. git still counts their branches as checked out
// there, so until they are pruned sync would keep those branches and say to
// sync from a directory that no longer exists. A locked worktree is never
// pruned (that's what the lock is for, e.g. a checkout on a drive that isn't
// mounted), so it only gets a notice.
func (a *App) syncPruneWorktrees(ctx context.Context, st *syncState, res *SyncResult) error {
	var gone []string
	for _, wt := range st.worktrees {
		switch {
		case wt.Prunable && !wt.Locked:
			gone = append(gone, wt.Path)
		case wt.Locked && missing(wt.Path):
			res.notice("the worktree at %s is locked, so git keeps it although its directory is gone; run git worktree unlock %s and sync again to clean it up",
				shortPath(wt.Path), wt.Path)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	if err := a.d.Git.PruneWorktrees(ctx, st.repo); err != nil {
		return err
	}
	for _, p := range gone {
		res.notice("forgot the worktree at %s because its directory is gone", shortPath(p))
	}
	return a.loadCheckouts(ctx, st)
}

// missing reports whether nothing exists at path.
func missing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}
