package cmd

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/ai/claudecode"
	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/backend/ghstack"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge/github"
	"github.com/DomBlack/git-stack/pkg/git"
	mcpserver "github.com/DomBlack/git-stack/pkg/mcp"
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
				Version: version,
				Cwd:     cwd,
				Git:     g,
				Log:     log,
				NewApp: func(ctx context.Context, repo git.Repo) (*app.App, error) {
					cfg, err := config.Load(ctx, g, repo)
					if err != nil {
						return nil, err
					}
					backend := ghstack.New(runner, g)
					return app.New(app.Deps{
						Git: g, Meta: backend, Tracker: backend, Restack: backend, Submit: backend, Sync: backend,
						Forge: github.New(runner),
						AI: claudecode.New(runner, claudecode.Config{
							Command: cfg.AICommand, Model: cfg.AIModel, ExtraPrompt: cfg.AIExtraPrompt, Timeout: cfg.AITimeout,
						}, log),
						Cache: cache.New(repo), Config: cfg, Log: log,
					}), nil
				},
			})
			return srv.Run(cmd.Context())
		},
	}
}
