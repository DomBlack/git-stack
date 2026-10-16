package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/update"
	"github.com/DomBlack/git-stack/pkg/version"
)

func newUpdateCmd(c *cli) *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update git-stack to the latest release",
		Long: `Download the latest GitHub release and replace this binary with it.

The archive for this OS and architecture is verified against the release's
checksums.txt before anything is written. No login is needed; releases are
fetched over plain HTTPS.

A binary built from source (git stack version says "dev") is left alone unless
you pass --force, since you probably built it that way on purpose.

Once updated, whatever git stack install set up before (aliases, completion,
agent registration, the Claude Code and Codex skills) is brought in line with
the new release without prompting. Only the changes are printed.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Current()
			target := c.updateTarget
			if target == "" {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				if resolved, err := filepath.EvalSymlinks(exe); err == nil {
					exe = resolved
				}
				target = exe
			}
			client := c.updateClient
			if client == nil {
				client = update.New(info)
			}
			rep := c.report()
			opts := update.Options{
				Current: info, Target: target, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Check: check, Force: force,
			}
			var plan update.Plan
			err := rep.Step(cmd.Context(), app.PhaseUpdate, "Checking for updates", func(ctx context.Context) error {
				var err error
				plan, err = client.Plan(ctx, opts)
				return err
			})
			if err != nil {
				return err
			}
			res := plan.Result
			if plan.Install {
				err := rep.Step(cmd.Context(), app.PhaseUpdate, "Downloading git-stack "+res.Latest, func(ctx context.Context) error {
					var err error
					res, err = client.Install(ctx, plan, opts)
					return err
				})
				if err != nil {
					return err
				}
			}
			switch res.Status {
			case update.StatusUpToDate:
				rep.Success("git-stack %s is the latest release", res.Current)
			case update.StatusAhead:
				rep.Success("git-stack %s is ahead of the latest release (%s)", res.Current, res.Latest)
			case update.StatusAvailable:
				if res.DevBuild {
					rep.Partial("This is a dev build (%s); the latest release is %s", info, res.Latest)
					rep.NextStep("git stack update --force replaces it with the release")
					rep.NextStep("or rebuild from your checkout")
				} else {
					rep.Partial("Update available: %s is out, you have %s", res.Latest, res.Current)
					rep.NextStep("git stack update installs it")
				}
			case update.StatusUpdated:
				rep.Success("Updated git-stack %s to %s (%s)", info, res.Latest, res.Target)
				return refreshInstall(cmd.Context(), c, res.Target)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().BoolVar(&force, "force", false, "install the latest release even if this binary is up to date, ahead, or a dev build")
	return cmd
}

// refreshInstall hands over to the new binary to bring whatever git stack
// install set up before (aliases, completion, agent registrations, skills)
// in line with it. It has to be the new binary: the completion script and
// the skill are built into it. It prints only what it changed.
func refreshInstall(ctx context.Context, c *cli, exe string) error {
	rt := c.runtime()
	args := []string{"install", "--refresh", "--no-interactive"}
	if c.globals.Quiet {
		args = append(args, "--quiet")
	}
	cmd := exec.Cmd{
		Name: exe,
		Args: args,
		// This process already owns the window title and program status,
		// and has just checked for updates.
		Env: []string{noTerminalStatusEnv + "=1", "GIT_STACK_NO_UPDATE_CHECK=1"},
	}
	// On a terminal the new binary writes straight to it, so its lines look
	// like ours; otherwise its plain output is passed on.
	if rt.Interactive {
		cmd.Mode = exec.Passthrough
	}
	res, err := rt.Runner.Run(ctx, cmd)
	if err != nil {
		se := stack.New(stack.KindUnknown, "git-stack was updated, but refreshing what git stack install set up failed").
			WithCause(err).
			WithSteps("git stack install")
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			se = se.WithDetail(ee.Result.Err())
		}
		return se
	}
	rt.Report.Print(string(res.Stdout))
	return nil
}
