package app

import (
	"context"
	"errors"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// StagingMode mirrors Graphite's -a / -u / -p flags.
type StagingMode int

const (
	StageNone StagingMode = iota
	StageAll
	StageUpdate
	StagePatch
)

// CommitInfo identifies a commit for output.
type CommitInfo struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// Short returns the abbreviated id.
func (c CommitInfo) Short() string {
	if len(c.SHA) > 7 {
		return c.SHA[:7]
	}
	return c.SHA
}

// stage applies the staging mode and, when nothing ends up staged but the
// working tree has changes, offers (interactively) to stage everything, as
// Graphite does. It reports whether anything is staged afterwards.
func (a *App) stage(ctx context.Context, repo git.Repo, mode StagingMode) (bool, error) {
	var gitMode git.AddMode
	switch mode {
	case StageAll:
		gitMode = git.AddAll
	case StageUpdate:
		gitMode = git.AddUpdate
	case StagePatch:
		gitMode = git.AddPatch
	}
	if err := a.d.Git.Add(ctx, repo, gitMode); err != nil {
		if errors.Is(err, exec.ErrTTYUnavailable) {
			return false, stack.New(stack.KindInteractionRequired, "--patch needs a terminal").
				WithSteps("use -a or -u, or stage with git add first")
		}
		return false, err
	}
	staged, err := a.d.Git.HasStagedChanges(ctx, repo)
	if err != nil || staged || mode != StageNone || a.d.Prompter == nil {
		return staged, err
	}
	unstaged, err := a.d.Git.HasUnstagedChanges(ctx, repo)
	if err != nil {
		return false, err
	}
	untracked, err := a.d.Git.HasUntrackedFiles(ctx, repo)
	if err != nil {
		return false, err
	}
	if !unstaged && !untracked {
		return false, nil
	}
	yes, err := a.d.Prompter.Confirm("You have unstaged changes. Stage all of them?", true)
	if err != nil || !yes {
		return false, err
	}
	if err := a.d.Git.Add(ctx, repo, git.AddAll); err != nil {
		return false, err
	}
	return a.d.Git.HasStagedChanges(ctx, repo)
}

func (a *App) commitInfo(ctx context.Context, repo git.Repo, sha string) CommitInfo {
	subject, err := a.d.Git.Subject(ctx, repo, sha)
	if err != nil {
		a.d.Log.Debug("read commit subject", "err", err)
	}
	return CommitInfo{SHA: sha, Subject: subject}
}

// editorError maps a missing TTY for an editor-backed commit.
func editorError(err error, hint string) error {
	if errors.Is(err, exec.ErrTTYUnavailable) {
		return stack.New(stack.KindInteractionRequired, "a commit message editor is needed but no terminal is available").
			WithSteps(hint)
	}
	return err
}
