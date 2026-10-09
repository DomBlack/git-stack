package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// HasStagedChanges reports whether the index differs from HEAD. Like every
// read that may run against another worktree, it passes
// --no-optional-locks so it never takes (or trips over) that worktree's
// index lock just to refresh stat data.
func (c *Client) HasStagedChanges(ctx context.Context, repo Repo) (bool, error) {
	_, err := c.gitIn(ctx, repo, "--no-optional-locks", "diff", "--cached", "--quiet")
	return exitOneIsTrue(err)
}

// HasUnstagedChanges reports whether the working tree has unstaged changes to
// tracked files.
func (c *Client) HasUnstagedChanges(ctx context.Context, repo Repo) (bool, error) {
	_, err := c.gitIn(ctx, repo, "--no-optional-locks", "diff", "--quiet")
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
			stderr := strings.TrimSpace(ee.Result.Err())
			if strings.Contains(stderr, signingFailedMarker) {
				if se := c.signingError(ctx, repo, stderr); se != nil {
					return "", se
				}
			}
			return "", fmt.Errorf("git commit: %s", stderr)
		}
		return "", err
	}
	return c.RevParse(ctx, repo, "HEAD")
}

// signingFailedMarker is what git prints when the signing program fails,
// whatever gpg.format is; the program's own output comes before it. Git
// prints the same line when it can't write the object at all (a read only
// object store), so it only means signing when signing is turned on.
const signingFailedMarker = "failed to write commit object"

// SigningError reports that git could not sign a commit. Nothing was
// committed. From Commit the index is untouched; from a rebase (RebaseOnto,
// RebaseContinue) the rebase is left in progress with the failed pick's
// changes staged, for RebaseContinue to commit once signing works or
// RebaseAbort to drop.
type SigningError struct {
	// Format is gpg.format: ssh, openpgp (the default) or x509.
	Format string
	// Key is user.signingkey as configured, which for ssh is usually the
	// path of the public key file; empty when unset.
	Key string
	// Detail is git's stderr, including the signing program's output.
	Detail string
}

func (e *SigningError) Error() string {
	return "git commit: signing failed: " + lastLine(e.Detail)
}

// signingError builds a SigningError from git's stderr, reading the signing
// configuration so the caller can say which key to unlock. It returns nil
// when signing is off, as then the failure was writing the object itself.
func (c *Client) signingError(ctx context.Context, repo Repo, stderr string) error {
	// Let git read the boolean: it takes yes/on/1 and a bare `gpgsign`
	// with no value as true. Unset exits 1, which is false here too.
	if res, err := c.config(ctx, repo, ScopeMerged, "--type=bool", "--get", "commit.gpgsign"); err != nil || res.Out() != "true" {
		return nil
	}
	e := &SigningError{Format: "openpgp", Detail: stderr}
	if v, ok, err := c.ConfigGet(ctx, repo, ScopeMerged, "gpg.format"); err == nil && ok && v != "" {
		e.Format = v
	}
	if v, ok, err := c.ConfigGet(ctx, repo, ScopeMerged, "user.signingkey"); err == nil && ok {
		e.Key = v
		// For ssh the value is a key file when one exists at that path,
		// relative to the repository where git runs the signer, and a
		// literal key otherwise. Resolve a file so the path means the
		// same from wherever the user is.
		if e.Format == "ssh" && v != "" && !filepath.IsAbs(v) {
			if p := filepath.Join(repo.TopLevel, v); fileExists(p) {
				e.Key = p
			}
		}
	}
	return e
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

// Subject returns the first line of the commit message of rev.
func (c *Client) Subject(ctx context.Context, repo Repo, rev string) (string, error) {
	res, err := c.gitIn(ctx, repo, "log", "-1", "--format=%s", rev)
	if err != nil {
		return "", err
	}
	return res.Out(), nil
}
