package ghstack

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Backend implements the stack ports via gh-stack.
type Backend struct {
	out io.Writer // live output for submit/sync, see WithOutput
	run exec.Runner
	git *git.Client
}

// Option configures a Backend.
type Option func(*Backend)

// WithOutput relays the output of long running gh stack commands (submit,
// sync) to w as it is produced instead of only returning it afterwards.
// The CLI sets this to stderr; the MCP server never does.
func WithOutput(w io.Writer) Option {
	return func(b *Backend) { b.out = w }
}

// New returns a Backend that runs gh through r.
func New(r exec.Runner, g *git.Client, opts ...Option) *Backend {
	b := &Backend{run: r, git: g}
	for _, o := range opts {
		o(b)
	}
	return b
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
// gh runs `gh stack <args>` captured and maps failures to stack.Error.
func (b *Backend) gh(ctx context.Context, repo git.Repo, args ...string) error {
	_, err := b.ghRun(ctx, repo, nil, args...)
	return err
}

// ghRun is gh with the captured stderr also relayed live to stream when it
// is non-nil (the long submit and sync commands pass the WithOutput writer).
func (b *Backend) ghRun(ctx context.Context, repo git.Repo, stream io.Writer, args ...string) (exec.Result, error) {
	res, err := b.run.Run(ctx, exec.Cmd{
		Name:   "gh",
		Args:   append([]string{"stack"}, args...),
		Dir:    repo.TopLevel,
		Env:    ghEnv,
		Unset:  ghUnset,
		Stream: stream,
	})
	if err != nil {
		return res, mapError(args, err)
	}
	return res, nil
}
