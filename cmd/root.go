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
	"github.com/DomBlack/git-stack/pkg/update"
	"github.com/DomBlack/git-stack/pkg/version"
)

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
	// Report prints every human facing line (docs/style.md).
	Report *ui.Reporter

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
	// updateClient and updateTarget override the release source and the
	// binary that `update` replaces; tests inject these.
	updateClient *update.Client
	updateTarget string
	// updateChecker and current override the background release check and
	// the version it compares against; tests inject these.
	updateChecker *update.Checker
	current       version.Info
	check         *update.Check
	rtOnce        sync.Once
	rt            *Runtime
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
			Report: ui.NewReporter(c.streams.In, c.streams.Out, c.streams.Err, ui.ReporterOptions{
				OutTTY: isTerminal(c.streams.Out), ErrTTY: isTerminal(c.streams.Err), Spinners: interactive, Quiet: c.globals.Quiet,
				TerminalStatus: os.Getenv(noTerminalStatusEnv) == "", Tmux: os.Getenv("TMUX") != "",
			}),
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
	// Long gh stack commands relay their progress live through the reporter's gutter.
	backend := ghstack.New(rt.Runner, rt.Git, ghstack.WithOutput(rt.Report.Stream()))
	deps := app.Deps{
		Git:     rt.Git,
		Meta:    backend,
		Tracker: backend,
		Submit:  backend,
		Forge:   github.New(rt.Runner),
		AI: claudecode.New(rt.Runner, claudecode.Config{
			Command: cfg.AICommand, Model: cfg.AIModel, ExtraPrompt: cfg.AIExtraPrompt, Timeout: cfg.AITimeout,
		}, rt.Log),
		Cache:    cache.New(repo),
		Config:   cfg,
		Log:      rt.Log,
		Progress: rt.Report.Step,
	}
	if rt.Interactive {
		deps.Prompter = ui.Prompter{In: rt.Streams.In, Out: rt.Streams.Err, Ctx: ctx, Wait: rt.Report.Waiting}
	}
	a := app.New(deps)
	// #123 in any line printed to a terminal links to the pull request.
	rt.Report.SetPRResolver(a.PRURLs(ctx, repo))
	return a, repo, nil
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

// report is the Reporter for this invocation (built with the runtime).
func (c *cli) report() *ui.Reporter { return c.runtime().Report }

// wantAI resolves the --ai / --no-ai flags against stack.ai.auto: --no-ai
// always wins, otherwise --ai or the config turns drafting on.
func (c *cli) wantAI(ctx context.Context, ai, noAI bool) (bool, error) {
	if noAI {
		return false, nil
	}
	if ai {
		return true, nil
	}
	cfg, err := c.runtime().Config(ctx)
	if err != nil {
		return false, err
	}
	return cfg.AIAuto, nil
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
		Use: "git-stack",
		// Usage lines read the way people type it.
		Annotations: map[string]string{cobra.CommandDisplayNameAnnotation: "git stack"},
		Short:       "Git commands for GitHub's native stacked PRs; restack, sync and merge whole stacks",
		Long:        "Git commands for GitHub's native stacked PRs; restack, sync and merge whole stacks.\nBuilt on gh stack. Run it as git stack <command>.\n\nFirst time? Run git stack install to set up the aliases and completion.",
		Version:     version.Current().String(),
		Args:        cobra.NoArgs,
		// Every command sets ValidArgsFunction; the root has no positional args.
		ValidArgsFunction: completeNothing,
		SilenceUsage:      true,
		SilenceErrors:     true,
		// A bare `git stack` shows the stacks, like `gt log`.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showLog(cmd.Context(), c)
		},
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			if c.globals.Cwd != "" {
				c.globals.Cwd = filepath.Clean(c.globals.Cwd)
			}
			c.startUpdateCheck(cmd)
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
	pf.StringVar(&c.globals.Cwd, "cwd", "", "run as if started in this directory")
	must(root.RegisterFlagCompletionFunc("cwd", completeDirs))

	root.AddCommand(
		newCreateCmd(c),
		newModifyCmd(c),
		newRestackCmd(c),
		newContinueCmd(c),
		newAbortCmd(c),
		newSubmitCmd(c),
		newSyncCmd(c),
		newMergeCmd(c),
		newUpCmd(c),
		newDownCmd(c),
		newTopCmd(c),
		newBottomCmd(c),
		newCheckoutCmd(c),
		newLogCmd(c),
		newInstallCmd(c),
		newMcpCmd(c),
		newCompletionCmd(c),
		newVersionCmd(c),
		newUpdateCmd(c),
	)
	c.ownTerminal(root)
	return root
}

