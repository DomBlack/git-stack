// Package cmd wires the cobra command tree. It contains no business logic:
// each file parses flags, decides between interactive and plain output, calls
// pkg/app and renders the result.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/ai/claudecode"
	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/backend/ghstack"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge/github"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/ui"
)

// version is set by the linker (see .goreleaser.yaml).
var version = "dev"

// Streams are the process's standard streams, injectable for tests.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Globals holds the persistent flags.
type Globals struct {
	NoInteractive bool
	Debug         bool
	Quiet         bool
	NoVerify      bool
	Cwd           string
}

// Runtime is everything a command needs, built once after flag parsing.
type Runtime struct {
	Globals Globals
	Streams Streams
	// Interactive is true when prompts and TUIs may be used.
	Interactive bool
	Runner      exec.Runner
	Git         *git.Client
	Log         *slog.Logger

	repoOnce sync.Once
	repo     git.Repo
	repoErr  error
	cfgOnce  sync.Once
	cfg      *config.Config
	cfgErr   error
}

// Repo discovers the repository lazily (from --cwd or the working directory).
func (rt *Runtime) Repo(ctx context.Context) (git.Repo, error) {
	rt.repoOnce.Do(func() {
		dir := rt.Globals.Cwd
		if dir == "" {
			dir, rt.repoErr = os.Getwd()
			if rt.repoErr != nil {
				return
			}
		}
		rt.repo, rt.repoErr = rt.Git.Discover(ctx, dir)
		if errors.Is(rt.repoErr, git.ErrNotRepo) {
			rt.repoErr = stack.Newf(stack.KindNotRepo, "not a git repository: %s", dir)
		}
	})
	return rt.repo, rt.repoErr
}

// Config loads stack.* settings lazily.
func (rt *Runtime) Config(ctx context.Context) (*config.Config, error) {
	rt.cfgOnce.Do(func() {
		repo, err := rt.Repo(ctx)
		if err != nil {
			rt.cfgErr = err
			return
		}
		rt.cfg, rt.cfgErr = config.Load(ctx, rt.Git, repo)
	})
	return rt.cfg, rt.cfgErr
}

// cli owns the command tree and the runtime for one invocation.
type cli struct {
	streams Streams
	globals Globals
	// newRunner builds the subprocess runner; tests inject fakes here.
	newRunner func(opts ...exec.Option) exec.Runner
	rtOnce    sync.Once
	rt        *Runtime
}

// runtime builds the full Runtime (TTY-aware runner, prompts allowed when
// interactive). Call it only from RunE, after flags are parsed.
func (c *cli) runtime() *Runtime {
	c.rtOnce.Do(func() {
		interactive := !c.globals.NoInteractive &&
			os.Getenv("GIT_STACK_NO_INTERACTIVE") == "" &&
			isTerminal(c.streams.In) && isTerminal(c.streams.Out)

		var opts []exec.Option
		if c.globals.Debug {
			opts = append(opts, exec.WithDebug(c.streams.Err))
		}
		if interactive {
			opts = append(opts, exec.WithTTY(exec.TTY{
				In:  c.streams.In.(*os.File),
				Out: c.streams.Out.(*os.File),
				Err: errFile(c.streams.Err),
			}))
		}
		newRunner := c.newRunner
		if newRunner == nil {
			newRunner = exec.New
		}
		runner := newRunner(opts...)
		c.rt = &Runtime{
			Globals:     c.globals,
			Streams:     c.streams,
			Interactive: interactive,
			Runner:      runner,
			Git:         git.New(runner),
			Log:         newLogger(c.streams.Err, c.globals.Debug),
		}
	})
	return c.rt
}

// completionRuntime builds a restricted Runtime for shell completion: never
// interactive, no debug output, and only git may be executed.
func (c *cli) completionRuntime() *Runtime {
	runner := onlyGit{exec.New()}
	return &Runtime{
		Globals: c.globals,
		Streams: c.streams,
		Runner:  runner,
		Git:     git.New(runner),
		Log:     slog.New(slog.DiscardHandler),
	}
}

// app wires the use cases with the real adapters for a command run. This is
// the only place (with cmd/mcp.go) that imports adapters.
func (c *cli) app(ctx context.Context) (*app.App, git.Repo, error) {
	rt := c.runtime()
	repo, err := rt.Repo(ctx)
	if err != nil {
		return nil, git.Repo{}, err
	}
	cfg, err := rt.Config(ctx)
	if err != nil {
		return nil, git.Repo{}, err
	}
	backend := ghstack.New(rt.Runner, rt.Git)
	deps := app.Deps{
		Git:     rt.Git,
		Meta:    backend,
		Tracker: backend,
		Restack: backend,
		Forge:   github.New(rt.Runner),
		AI: claudecode.New(rt.Runner, claudecode.Config{
			Command: cfg.AICommand, Model: cfg.AIModel, ExtraPrompt: cfg.AIExtraPrompt, Timeout: cfg.AITimeout,
		}, rt.Log),
		Cache:  cache.New(repo),
		Config: cfg,
		Log:    rt.Log,
	}
	if rt.Interactive {
		deps.Prompter = ui.Prompter{In: rt.Streams.In, Out: rt.Streams.Err, Ctx: ctx}
		deps.Progress = func(ctx context.Context, message string, fn func(ctx context.Context) error) error {
			return ui.WithSpinner(ctx, rt.Streams.In, rt.Streams.Err, message, fn)
		}
	}
	return app.New(deps), repo, nil
}

