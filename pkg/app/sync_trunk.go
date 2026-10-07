package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DomBlack/git-stack/pkg/git"
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
		// A diverged trunk needs the user's say so. The question is asked
		// between steps, never under one, so no spinner draws over it.
		diverged, n := false, 0
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
				return a.moveBranch(ctx, st, trunk, lb.Head, remoteTip, false)
			case ahead:
				t.Status = TrunkAhead
				t.To = ""
			default:
				diverged = true
				n, _ = a.d.Git.CountCommits(ctx, st.repo, remoteRef, trunk)
			}
			return nil
		})
		if err == nil && diverged {
			consent := o.Force
			if !consent && a.d.Prompter != nil {
				q := fmt.Sprintf("%s has diverged from %s/%s (%d local %s not on the remote). Reset it to the remote?",
					trunk, st.remote, trunk, n, pluralise(n, "commit", "commits"))
				if consent, err = a.d.Prompter.Confirm(q, false); err != nil {
					return err
				}
			}
			if consent {
				err = a.progress(ctx, PhaseSync, fmt.Sprintf("Resetting %s to %s/%s", trunk, st.remote, trunk), func(ctx context.Context) error {
					t.Status = TrunkReset
					return a.moveBranch(ctx, st, trunk, lb.Head, remoteTip, true)
				})
			} else {
				t.Status = TrunkDiverged
				res.notice("%s has diverged from %s/%s (%d local %s); run git stack sync -f to reset it to the remote",
					trunk, st.remote, trunk, n, pluralise(n, "commit", "commits"))
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			n, ok := a.notUpdated(ctx, st, trunk, lb.Worktree, err)
			if !ok {
				return err
			}
			res.NotUpdated = append(res.NotUpdated, n)
			t.Status = TrunkNotUpdated
			if !n.Failed() {
				t.Status = TrunkDirty
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
	return a.isAncestor(ctx, st.repo, ancestor, descendant)
}

// isAncestor is ancestor without a sync state.
func (a *App) isAncestor(ctx context.Context, repo git.Repo, ancestor, descendant string) bool {
	ok, err := a.d.Git.IsAncestor(ctx, repo, ancestor, descendant)
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
