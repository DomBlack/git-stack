package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/install"
	"github.com/DomBlack/git-stack/pkg/shell"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newInstallCmd(c *cli) *cobra.Command {
	var (
		aliases, completion, agentsOn bool
		skill                         bool
		shells                        []string
		dryRun, uninstall, force      bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set up git aliases, shell completion and agent MCP registration",
		Long: `Install everything git-stack needs to feel native:

  aliases      git create/modify/restack/submit/sync/up/down/top/bottom and the
               short forms c, m, rs, ss, u, d, t, b, co (checkout is a git builtin
               and cannot be aliased). Existing aliases are never replaced without
               --force or your confirmation.
  completion   cobra completion for git-stack plus the hooks that make
               "git stack <TAB>" and the aliases complete (bash, zsh, fish).
  agents       registers "git stack mcp" with Claude Code and Codex at user scope
               through their own CLIs (claude mcp / codex mcp).
  skill        optionally installs a Claude Code skill describing the workflow.

--uninstall removes exactly what was recorded as installed. --dry-run shows every
change without making it.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			rt := c.runtime()
			var shs []shell.Shell
			for _, s := range shells {
				sh, err := shell.Parse(s)
				if err != nil {
					return err
				}
				shs = append(shs, sh)
			}
			exe, err := executablePath()
			if err != nil {
				return err
			}
			in := &install.Installer{
				Git:    rt.Git,
				Runner: rt.Runner,
				Exe:    exe,
				Env: shell.Env{
					Home:          homeDir(),
					XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
					XDGDataHome:   os.Getenv("XDG_DATA_HOME"),
				},
				CobraScript: func(sh shell.Shell) (string, error) {
					var buf bytes.Buffer
					var err error
					switch sh {
					case shell.Bash:
						err = cmd.Root().GenBashCompletionV2(&buf, true)
					case shell.Zsh:
						err = cmd.Root().GenZshCompletion(&buf)
					case shell.Fish:
						err = cmd.Root().GenFishCompletion(&buf, true)
					}
					return buf.String(), err
				},
				Report: c.report(),
			}
			if rt.Interactive {
				in.Prompter = ui.Prompter{In: rt.Streams.In, Out: rt.Streams.Err, Ctx: ctx}
			}
			o := install.Options{Aliases: aliases, Completion: completion, Agents: agentsOn, Shells: shs, DryRun: dryRun, Force: force}
			if cmd.Flags().Changed("skill") {
				o.Skill = &skill
			}
			if uninstall {
				return in.Uninstall(ctx, o)
			}
			return in.Run(ctx, o)
		},
	}
	cmd.Flags().BoolVar(&aliases, "aliases", true, "install git aliases")
	cmd.Flags().BoolVar(&completion, "completion", true, "install shell completion with git integration")
	cmd.Flags().BoolVar(&agentsOn, "agents", true, "register the MCP server with Claude Code and Codex")
	cmd.Flags().BoolVar(&skill, "skill", false, "install the Claude Code skill (asked interactively when not given)")
	cmd.Flags().StringArrayVar(&shells, "shell", nil, "shell(s) to install completion for (default: from $SHELL)")
	must(cmd.RegisterFlagCompletionFunc("shell", completeShells))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show every change without making it")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "remove everything git-stack installed")
	cmd.Flags().BoolVar(&force, "force", false, "replace existing aliases and completion files")
	return cmd
}

// executablePath is the absolute, symlink-resolved path of this binary.
func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

var _ = context.Background
