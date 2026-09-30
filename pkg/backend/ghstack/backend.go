package ghstack

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Backend implements the stack ports via gh-stack.
type Backend struct {
	run exec.Runner
	git *git.Client
}

// New returns a Backend that runs gh through r.
func New(r exec.Runner, g *git.Client) *Backend {
	return &Backend{run: r, git: g}
}

// Compile-time port checks.
var _ stack.Metadata = (*Backend)(nil)

// Load implements stack.Metadata by reading <git-dir>/gh-stack. A missing
// file means no stacks; it never runs gh.
func (b *Backend) Load(_ context.Context, repo git.Repo) (*stack.Graph, error) {
	data, err := os.ReadFile(filepath.Join(repo.GitDir, FileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return stack.NewGraph(nil), nil
		}
		return nil, err
	}
	f, err := parseFile(data)
	if err != nil {
		return nil, err
	}
	return f.toGraph(), nil
}

// ghEnv keeps gh-stack non-interactive and its output plain when captured.
var ghEnv = []string{
	"GH_NO_UPDATE_NOTIFIER=1",
	"GH_PROMPT_DISABLED=1",
	"NO_COLOR=1",
	"CLICOLOR=0",
}

// ghUnset removes variables that would re-enable prompts or TUIs.
var ghUnset = []string{"GH_FORCE_TTY", "CLICOLOR_FORCE"}

// gh runs `gh stack <args>` captured, mapping failures to *stack.Error.
// The result carries gh-stack's output for commands that relay it.
func (b *Backend) gh(ctx context.Context, repo git.Repo, args ...string) (exec.Result, error) {
	res, err := b.run.Run(ctx, exec.Cmd{
		Name:  "gh",
		Args:  append([]string{"stack"}, args...),
		Dir:   repo.TopLevel,
		Env:   ghEnv,
		Unset: ghUnset,
	})
	if err != nil {
		return res, mapError(args, err)
	}
	return res, nil
}
