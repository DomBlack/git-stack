package git

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// zeroContext is how absorb reads a diff: no context lines, renames
// detected, a/ and b/ prefixes whatever the user's diff settings say, no
// external diff or textconv (the patch has to apply to the real blob), and
// submodules as one commit line rather than a nested diff.
var zeroContext = []string{"-U0", "--find-renames", "--no-color", "--no-ext-diff", "--no-textconv", "--submodule=short", "--src-prefix=a/", "--dst-prefix=b/"}

// DiffCommit returns rev's changes against its first parent as a zero
// context diff with renames detected.
func (c *Client) DiffCommit(ctx context.Context, repo Repo, rev string) (string, error) {
	args := append([]string{"diff"}, zeroContext...)
	res, err := c.gitIn(ctx, repo, append(args, rev+"^", rev, "--")...)
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

// StagedPatch returns the index against HEAD the way DiffCommit reads a
// commit.
func (c *Client) StagedPatch(ctx context.Context, repo Repo) (string, error) {
	args := append([]string{"diff", "--cached"}, zeroContext...)
	res, err := c.gitIn(ctx, repo, append(args, "--")...)
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

// HasMerges reports whether from..to holds a merge commit.
func (c *Client) HasMerges(ctx context.Context, repo Repo, from, to string) (bool, error) {
	res, err := c.gitIn(ctx, repo, "rev-list", "--merges", "-n", "1", from+".."+to, "--")
	if err != nil {
		return false, err
	}
	return res.Out() != "", nil
}

// PatchError is a patch git apply would not put on the tree as given: the
// lines it removes are not where it says, it applied them at an offset, or
// git rejected it for any other reason. Detail is git's output.
type PatchError struct {
	Detail string
}

func (e *PatchError) Error() string { return "patch does not apply: " + lastLine(e.Detail) }

// PatchTree applies patch, a zero context diff, to tree and returns the new
// tree. It goes through a temporary index in the git dir, so the real
// index and working tree are never touched, and the temporary index is
// removed whether or not it worked. git checks every removed line against
// the file (line numbers are only a hint to it), so a patch that doesn't
// fit comes back as a *PatchError and nothing is written. git also applies
// a hunk at an offset when it finds the removed lines elsewhere; a correct
// plan always matches at the stated line, so an offset is refused too.
// Whitespace settings are ignored so the applied lines are exactly the
// patch's.
func (c *Client) PatchTree(ctx context.Context, repo Repo, tree, patch string) (string, error) {
	f, err := os.CreateTemp(repo.GitDir, "git-stack-absorb-*.index")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_ = f.Close()
	// read-tree wants to create the index itself; it only needs the name.
	_ = os.Remove(path)
	defer func() { _ = os.Remove(path) }()
	env := []string{"GIT_INDEX_FILE=" + path}
	if _, err := c.gitInput(ctx, repo, nil, env, "read-tree", tree); err != nil {
		return "", gitFailure("read-tree", err)
	}
	applied, err := c.gitInput(ctx, repo, strings.NewReader(patch), env, "apply", "-v", "--cached", "--unidiff-zero", "--whitespace=nowarn", "-")
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", &PatchError{Detail: strings.TrimSpace(ee.Result.Err())}
		}
		return "", err
	}
	// -v makes git say "Hunk #1 succeeded at 1 (offset -2 lines)".
	if offsetApplied.MatchString(applied.Err()) {
		return "", &PatchError{Detail: strings.TrimSpace(applied.Err())}
	}
	res, err := c.gitInput(ctx, repo, nil, env, "write-tree")
	if err != nil {
		return "", gitFailure("write-tree", err)
	}
	return res.Out(), nil
}

// offsetApplied matches git's "Hunk #1 succeeded at 3 (offset 1 line)". The
// verbose output also names every file, so "offset" alone proves nothing.
var offsetApplied = regexp.MustCompile(`succeeded at \d+ \(offset `)

// gitFailure turns a non-zero exit into an error carrying git's message.
func gitFailure(what string, err error) error {
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return errors.New(what + ": " + strings.TrimSpace(ee.Result.Err()))
	}
	return err
}
