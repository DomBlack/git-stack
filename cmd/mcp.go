package cmd

import (
	"context"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/ai/claudecode"
	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/backend/ghstack"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge/github"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/gitconfig"
	mcpserver "github.com/DomBlack/git-stack/pkg/mcp"
	"github.com/DomBlack/git-stack/pkg/update"
	"github.com/DomBlack/git-stack/pkg/version"
)

func newMcpCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the stdio MCP server for coding agents",
		Long: `Serve the stack operations over the Model Context Protocol on stdin/stdout so
agents such as Claude Code and Codex can drive git-stack. Register it with
"git stack install --agents". Logs go to stderr; nothing else may touch stdout.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Never a TTY runner, never a prompter: the server is always
			// non-interactive and every subprocess is captured.
			var opts []exec.Option
			if c.globals.Debug {
				opts = append(opts, exec.WithDebug(c.streams.Err))
			}
			newRunner := c.newRunner
			if newRunner == nil {
				newRunner = exec.New
			}
			runner := newRunner(opts...)
			g := git.New(runner)
			log := newLogger(c.streams.Err, c.globals.Debug)
			cwd := c.globals.Cwd
			if cwd == "" {
				cwd, _ = os.Getwd()
			}
			srv := mcpserver.New(mcpserver.Options{
				Version: version.Current().Version,
				Cwd:     cwd,
				Git:     g,
				Log:     log,
				NewBareApp: func(context.Context) (*app.App, error) {
					return app.New(app.Deps{Git: g, Log: log, Doctors: []doctor.Provider{ghstack.New(runner, g)}}), nil
				},
				Doctors: []doctor.Provider{
					gitconfig.New(g),
					// No AfterUpdate: the server reports a newer release but
					// never replaces the binary it runs as.
					&update.Doctor{Client: update.New(version.Current()), Options: update.Options{Current: version.Current(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}},
				},
				NewApp: func(ctx context.Context, repo git.Repo) (*app.App, error) {
					cfg, err := config.Load(ctx, g, repo)
					if err != nil {
						return nil, err
					}
					backend := ghstack.New(runner, g)
					return app.New(app.Deps{
						Git: g, Meta: backend, Tracker: backend, Submit: backend,
						Forge: github.New(runner),
						AI: claudecode.New(runner, claudecode.Config{
							Command: cfg.AICommand, Model: cfg.AIModel, ExtraPrompt: cfg.AIExtraPrompt, Timeout: cfg.AITimeout,
						}, log),
						Cache: cache.New(repo), Config: cfg, Log: log,
						Doctors: []doctor.Provider{backend},
					}), nil
				},
			})
			return srv.Run(cmd.Context())
		},
	}
}