// completionApp wires a read-only App for shell completion: metadata comes
// from the backend's local file and only git may run.
func (c *cli) completionApp(ctx context.Context) (*app.App, git.Repo, error) {
	rt := c.completionRuntime()
	repo, err := rt.Repo(ctx)
	if err != nil {
		return nil, git.Repo{}, err
	}
	return app.New(app.Deps{
		Git:   rt.Git,
		Meta:  ghstack.New(rt.Runner, rt.Git),
		Cache: cache.New(repo),
		Log:   rt.Log,
	}), repo, nil
}

// onlyGit refuses to run anything but git; used for completion.
type onlyGit struct{ exec.Runner }

func (o onlyGit) Run(ctx context.Context, cmd exec.Cmd) (exec.Result, error) {
	if cmd.Name != "git" || cmd.Mode != exec.Capture {
		return exec.Result{ExitCode: -1}, fmt.Errorf("completion may only run git, not %q", cmd.Name)
	}
	return o.Runner.Run(ctx, cmd)
}

func newLogger(w io.Writer, debug bool) *slog.Logger {
	level := slog.LevelWarn
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func errFile(w io.Writer) *os.File {
	if f, ok := w.(*os.File); ok {
		return f
	}
	return os.Stderr
}

// NewRootCmd builds the command tree bound to the given streams.
func NewRootCmd(streams Streams) *cobra.Command {
	return newRootCmd(&cli{streams: streams})
}

func newRootCmd(c *cli) *cobra.Command {
	streams := c.streams
	root := &cobra.Command{
		Use:     "git-stack",
		Short:   "Graphite-style stacked branches on top of gh stack",
		Long:    "git-stack manages stacked branches and stacked pull requests.\nInstalled as git-stack, git exposes it as `git stack <command>`.",
		Version: version,
		Args:    cobra.NoArgs,
		// Every command sets ValidArgsFunction; the root has no positional args.
		ValidArgsFunction: completeNothing,
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			if c.globals.Cwd != "" {
				c.globals.Cwd = filepath.Clean(c.globals.Cwd)
			}
		},
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetVersionTemplate("git-stack {{.Version}}\n")

	pf := root.PersistentFlags()
	pf.BoolVar(&c.globals.NoInteractive, "no-interactive", false, "never prompt or open a TUI (implied when stdin/stdout is not a terminal)")
	pf.BoolVar(&c.globals.Debug, "debug", false, "log every subprocess to stderr")
	pf.BoolVarP(&c.globals.Quiet, "quiet", "q", false, "minimise output; implies --no-interactive")
	pf.BoolVar(&c.globals.NoVerify, "no-verify", false, "skip git hooks when committing")
	pf.StringVar(&c.globals.Cwd, "cwd", "", "run as if started in this directory")
	must(root.RegisterFlagCompletionFunc("cwd", completeDirs))

	root.AddCommand(
		newCreateCmd(c),
		newModifyCmd(c),
		newRestackCmd(c),
		newUpCmd(c),
		newDownCmd(c),
		newTopCmd(c),
		newBottomCmd(c),
		newCheckoutCmd(c),
		newCompletionCmd(c),
	)
	return root
}

// Execute runs the CLI with the process streams and returns the exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := NewRootCmd(Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
	if err := root.ExecuteContext(ctx); err != nil {
		printError(os.Stderr, err)
		return exitCode(err)
	}
	return 0
}

// printError renders an error Graphite-style: one line, then next steps.
func printError(w io.Writer, err error) {
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(w, "interrupted")
		return
	}
	if se, ok := errors.AsType[*stack.Error](err); ok {
		fmt.Fprintf(w, "error: %s\n", se.Msg)
		if se.Detail != "" {
			for line := range strings.SplitSeq(se.Detail, "\n") {
				fmt.Fprintf(w, "  %s\n", line)
			}
		}
		for _, step := range se.NextSteps {
			fmt.Fprintf(w, "  - %s\n", step)
		}
		return
	}
	fmt.Fprintf(w, "error: %v\n", err)
}

// exitCode mirrors gh-stack for the codes scripts care about.
func exitCode(err error) int {
	if errors.Is(err, context.Canceled) {
		return 130
	}
	if se, ok := errors.AsType[*stack.Error](err); ok {
		switch se.Kind {
		case stack.KindNotInStack, stack.KindNotRepo:
			return 2
		case stack.KindConflict:
			return 3
		}
	}
	return 1
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
