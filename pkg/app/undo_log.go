package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// undoMode is how the worktree undo runs in follows its branch back.
type undoMode string

const (
	// undoSoft is for the commands that turned working tree changes into
	// commits (create, modify, absorb): HEAD goes back with reset --soft,
	// so what they committed comes back staged.
	undoSoft undoMode = "soft"
	// undoKeep is for the commands that rewrote history (restack, reorder,
	// move, init, sync): the branch goes back with reset --keep, so local
	// changes come along or the undo refuses.
	undoKeep undoMode = "keep"
)

// undoLimit is how many commands the history keeps, across worktrees.
const undoLimit = 20

// undoRef keeps every commit an entry's before state mentions reachable,
// so garbage collection never takes what undo might need to put back.
const undoRef = "refs/git-stack/undo"

// undoState is everything undo puts back, as it was at one moment.
type undoState struct {
	// Refs maps each branch to its tip, "" when it doesn't exist.
	Refs map[string]string `json:"refs"`
	Meta stack.Snapshot    `json:"meta"`
	// Queue is remote-stacks.json as stored, nil when it doesn't exist.
	Queue     []byte         `json:"queue,omitempty"`
	Worktrees []undoWorktree `json:"worktrees,omitempty"`
}

// undoWorktree is a worktree and what it had checked out.
type undoWorktree struct {
	Path string `json:"path"`
	// Branch is "" when HEAD was detached.
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head"`
}

// undoEntry is one command that can be undone.
type undoEntry struct {
	Command string    `json:"command"`
	At      time.Time `json:"at"`
	// Worktree is where the command ran; only an undo run there sees it.
	Worktree string    `json:"worktree"`
	Mode     undoMode  `json:"mode"`
	Before   undoState `json:"before"`
	After    undoState `json:"after"`
}

// undoLog is the history, newest entry first.
type undoLog struct {
	Entries []undoEntry `json:"entries"`
	// Cleared is set once a submit or merge emptied the history, so an
	// empty one can say why.
	Cleared bool `json:"cleared,omitempty"`
}

// undoLogPath is <common git dir>/git-stack/undo.json; branches are shared
// between worktrees, so the history is too.
func undoLogPath(repo git.Repo) string {
	return filepath.Join(repo.CommonDir, "git-stack", "undo.json")
}

// undoLockWait is how long a change to the history waits for its lock.
var undoLockWait = 5 * time.Second

// loadUndoLog reads the history; a missing file is an empty one.
func loadUndoLog(repo git.Repo) (*undoLog, error) {
	data, err := os.ReadFile(undoLogPath(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return &undoLog{}, nil
	}
	if err != nil {
		return nil, err
	}
	var l undoLog
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", undoLogPath(repo), err)
	}
	return &l, nil
}

// saveUndoLog writes the history atomically.
func saveUndoLog(repo git.Repo, l *undoLog) error {
	p := undoLogPath(repo)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "undo.json.*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// editUndoLog changes the history under its lock: fn edits it, then it is
// trimmed to undoLimit, saved, and the keep alive ref rewritten to match.
func (a *App) editUndoLog(ctx context.Context, repo git.Repo, fn func(*undoLog) error) error {
	p := undoLogPath(repo)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(ctx, filepath.Join(filepath.Dir(p), "undo.lock"), undoLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	l, err := loadUndoLog(repo)
	if err != nil {
		return err
	}
	if err := fn(l); err != nil {
		return err
	}
	if len(l.Entries) > undoLimit {
		l.Entries = l.Entries[:undoLimit]
	}
	if err := saveUndoLog(repo, l); err != nil {
		return err
	}
	return a.writeKeepAlive(ctx, repo, l)
}

// writeKeepAlive points undoRef at a commit whose parents are every distinct
// commit the history's before states mention, or deletes it when there are
// none.
func (a *App) writeKeepAlive(ctx context.Context, repo git.Repo, l *undoLog) error {
	var commits []string
	for _, e := range l.Entries {
		for _, sha := range e.Before.Refs {
			if sha != "" {
				commits = append(commits, sha)
			}
		}
		for _, w := range e.Before.Worktrees {
			if w.Head != "" {
				commits = append(commits, w.Head)
			}
		}
	}
	slices.Sort(commits)
	commits = slices.Compact(commits)
	cur, exists, err := a.d.Git.Tip(ctx, repo, undoRef)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		if !exists {
			return nil
		}
		return a.d.Git.UpdateRefs(ctx, repo, []git.RefUpdate{{Ref: undoRef, Old: cur}})
	}
	sha, err := a.d.Git.EmptyTreeCommit(ctx, repo, commits, "git-stack undo history\n\nKeeps the commits git stack undo may put back.")
	if err != nil {
		return err
	}
	return a.d.Git.UpdateRefs(ctx, repo, []git.RefUpdate{{Ref: undoRef, New: sha}})
}
