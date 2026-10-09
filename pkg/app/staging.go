package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/shell"
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

// commitError maps the ways git commit fails that the user can act on: a
// missing TTY for an editor-backed commit (editorHint says how to pass the
// message instead) and a signing failure.
func commitError(err error, editorHint string) error {
	if errors.Is(err, exec.ErrTTYUnavailable) {
		return stack.New(stack.KindInteractionRequired, "a commit message editor is needed but no terminal is available").
			WithSteps(editorHint)
	}
	if se, ok := errors.AsType[*git.SigningError](err); ok {
		return signingError(se)
	}
	return err
}

// rebaseError maps a failed git rebase from a restack. A signing failure
// leaves the rebase in progress with the commit rescheduled, so the next
// steps are the usual continue and abort once signing works.
func rebaseError(err error) error {
	if se, ok := errors.AsType[*git.SigningError](err); ok {
		return asStackError(signingError(se)).WithSteps(
			"then `git stack continue` to carry on the restack",
			"or `git stack abort` to put the moved branches back")
	}
	return err
}

// signingError explains a signing failure. The common cause with an SSH key
// is a passphrase protected key that isn't in ssh-agent: without a terminal
// (the MCP server, a script) nothing can answer the passphrase prompt, so
// git fails instead. With gpg it is usually an agent whose pinentry can't
// prompt either.
func signingError(se *git.SigningError) error {
	var e *stack.Error
	switch se.Format {
	case "ssh":
		if se.Key == "" {
			e = stack.New(stack.KindSigningFailed, "commit signing failed: gpg.format is ssh but user.signingkey is not set").
				WithSteps("set user.signingkey to the public key file to sign with")
		} else {
			// The private key sits next to the configured .pub; quote it
			// so the line can be pasted (a path a shell can't take, one
			// with a control character, gets described instead).
			// git resolved a key file to an absolute path (or ~); anything
			// else is a literal key, which can't be ssh-added by name.
			step := "load the signing key into ssh-agent with ssh-add"
			if filepath.IsAbs(se.Key) || strings.HasPrefix(se.Key, "~") {
				if q, ok := shell.Path(strings.TrimSuffix(se.Key, ".pub")); ok {
					step = "load the key into ssh-agent: ssh-add " + q
				} else {
					step = "load the private key next to " + se.Key + " into ssh-agent with ssh-add"
				}
			}
			e = stack.Newf(stack.KindSigningFailed, "commit signing failed: the SSH key %s could not be used (is it unlocked in ssh-agent?)", se.Key).
				WithSteps(step)
		}
	case "x509":
		e = stack.New(stack.KindSigningFailed, "commit signing failed: the X.509 signer could not sign the commit (it may need a terminal to ask for the passphrase)").
			WithSteps("check that the signer (gpg.x509.program, gpgsm by default) can sign without a prompt")
	default:
		e = stack.New(stack.KindSigningFailed, "commit signing failed: gpg could not sign the commit (its agent may need a terminal to ask for the passphrase)").
			WithSteps("check that gpg can sign without a prompt: echo test | gpg --batch --clearsign")
	}
	return e.WithSteps("or turn signing off for this repository: git config commit.gpgsign false").
		WithDetail(se.Detail).WithCause(se)
}
