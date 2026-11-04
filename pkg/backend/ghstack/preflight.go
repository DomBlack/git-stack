package ghstack

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/version"
)

// extensionName is how `gh extension list` reports gh-stack.
const extensionName = "github/gh-stack"

// MinVersion is the oldest gh-stack this adapter works with: 0.3.0 keeps
// one catalog for every worktree in the common git dir, which is the only
// layout we read and write.
const MinVersion = "v0.3.0"

var preflightOnce sync.Map // map[*Backend]error

// Preflight verifies gh and the gh-stack extension are installed and that
// gh-stack is new enough. The result is cached per Backend for the life of
// the process.
func (b *Backend) Preflight(ctx context.Context) error {
	if v, ok := preflightOnce.Load(b); ok {
		err, _ := v.(error)
		return err
	}
	err := b.preflight(ctx)
	preflightOnce.Store(b, err)
	return err
}

func (b *Backend) preflight(ctx context.Context) error {
	v, err := b.Extension(ctx)
	if err != nil {
		return err
	}
	if TooOld(v) {
		return stack.Newf(stack.KindNotInstalled, "gh-stack %s is too old; git-stack needs %s or later", v, MinVersion).
			WithSteps("run: gh extension upgrade stack", "or run `git stack doctor --fix`")
	}
	return nil
}

// TooOld reports whether an installed gh-stack version is older than
// MinVersion. Anything that isn't a release (a local build) is let through.
func TooOld(v string) bool {
	return version.IsRelease(v) && version.Compare(v, MinVersion) < 0
}

// Extension reports the installed gh-stack's version as `gh extension list`
// shows it ("v0.3.0", or a commit for a local build; "" when it shows none).
// It is a KindNotInstalled error when gh or the extension is missing.
func (b *Backend) Extension(ctx context.Context) (string, error) {
	res, err := b.run.Run(ctx, exec.Cmd{Name: "gh", Args: []string{"extension", "list"}, Env: ghEnv, Unset: ghUnset})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		if _, ok := errors.AsType[*exec.NotFoundError](err); ok {
			return "", stack.New(stack.KindNotInstalled, "gh (GitHub CLI) is not installed").
				WithSteps("install it: https://cli.github.com", "then: gh auth login && gh extension install github/gh-stack").
				WithCause(err)
		}
		return "", mapError([]string{"extension"}, err)
	}
	for line := range strings.SplitSeq(res.Out(), "\n") {
		if !strings.Contains(line, extensionName) {
			continue
		}
		fields := strings.Split(line, "\t")
		for i, f := range fields {
			if strings.TrimSpace(f) == extensionName && i+1 < len(fields) {
				return strings.TrimSpace(fields[i+1]), nil
			}
		}
		return "", nil
	}
	return "", stack.New(stack.KindNotInstalled, "the gh-stack extension is not installed").
		WithSteps("run: gh extension install github/gh-stack")
}
