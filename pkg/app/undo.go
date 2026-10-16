package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// UndoOptions controls Undo.
type UndoOptions struct {
	// List shows this worktree's history without changing anything.
	List bool
	// Force undoes even when branches moved since the command, throwing
	// away what happened on them. Working tree changes are never thrown
	// away.
	Force bool
}

// UndoEntryView is one command undo can take back.
type UndoEntryView struct {
	Command string    `json:"command"`
	At      time.Time `json:"at"`
	// Restores lists what undoing it puts back, one short line each.
	Restores []string `json:"restores"`
}

// UndoResult reports what Undo did, or with List what it could do.
type UndoResult struct {
	Undone *UndoEntryView `json:"undone,omitempty"`
	// Entries is the history, newest first (List only).
	Entries []UndoEntryView `json:"entries,omitempty"`
	// Staged is true when the undone command's changes are back as staged
	// changes (create, modify, absorb).
	Staged  bool     `json:"staged,omitempty"`
	Notices []string `json:"notices,omitempty"`
}

// Undo reverses the newest command recorded in this worktree: every branch
// it covered goes back to its tip from before, the metadata and the GitHub
// queue are put back byte for byte, and checked out branches follow, softly
// (changes come back staged) or keeping local changes, by the command's
// mode. Nothing moves when anything would refuse.
func (a *App) Undo(ctx context.Context, repo git.Repo, o UndoOptions) (UndoResult, error) {
	if o.List {
		l, err := loadUndoLog(repo)
		if err != nil {
			return UndoResult{}, err
		}
		var res UndoResult
		for _, e := range l.Entries {
			if e.Worktree == repo.TopLevel {
				res.Entries = append(res.Entries, entryView(e, repo.TopLevel))
			}
		}
		return res, nil
	}
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return UndoResult{}, err
	}
	var res UndoResult
	err := a.editUndoLog(ctx, repo, func(l *undoLog) error {
		i := slices.IndexFunc(l.Entries, func(e undoEntry) bool { return e.Worktree == repo.TopLevel })
		if i < 0 {
			if l.Cleared {
				return stack.New(stack.KindInvalidArgs, "nothing to undo since the last submit or merge")
			}
			return stack.New(stack.KindInvalidArgs, "nothing to undo yet")
		}
		e := l.Entries[i]
		v := entryView(e, repo.TopLevel)
		staged, notices, err := a.undoEntry(ctx, repo, e, o.Force)
		if err != nil {
			return err
		}
		res.Undone, res.Staged, res.Notices = &v, staged, notices
		l.Entries = slices.Delete(l.Entries, i, i+1)
		return nil
	})
	return res, err
}

// worktreeMove is how one worktree follows its branch back.
type worktreeMove struct {
	path         string
	mode         undoMode
	fromBranch   string // "" when detached
	from         string // HEAD now
	branch, tip  string // what to check out, and where
	switchBranch bool
}