// noTerminalStatusEnv switches off the window title and the OSC 7501
// program status that steps and prompts set.
const noTerminalStatusEnv = "GIT_STACK_NO_TERMINAL_STATUS"

// Execute runs the CLI with the process streams and returns the exit code.
func Execute() int {
	// Ctrl-C and SIGTERM cancel the context, so the command unwinds and
	// returns through here like any other error.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := &cli{streams: Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}}
	// The command puts the terminal back itself (see ownTerminal).
	err := newRootCmd(c).ExecuteContext(ctx)
	if err != nil {
		c.errorReporter().Error(err)
	}
	c.updateNotice()
	if err != nil {
		return exitCode(err)
	}
	return 0
}

// ownTerminal makes every command in the tree put the terminal back (the
// window title, the program status) when it ends, however it ends: a
// result, an error, Ctrl-C or SIGTERM (both cancel the context, so the
// command returns), or a panic, which is let through once the terminal is
// put back. Doing it in the commands themselves rather than in Execute means
// anything that runs the tree from NewRootCmd gets it too.
func (c *cli) ownTerminal(cmd *cobra.Command) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
			defer func() {
				if p := recover(); p != nil {
					c.finishTerminal(fmt.Errorf("panic: %v", p))
					panic(p)
				}
				c.finishTerminal(err)
			}()
			return run(cmd, args)
		}
	}
	for _, sub := range cmd.Commands() {
		c.ownTerminal(sub)
	}
}

// finishTerminal puts back whatever the command's steps and prompts changed
// about the terminal and reports how it ended (err). Safe to call on any
// path, any number of times.
func (c *cli) finishTerminal(err error) {
	if c.rt != nil {
		c.rt.Report.Finish(err)
	}
}

// errorReporter is the Reporter that prints the command's final error: the
// command's own once it has one (so pull requests it knows about are links
// in the message), otherwise a plain one over the process streams.
func (c *cli) errorReporter() *ui.Reporter {
	if c.rt != nil {
		return c.rt.Report
	}
	return ui.NewReporter(c.streams.In, c.streams.Out, c.streams.Err, ui.ReporterOptions{
		OutTTY: isTerminal(c.streams.Out), ErrTTY: isTerminal(c.streams.Err),
	})
}

// noUpdateCheckEnv switches the background release check off.
const noUpdateCheckEnv = "GIT_STACK_NO_UPDATE_CHECK"

// startUpdateCheck kicks off the background release check for a command run
// by a person at a terminal. It returns at once; the answer, if any, is
// printed by updateNotice when the command is done. Machine facing commands
// (the MCP server, completion), the version and update commands themselves
// and --quiet runs skip it, as does GIT_STACK_NO_UPDATE_CHECK.
func (c *cli) startUpdateCheck(cmd *cobra.Command) {
	switch cmd.Name() {
	case "mcp", "completion", "__complete", "__completeNoDesc", "help", "version", "update":
		return
	}
	if c.globals.Quiet || os.Getenv(noUpdateCheckEnv) != "" {
		return
	}
	checker := c.updateChecker
	if checker == nil {
		// The notice is for a person; piped output (scripts, agents) never sees it.
		if !isTerminal(c.streams.Err) {
			return
		}
		dir, err := update.DefaultCacheDir()
		if err != nil {
			return
		}
		checker = &update.Checker{Client: update.New(version.Current()), Store: cache.At(dir)}
	}
	current := c.current
	if current.Version == "" {
		current = version.Current()
	}
	c.check = checker.Start(cmd.Context(), current)
}

// updateNotice tells the user a newer release is out, after the command's
// own output. It never waits for the background check: it reports what this
// run learnt if the answer is already in, otherwise what the last run knew.
func (c *cli) updateNotice() {
	latest, ok := c.check.Available()
	if !ok {
		return
	}
	c.report().Warn("git-stack %s is out (you have %s); run git stack update", latest, c.check.Current.Version)
}

// printError renders an error in the plain (non terminal) style; the CLI
// itself goes through ui.Reporter, this is kept for tests and callers that
// only have a writer.
func printError(w io.Writer, err error) {
	ui.NewReporter(nil, w, w, ui.ReporterOptions{}).Error(err)
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
