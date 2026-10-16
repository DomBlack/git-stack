package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// RenameOptions is one rename of the current branch.
type RenameOptions struct {
	// Name is the branch's new name, used as given (no prefix).
	Name string
	// Local renames the branch in this repository only, leaving the remote
	// branch and its pull request under the old name.
	Local bool
}

// RenameResult reports what happened.
type RenameResult struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Remote is the remote the branch was renamed on as well, "" when only
	// the local branch was renamed.
	Remote  string   `json:"remote,omitempty"`
	Notices []string `json:"notices,omitempty"`
}

const renameCommand = "git stack rename"

// Rename gives the current branch a new name, keeping its place in the
// stack, its commits and its recorded base; nothing is restacked. A branch
// that has been pushed is renamed on the forge first, which keeps its pull
// request attached and retargets the ones based on it, so a refusal there
// leaves everything here as it was. With Local only this repository
// changes.
func (a *App) Rename(ctx context.Context, repo git.Repo, o RenameOptions) (RenameResult, error) {
	command := renameCommand
	if o.Local {
		command += " --local"
	}
	res, err := undoableResult(a, ctx, repo, command, undoKeep, func(ctx context.Context) (RenameResult, error) {
		return a.rename(ctx, repo, o)
	})
	if err == nil && res.Remote != "" {
		// GitHub has the new name now, and undo only ever puts local state
		// back, so like a submit this is as far back as undo can go.
		if cerr := a.clearUndo(ctx, repo); cerr != nil {
			a.d.Log.Warn("could not clear the undo history", "err", cerr)
		}
	}
	return res, err
}

// rename is Rename's body.
func (a *App) rename(ctx context.Context, repo git.Repo, o RenameOptions) (RenameResult, error) {
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return RenameResult{}, err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		if errors.Is(err, git.ErrDetached) {
			return RenameResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch first")
		}
		return RenameResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return RenameResult{}, err
	}
	if graph.IsTrunk(current) {
		return RenameResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; rename works on stacked branches", current)
	}
	s, idx, ok := graph.StackOf(current)
	if !ok {
		return RenameResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", current).
			WithSteps("git stack init " + current + " to start tracking it")
	}
	branch := s.Branches[idx]
	if branch.Merged() {
		return RenameResult{}, stack.Newf(stack.KindInvalidArgs, "%s has merged; run git stack sync first", current)
	}
	if err := a.checkNewName(ctx, repo, graph, current, o.Name); err != nil {
		return RenameResult{}, err
	}
	res := RenameResult{From: current, To: o.Name}

	remote := a.d.Git.RemoteFor(ctx, repo, current)
	tracking := "refs/remotes/" + remote + "/"
	pushedTip, pushed, err := a.d.Git.Tip(ctx, repo, tracking+current)
	if err != nil {
		return res, err
	}
	pushed = pushed || branch.PR != nil
	if pushed && !o.Local {
		if err := a.renameRemote(ctx, repo, remote, current, o.Name); err != nil {
			return res, err
		}
		res.Remote = remote
	}

	if err := a.d.Git.RenameBranch(ctx, repo, current, o.Name); err != nil {
		if res.Remote != "" {
			return res, asStackError(err).WithSteps(fmt.Sprintf("it is already called %s on %s; git branch -m %s %s renames it here to match", o.Name, remote, current, o.Name))
		}
		return res, err
	}
	// A branch renamed on the remote, or never pushed, is the same branch
	// under a new name and keeps its record. One renamed only here leaves
	// its pull request behind on the old name.
	carry := res.Remote != "" || !pushed
	err = a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
		s, i, ok := g.StackOf(current)
		if !ok {
			return stack.Newf(stack.KindInvalidArgs, "%s left its stack while it was being renamed", current)
		}
		b := &s.Branches[i]
		b.Name = o.Name
		if carry {
			b.RenamedFrom = current
		} else {
			b.PR = nil
		}
		return nil
	})
	if err != nil {
		if res.Remote != "" {
			return res, asStackError(err).WithSteps(fmt.Sprintf("%s is renamed here and on %s, but its stack still lists it as %s; check git stack log", o.Name, remote, current))
		}
		if rerr := a.d.Git.RenameBranch(ctx, repo, o.Name, current); rerr != nil {
			return res, asStackError(err).WithSteps(fmt.Sprintf("the branch is called %s now and putting the old name back failed (%v)", o.Name, rerr))
		}
		return res, asStackError(err).WithSteps("nothing was changed")
	}

	if res.Remote != "" {
		res.Notices = append(res.Notices, a.followRemoteRename(ctx, repo, remote, current, o.Name, pushedTip)...)
		return res, nil
	}
	if o.Local && pushed {
		res.Notices = append(res.Notices, a.leftBehind(ctx, repo, branch, remote, current, o.Name))
	}
	return res, nil
}

