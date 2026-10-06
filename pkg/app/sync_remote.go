package app

import (
	"context"
	"errors"
)

// syncRemote fast forwards every tracked branch the remote is strictly ahead
// of (a suggestion applied on GitHub, a colleague's commit, work from another
// machine). Local ahead is left for submit. Diverged branches get a notice
// only when the remote holds changes we don't have at all (see theirCommits).
func (a *App) syncRemote(ctx context.Context, st *syncState, res *SyncResult) error {
	return a.progress(ctx, PhaseSync, "Checking "+st.remote, func(ctx context.Context) error {
		for _, name := range st.graph.Branches() {
			lb, ok := st.local[name]
			if !ok {
				continue
			}
			remoteTip, ok, err := a.d.Git.Tip(ctx, st.repo, "refs/remotes/"+st.remote+"/"+name)
			if err != nil {
				return err
			}
			if !ok || remoteTip == lb.Head {
				continue
			}
			switch {
			case a.ancestor(ctx, st, lb.Head, remoteTip):
				err := a.moveBranch(ctx, st, name, lb.Head, remoteTip, false)
				if errors.Is(err, errDirty) {
					res.notice("%s moved on %s but is checked out in %s with uncommitted changes, so it was not updated",
						name, st.remote, shortPath(lb.Worktree))
					continue
				}
				if errors.Is(err, errRefused) {
					res.notice("%s moved on %s but git would not fast forward it in %s, so it was not updated: %v",
						name, st.remote, shortPath(lb.Worktree), err)
					continue
				}
				if err != nil {
					return err
				}
				res.Updated = append(res.Updated, BranchMove{Name: name, From: lb.Head, To: remoteTip})
			case a.ancestor(ctx, st, remoteTip, lb.Head):
				// Local ahead: submit pushes it.
			default:
				n, err := a.theirCommits(ctx, st.repo, name, lb.Head, remoteTip)
				if err != nil {
					return err
				}
				if n == 0 {
					continue
				}
				res.Behind = append(res.Behind, RemoteAhead{Name: name, Commits: n})
				res.notice("%s has %d %s on %s that you don't have locally; pull them in by hand before submitting",
					name, n, pluralise(n, "commit", "commits"), st.remote)
			}
		}
		return nil
	})
}
