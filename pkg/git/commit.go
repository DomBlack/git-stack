package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// AddMode selects what `git add` stages.
type AddMode int

const (
	// AddNone stages nothing.
	AddNone AddMode = iota
	// AddAll stages all changes including untracked files (git add -A).
	AddAll
	// AddUpdate stages changes to tracked files only (git add -u).
	AddUpdate
	// AddPatch stages hunks interactively (git add -p); needs a TTY.
	AddPatch
)

// Add stages changes according to mode.
func (c *Client) Add(ctx context.Context, repo Repo, mode AddMode) error {
	switch mode {
	case AddNone:
		return nil
	case AddAll:
		_, err := c.gitIn(ctx, repo, "add", "-A")
		return err
	case AddUpdate:
		_, err := c.gitIn(ctx, repo, "add", "-u")
		return err
	case AddPatch:
		return c.Passthrough(ctx, repo, "add", "-p")
	default:
		return fmt.Errorf("unknown add mode %d", int(mode))
	}
}

// HasStagedChanges reports whether the index differs from HEAD.
func (c *Client) HasStagedChanges(ctx context.Context, repo Repo) (bool, error) {
	_, err := c.gitIn(ctx, repo, "diff", "--cached", "--quiet")
	return exitOneIsTrue(err)
}

// HasUnstagedChanges reports whether the working tree has unstaged changes to
// tracked files.
func (c *Client) HasUnstagedChanges(ctx context.Context, repo Repo) (bool, error) {
	_, err := c.gitIn(ctx, repo, "diff", "--quiet")
	return exitOneIsTrue(err)
}

// HasUntrackedFiles reports whether untracked, non-ignored files exist.
func (c *Client) HasUntrackedFiles(ctx context.Context, repo Repo) (bool, error) {
	res, err := c.gitIn(ctx, repo, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return false, err
	}
	return res.Out() != "", nil
}

func exitOneIsTrue(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 1 {
		return true, nil
	}
	return false, err
}

// CommitOptions controls Commit.
type CommitOptions struct {
	// Message paragraphs; each becomes a -m argument. Empty opens the editor
	// unless NoEdit is set.
	Message []string
	Amend   bool
	NoEdit  bool
	// Edit forces the editor even with a message (or when amending).
	Edit        bool
	ResetAuthor bool
	NoVerify    bool
	// AllowEmpty permits a commit with no changes.
	AllowEmpty bool
}

// Commit creates or amends a commit and returns the new HEAD id. When the
// editor must open (no message and not NoEdit, or Edit), the command runs in
// passthrough mode.
func (c *Client) Commit(ctx context.Context, repo Repo, o CommitOptions) (string, error) {
	args := []string{"commit"}
	if o.Amend {
		args = append(args, "--amend")
	}
	for _, m := range o.Message {
		args = append(args, "-m", m)
	}
	if o.NoEdit {
		args = append(args, "--no-edit")
	}
	if o.Edit {
		args = append(args, "--edit")
	}
	if o.ResetAuthor {
		args = append(args, "--reset-author")
	}
	if o.NoVerify {
		args = append(args, "--no-verify")
	}
	if o.AllowEmpty {
		args = append(args, "--allow-empty")
	}
	needsEditor := o.Edit || (len(o.Message) == 0 && !o.NoEdit)
	var err error
	if needsEditor {
		err = c.Passthrough(ctx, repo, args...)
	} else {
		_, err = c.gitIn(ctx, repo, args...)
	}
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", fmt.Errorf("git commit: %s", strings.TrimSpace(ee.Result.Err()))
		}
		return "", err
	}
	return c.RevParse(ctx, repo, "HEAD")
}

// Subject returns the first line of the commit message of rev.
func (c *Client) Subject(ctx context.Context, repo Repo, rev string) (string, error) {
	res, err := c.gitIn(ctx, repo, "log", "-1", "--format=%s", rev)
	if err != nil {
		return "", err
	}
	return res.Out(), nil
}