// undoEntry puts e's before state back. It checks everything that could
// refuse first, so a refusal changes nothing.
func (a *App) undoEntry(ctx context.Context, repo git.Repo, e undoEntry, force bool) (staged bool, notices []string, err error) {
	local, err := a.localBranches(ctx, repo)
	if err != nil {
		return false, nil, err
	}
	if !force {
		var moved []string
		for _, n := range slices.Sorted(maps.Keys(e.After.Refs)) {
			if local[n].Head != e.After.Refs[n] {
				moved = append(moved, n)
			}
		}
		if len(moved) > 0 {
			return false, nil, stack.Newf(stack.KindInvalidArgs, "%s moved since %s, so undoing it would lose that", namesUpTo(moved, 3), e.Command).
				WithSteps("git stack undo --force throws away what changed on them since (your uncommitted changes stay)")
		}
	}
	moves, err := a.planWorktreeMoves(ctx, repo, e, local)
	if err != nil {
		return false, nil, err
	}

	// Branches not checked out anywhere move in one transaction; the ones
	// that are move with their worktree, or after it has left them.
	var updates []git.RefUpdate
	var later []string
	for _, n := range slices.Sorted(maps.Keys(e.Before.Refs)) {
		want, cur := e.Before.Refs[n], local[n].Head
		if want == cur {
			continue
		}
		if local[n].Worktree != "" {
			later = append(later, n)
			continue
		}
		if want == "" {
			updates = append(updates, git.RefUpdate{Ref: "refs/heads/" + n, Old: cur})
		} else {
			updates = append(updates, git.RefUpdate{Ref: "refs/heads/" + n, New: want, Old: cur})
		}
	}
	if err := a.d.Git.UpdateRefs(ctx, repo, updates); err != nil {
		return false, nil, stack.New(stack.KindInvalidArgs, "a branch moved while undo was putting it back, so nothing was changed").
			WithSteps("run git stack undo again").WithCause(err)
	}
	if err := a.d.Meta.Restore(ctx, repo, e.Before.Meta); err != nil {
		return false, nil, err
	}
	if err := restoreQueue(repo, e.Before.Queue); err != nil {
		return false, nil, err
	}
	for _, m := range moves {
		wr := worktreeRepo(repo, m.path)
		switch m.mode {
		case undoSoft:
			if m.switchBranch {
				err = a.d.Git.PointHEAD(ctx, wr, m.branch)
			}
			if err == nil {
				err = a.d.Git.ResetSoft(ctx, wr, m.tip)
			}
		default:
			if m.switchBranch {
				err = a.d.Git.Switch(ctx, wr, m.branch)
			}
			if err == nil {
				err = a.d.Git.ResetKeep(ctx, wr, m.tip)
			}
		}
		if err != nil {
			return false, nil, fmt.Errorf("putting %s back in %s: %w", m.branch, shortPath(m.path), err)
		}
		if m.path == repo.TopLevel && m.mode == undoSoft {
			if staged, err = a.d.Git.HasStagedChanges(ctx, wr); err != nil {
				return false, nil, err
			}
		}
	}

	// The branches a worktree has just left (or one undo couldn't plan for).
	if len(later) > 0 {
		now, err := a.localBranches(ctx, repo)
		if err != nil {
			return staged, nil, err
		}
		updates = updates[:0]
		for _, n := range later {
			want, cur := e.Before.Refs[n], now[n].Head
			switch {
			case want == cur:
			case now[n].Worktree != "":
				notices = append(notices, fmt.Sprintf("%s is checked out in %s and was left where it is", n, shortPath(now[n].Worktree)))
			case want == "":
				updates = append(updates, git.RefUpdate{Ref: "refs/heads/" + n, Old: cur})
			default:
				updates = append(updates, git.RefUpdate{Ref: "refs/heads/" + n, New: want, Old: cur})
			}
		}
		if err := a.d.Git.UpdateRefs(ctx, repo, updates); err != nil {
			return staged, notices, err
		}
	}
	return staged, notices, nil
}

// planWorktreeMoves works out how each worktree follows its branch back,
// and refuses, before anything moves, when local changes in a worktree that
// keeps them are in the way.
func (a *App) planWorktreeMoves(ctx context.Context, repo git.Repo, e undoEntry, local map[string]git.Branch) ([]worktreeMove, error) {
	wts, err := a.d.Git.Worktrees(ctx, repo)
	if err != nil {
		return nil, err
	}
	var moves []worktreeMove
	for _, w := range wts {
		if w.Bare || w.Prunable {
			continue
		}
		branch := w.Branch
		if i := slices.IndexFunc(e.Before.Worktrees, func(b undoWorktree) bool { return b.Path == w.Path }); i >= 0 {
			branch = e.Before.Worktrees[i].Branch
		}
		if branch == "" {
			continue // detached before: nothing to put back
		}
		tip, ok := e.Before.Refs[branch]
		if !ok {
			tip = local[branch].Head
		}
		if tip == "" || (branch == w.Branch && tip == w.Head) {
			continue
		}
		m := worktreeMove{path: w.Path, mode: undoKeep, fromBranch: w.Branch, from: w.Head, branch: branch, tip: tip, switchBranch: branch != w.Branch}
		if w.Path == repo.TopLevel {
			m.mode = e.Mode
		}
		if m.mode == undoKeep {
			if err := a.checkKeepMove(ctx, repo, m); err != nil {
				return nil, err
			}
		}
		moves = append(moves, m)
	}
	return moves, nil
}