// checkNewName refuses a name that's invalid, unchanged or already taken
// by a local branch or a stale stack record.
func (a *App) checkNewName(ctx context.Context, repo git.Repo, graph *stack.Graph, current, name string) error {
	if name == "" {
		return stack.New(stack.KindInvalidArgs, "no new name given").WithSteps(renameCommand + " <name>")
	}
	if name == current {
		return stack.Newf(stack.KindInvalidArgs, "the branch is already called %s", current)
	}
	if err := a.validateBranchName(ctx, name); err != nil {
		return err
	}
	exists, err := a.d.Git.BranchExists(ctx, repo, name)
	if err != nil {
		return err
	}
	if exists || graph.Tracked(name) {
		return stack.Newf(stack.KindInvalidArgs, "branch %s already exists", name)
	}
	return nil
}

// renameRemote renames the branch on the forge, refusing up front when
// the remote already has a branch by the new name as far as we know.
func (a *App) renameRemote(ctx context.Context, repo git.Repo, remote, from, to string) error {
	localOnly := "rename it only on this machine: " + renameCommand + " --local " + to
	if _, taken, err := a.d.Git.Tip(ctx, repo, "refs/remotes/"+remote+"/"+to); err != nil {
		return err
	} else if taken {
		return stack.Newf(stack.KindInvalidArgs, "branch %s already exists on %s", to, remote).
			WithSteps("pick another name", localOnly)
	}
	if a.d.Forge == nil {
		return stack.Newf(stack.KindUnsupported, "no forge configured to rename %s on %s", from, remote).WithSteps(localOnly)
	}
	err := a.progress(ctx, PhaseRename, fmt.Sprintf("Renaming %s on %s", from, remote), func(ctx context.Context) error {
		return a.d.Forge.RenameBranch(ctx, repo, from, to)
	})
	if err != nil {
		return asStackError(err).WithSteps("nothing was renamed", localOnly)
	}
	return nil
}

// followRemoteRename points the local copy at the renamed remote branch:
// the remote tracking ref moves to the new name and an upstream naming the
// old branch is set to the new one. The PR cache names the old branch, so
// it goes and the next look fetches it again. Anything that fails is a
// notice; the rename itself is done.
func (a *App) followRemoteRename(ctx context.Context, repo git.Repo, remote, from, to, pushedTip string) []string {
	var notices []string
	tracking := "refs/remotes/" + remote + "/"
	if pushedTip != "" {
		err := a.d.Git.UpdateRefs(ctx, repo, []git.RefUpdate{
			{Ref: tracking + to, New: pushedTip},
			{Ref: tracking + from, New: strings.Repeat("0", len(pushedTip)), Old: pushedTip},
		})
		if err != nil {
			notices = append(notices, fmt.Sprintf("%s/%s could not be moved to %s/%s (%v); git fetch %s sorts it out", remote, from, remote, to, err, remote))
		}
	}
	key := "branch." + to + ".merge"
	if merge, ok, err := a.d.Git.ConfigGet(ctx, repo, git.ScopeLocal, key); err == nil && ok && merge == "refs/heads/"+from {
		if err := a.d.Git.ConfigSet(ctx, repo, git.ScopeLocal, key, "refs/heads/"+to); err != nil {
			notices = append(notices, fmt.Sprintf("%s still tracks %s/%s (%v); git branch -u %s/%s fixes it", to, remote, from, err, remote, to))
		}
	}
	if a.d.Cache != nil {
		if err := a.d.Cache.Remove(prsCacheName); err != nil {
			a.d.Log.Debug("drop PR cache", "err", err)
		}
	}
	return notices
}

// leftBehind is the notice for a pushed branch renamed only here: what it
// left on the remote under the old name.
func (a *App) leftBehind(ctx context.Context, repo git.Repo, branch stack.Branch, remote, from, to string) string {
	number := 0
	if branch.PR != nil {
		number = branch.PR.Number
	} else if prs, _ := a.loadPRs(ctx, repo, PRsCached); prs != nil {
		if pr, ok := PRsFor(prs)[from]; ok && (pr.State == forge.StateOpen || pr.State == forge.StateDraft) {
			number = pr.Number
		}
	}
	if number != 0 {
		return fmt.Sprintf("#%d stays on %s on %s; the next submit opens a new pull request for %s", number, from, remote, to)
	}
	return fmt.Sprintf("%s is still called %s on %s; the next submit pushes %s as a new branch", to, from, remote, to)
}
