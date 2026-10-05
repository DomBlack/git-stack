package app

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/DomBlack/git-stack/pkg/git"
)

// restackState is an interrupted restack: the conflict git is holding, what
// is left to do above it and what was already moved, so that continue can
// finish the job and abort can undo all of it. It lives in the worktree's
// git dir, like git's own rebase state, and is written only from this
// package.
type restackState struct {
	// Command is the CLI command that started it, for messages
	// ("git stack restack" or "git stack modify").
	Command string `json:"command"`
	// OriginalBranch was checked out when the operation started.
	OriginalBranch string `json:"original_branch"`
	Trunk          string `json:"trunk"`
	// Conflict is the branch whose git rebase is in progress, Onto its
	// parent, NewBase the parent tip it is being rebased onto, ConflictTip
	// and ConflictOldBase its tip and recorded base before the rebase.
	Conflict        string `json:"conflict"`
	Onto            string `json:"onto"`
	NewBase         string `json:"new_base"`
	ConflictTip     string `json:"conflict_tip"`
	ConflictOldBase string `json:"conflict_old_base,omitempty"`
	// Remaining are the branches above Conflict still to restack, bottom
	// first.
	Remaining []string `json:"remaining"`
	// Moved are the branches this operation has already moved.
	Moved []movedRef `json:"moved"`
}

// movedRef is one branch move, with what abort needs to undo it.
type movedRef struct {
	Name    string `json:"name"`
	From    string `json:"from"`
	To      string `json:"to"`
	OldBase string `json:"old_base,omitempty"`
	// Worktree is where the branch was checked out when it moved, if anywhere.
	Worktree string `json:"worktree,omitempty"`
}

// restackStatePath is <git-dir>/git-stack/restack.json.
func restackStatePath(repo git.Repo) string {
	return filepath.Join(repo.GitDir, "git-stack", "restack.json")
}

// loadRestackState reads the state, or returns nil when there is none.
func loadRestackState(repo git.Repo) (*restackState, error) {
	data, err := os.ReadFile(restackStatePath(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s restackState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", restackStatePath(repo), err)
	}
	return &s, nil
}

// saveRestackState writes the state atomically.
func saveRestackState(repo git.Repo, s *restackState) error {
	p := restackStatePath(repo)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// clearRestackState removes the state; a missing file is fine.
func clearRestackState(repo git.Repo) error {
	err := os.Remove(restackStatePath(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ghRebaseStateExists reports whether gh stack has its own interrupted
// rebase recorded for this worktree, which a user who upgraded mid
// conflict may still have.
func ghRebaseStateExists(repo git.Repo) bool {
	_, err := os.Stat(filepath.Join(repo.GitDir, "gh-stack-rebase-state"))
	return err == nil
}