// checkKeepMove refuses a move whose reset --keep (or switch) would fail
// because of local changes or untracked files in the way.
func (a *App) checkKeepMove(ctx context.Context, repo git.Repo, m worktreeMove) error {
	wr := worktreeRepo(repo, m.path)
	changes, err := a.d.Git.LocalChanges(ctx, wr)
	if err != nil {
		return err
	}
	var clash []string
	if len(changes) > 0 {
		changed, err := a.d.Git.ChangedPaths(ctx, repo, m.from, m.tip)
		if err != nil {
			return err
		}
		for _, f := range changes {
			if slices.Contains(changed, f) {
				clash = append(clash, f)
			}
		}
	}
	untracked, err := a.untrackedInTheWay(ctx, repo, m.path, m.from, m.tip)
	if err != nil {
		return err
	}
	clash = append(clash, untracked...)
	if len(clash) == 0 {
		return nil
	}
	where := "here"
	if m.path != repo.TopLevel {
		where = "in " + shortPath(m.path)
	}
	return stack.Newf(stack.KindInvalidArgs, "putting %s back would overwrite your changes to %s %s", m.branch, namesUpTo(clash, 3), where).
		WithSteps("commit or stash them, then git stack undo again")
}

// restoreQueue writes remote-stacks.json back as it was, or removes it.
func restoreQueue(repo git.Repo, data []byte) error {
	p := remoteStacksPath(repo)
	if len(data) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// entryView describes e for the history and the result: what undoing it
// puts back, one short line each, from the worktree at top's point of view.
func entryView(e undoEntry, top string) UndoEntryView {
	v := UndoEntryView{Command: e.Command, At: e.At}
	for _, n := range slices.Sorted(maps.Keys(e.Before.Refs)) {
		before, after := e.Before.Refs[n], e.After.Refs[n]
		switch {
		case before == after:
		case before == "":
			v.Restores = append(v.Restores, n+" deleted")
		case after == "":
			v.Restores = append(v.Restores, n+" recreated")
		default:
			v.Restores = append(v.Restores, n+" moved back")
		}
	}
	if len(v.Restores) == 0 && !maps.EqualFunc(e.Before.Meta.Files, e.After.Meta.Files, bytes.Equal) {
		v.Restores = append(v.Restores, "the stack metadata put back")
	}
	if !bytes.Equal(e.Before.Queue, e.After.Queue) {
		v.Restores = append(v.Restores, "the stacks queued for GitHub put back")
	}
	bi := slices.IndexFunc(e.Before.Worktrees, func(w undoWorktree) bool { return w.Path == top })
	ai := slices.IndexFunc(e.After.Worktrees, func(w undoWorktree) bool { return w.Path == top })
	if bi >= 0 && ai >= 0 && e.Before.Worktrees[bi].Branch != "" && e.Before.Worktrees[bi].Branch != e.After.Worktrees[ai].Branch {
		v.Restores = append(v.Restores, "back on "+e.Before.Worktrees[bi].Branch)
	}
	return v
}

// namesUpTo joins up to n names, then "and N more".
func namesUpTo(names []string, n int) string {
	if len(names) <= n {
		return joinNames(names)
	}
	return fmt.Sprintf("%s and %d more", joinNames(names[:n]), len(names)-n)
}
