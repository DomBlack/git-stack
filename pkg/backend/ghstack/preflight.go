package ghstack

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// extensionName is how `gh extension list` reports gh-stack.
const extensionName = "github/gh-stack"

var preflightOnce sync.Map // map[*Backend]error

// Preflight verifies gh and the gh-stack extension are installed. The
// result is cached per Backend for the life of the process.
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
	res, err := b.run.Run(ctx, exec.Cmd{Name: "gh", Args: []string{"extension", "list"}, Env: ghEnv, Unset: ghUnset})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		if _, ok := errors.AsType[*exec.NotFoundError](err); ok {
			return stack.New(stack.KindNotInstalled, "gh (GitHub CLI) is not installed").
				WithSteps("install it: https://cli.github.com", "then: gh auth login && gh extension install github/gh-stack").
				WithCause(err)
		}
		return mapError([]string{"extension"}, err)
	}
	for line := range strings.SplitSeq(res.Out(), "\n") {
		if strings.Contains(line, extensionName) {
			return nil
		}
	}
	return stack.New(stack.KindNotInstalled, "the gh-stack extension is not installed").
		WithSteps("run: gh extension install github/gh-stack")
}
