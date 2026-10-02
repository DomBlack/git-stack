package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

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
			res, err := client.Run(cmd.Context(), update.Options{
				Current: info, Target: target, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Check: check, Force: force,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch res.Status {
			case update.StatusUpToDate:
				fmt.Fprintf(out, "git-stack %s is the latest release.\n", res.Current)
			case update.StatusAhead:
				fmt.Fprintf(out, "git-stack %s is ahead of the latest release (%s).\n", res.Current, res.Latest)
			case update.StatusAvailable:
				if res.DevBuild {
					fmt.Fprintf(out, "This is a dev build (%s); the latest release is %s.\n", info, res.Latest)
					fmt.Fprintln(out, "  - git stack update --force replaces it with the release")
					fmt.Fprintln(out, "  - or rebuild from your checkout")
				} else {
					fmt.Fprintf(out, "Update available: %s is out, you have %s. Run git stack update to install it.\n", res.Latest, res.Current)
				}
			case update.StatusUpdated:
				fmt.Fprintf(out, "Updated git-stack %s to %s (%s).\n", info, res.Latest, res.Target)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().BoolVar(&force, "force", false, "install the latest release even if this binary is up to date, ahead, or a dev build")
	return cmd
}
