package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
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
you pass --force, since you probably built it that way on purpose.`,
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
			var res update.Result
			err := rep.Step(cmd.Context(), app.PhaseUpdate, "Checking for updates", func(ctx context.Context) error {
				var err error
				res, err = client.Run(ctx, update.Options{
					Current: info, Target: target, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Check: check, Force: force,
				})
				return err
			})
			if err != nil {
				return err
			}
			switch res.Status {
			case update.StatusUpToDate:
				rep.Success("git-stack %s is the latest release", res.Current)
			case update.StatusAhead:
				rep.Success("git-stack %s is ahead of the latest release (%s)", res.Current, res.Latest)
			case update.StatusAvailable:
				if res.DevBuild {
					rep.Warn("This is a dev build (%s); the latest release is %s", info, res.Latest)
					rep.Info("git stack update --force replaces it with the release")
					rep.Info("or rebuild from your checkout")
				} else {
					rep.Warn("Update available: %s is out, you have %s. Run git stack update to install it", res.Latest, res.Current)
				}
			case update.StatusUpdated:
				rep.Success("Updated git-stack %s to %s (%s)", info, res.Latest, res.Target)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().BoolVar(&force, "force", false, "install the latest release even if this binary is up to date, ahead, or a dev build")
	return cmd
}
