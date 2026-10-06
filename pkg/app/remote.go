package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// theirCommits counts the commits on remoteTip that someone else pushed to
// branch and that local doesn't have, i.e. what force pushing local would
// throw away. The caller has already ruled out remoteTip == local and
// remoteTip being an ancestor of local.
//
// A remote tip the local branch has pointed at before was pushed from here,
// so it never counts. That matters after a squash merge further down: the
// remote still holds the parent's old commits, which no longer match
// anything locally by patch id.
func (a *App) theirCommits(ctx context.Context, repo git.Repo, branch, local, remoteTip string) (int, error) {
	seen, err := a.d.Git.Reflog(ctx, repo, "refs/heads/"+branch)
	if err != nil {
		return 0, err
	}
	if slices.Contains(seen, remoteTip) {
		return 0, nil
	}
	// Commit ids aren't enough either: a local restack that hasn't been
	// pushed yet looks diverged by id but not by patch id.
	base, err := a.d.Git.MergeBase(ctx, repo, local, remoteTip)
	if err != nil {
		return 0, err
	}
	remoteIDs, err := a.d.Git.PatchIDs(ctx, repo, base, remoteTip)
	if err != nil {
		return 0, err
	}
	localIDs, err := a.d.Git.PatchIDs(ctx, repo, base, local)
	if err != nil {
		return 0, err
	}
	n := 0
	for id := range remoteIDs {
		if _, ok := localIDs[id]; !ok {
			n++
		}
	}
	return n, nil
}

// remoteTip is branch's remote tracking tip, with the remote it came from.
func (a *App) remoteTip(ctx context.Context, repo git.Repo, branch string) (remote, tip string, ok bool, err error) {
	remote = a.d.Git.RemoteFor(ctx, repo, branch)
	tip, ok, err = a.d.Git.Tip(ctx, repo, "refs/remotes/"+remote+"/"+branch)
	return remote, tip, ok, err
}

// checkPushedOver refuses a submit that would force push over commits
// someone else pushed to a branch in the stack. It works from the remote
// tracking refs, so it catches what the last fetch saw; anything pushed
// since then is caught by gh stack's --force-with-lease instead.
func (a *App) checkPushedOver(ctx context.Context, repo git.Repo, branches []string) error {
	var behind []string
	for _, name := range branches {
		local, ok, err := a.d.Git.Tip(ctx, repo, "refs/heads/"+name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		remote, tip, ok, err := a.remoteTip(ctx, repo, name)
		if err != nil {
			return err
		}
		if !ok || tip == local {
			continue
		}
		if ahead, err := a.d.Git.IsAncestor(ctx, repo, tip, local); err != nil || ahead {
			if err != nil {
				return err
			}
			continue
		}
		n, err := a.theirCommits(ctx, repo, name, local, tip)
		if err != nil {
			return err
		}
		if n > 0 {
			behind = append(behind, fmt.Sprintf("%s (%d %s on %s)", name, n, pluralise(n, "commit", "commits"), remote))
		}
	}
	if len(behind) == 0 {
		return nil
	}
	return stack.Newf(stack.KindInvalidArgs, "submitting would overwrite commits you don't have locally: %s", strings.Join(behind, ", ")).
		WithSteps("run git stack sync; it pulls them in when the remote is simply ahead",
			"otherwise merge or cherry-pick them onto your branch by hand",
			"or pass --force to overwrite them")
}
