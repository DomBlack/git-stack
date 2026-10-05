package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// syncTrunks fast forwards every trunk to its remote. A trunk that has
// diverged is only reset with consent: -f, or a yes on a terminal.
func (a *App) syncTrunks(ctx context.Context, st *syncState, o SyncOptions, res *SyncResult) error {
	for _, trunk := range st.trunks {
		t := TrunkSync{Name: trunk, Status: TrunkUpToDate}
		lb, ok := st.local[trunk]
		if !ok {
			t.Status = TrunkNoRemote
			res.notice("%s does not exist locally; it was not synced", trunk)
			res.Trunks = append(res.Trunks, t)
			continue
		}
		t.From = lb.Head
		remoteRef := "refs/remotes/" + st.remote + "/" + trunk
		remoteTip, ok, err := a.d.Git.Tip(ctx, st.repo, remoteRef)
		if err != nil {
			return err
		}
		if !ok {
			t.Status = TrunkNoRemote
			res.notice("%s has no branch on %s; it was not synced", trunk, st.remote)
			res.Trunks = append(res.Trunks, t)
			continue
		}
		t.To = remoteTip
		err = a.progress(ctx, PhaseSync, "Updating "+trunk, func(ctx context.Context) error {
			// Errors here must not read as "diverged": that path can reset the trunk.
			behind, ahead := false, false
			if lb.Head != remoteTip {
				var err error
				if behind, err = a.d.Git.IsAncestor(ctx, st.repo, lb.Head, remoteTip); err != nil {
					return err
				}
				if !behind {
					if ahead, err = a.d.Git.IsAncestor(ctx, st.repo, remoteTip, lb.Head); err != nil {
						return err
					}
				}
			}
			switch {
			case lb.Head == remoteTip:
				t.To = ""
			case behind:
				t.Status = TrunkFastForwarded
				if err := a.moveBranch(ctx, st, trunk, lb.Head, remoteTip, false); err != nil {
					return err
				}
			case ahead:
				t.Status = TrunkAhead
				t.To = ""
			default:
				n, _ := a.d.Git.CountCommits(ctx, st.repo, remoteRef, trunk)
				consent := o.Force
				if !consent && a.d.Prompter != nil {
					q := fmt.Sprintf("%s has diverged from %s/%s (%d local %s not on the remote). Reset it to the remote?",
						trunk, st.remote, trunk, n, pluralise(n, "commit", "commits"))
					consent, err = a.d.Prompter.Confirm(q, false)
					if err != nil {
						return err
					}
				}
				if !consent {
					t.Status = TrunkDiverged
					res.notice("%s has diverged from %s/%s (%d local %s); run git stack sync -f to reset it to the remote",
						trunk, st.remote, trunk, n, pluralise(n, "commit", "commits"))
					return nil
				}
				t.Status = TrunkReset
				if err := a.moveBranch(ctx, st, trunk, lb.Head, remoteTip, true); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			switch {
			case errors.Is(err, errDirty):
				t.Status = TrunkDirty
				res.notice("%s is checked out in %s with uncommitted changes, so it was not updated; commit or stash them and sync again",
					trunk, shortPath(lb.Worktree))
			case errors.Is(err, errRefused):
				t.Status = TrunkDirty
				res.notice("%s is checked out in %s and was not updated (%v); sort that out and sync again",
					trunk, shortPath(lb.Worktree), err)
			default:
				return err
			}
			res.Trunks = append(res.Trunks, t)
			continue
		}
		res.Trunks = append(res.Trunks, t)
	}
	return nil
}

// ancestor is IsAncestor with errors treated as "no".
func (a *App) ancestor(ctx context.Context, st *syncState, ancestor, descendant string) bool {
	ok, err := a.d.Git.IsAncestor(ctx, st.repo, ancestor, descendant)
	return err == nil && ok
}

// shortPath shows a worktree path relative to $HOME when it is under it.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return p
}
