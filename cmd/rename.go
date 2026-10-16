package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newRenameCmd(c *cli) *cobra.Command {
	var local bool
	cmd := &cobra.Command{
		Use:   "rename <name>",
		Short: "Rename the current branch",
		Long: `Gives the current branch a new name, keeping its place in the stack and its
commits; nothing is rebased, and the branch above now sits on the new name.

If the branch has been pushed it's renamed on GitHub first, which keeps its pull
request attached and retargets any pull request based on it, then here. If
GitHub says no (offline, no permission) nothing changes. With --local only this
copy is renamed: the pull request stays on the old name and the next submit
opens a new one.`,
		Example: `  # rename this branch here and on GitHub
  git stack rename billing-webhook-retries

  # rename it only on this machine
  git stack rename --local billing-webhook-retries`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			res, err := a.Rename(ctx, repo, app.RenameOptions{Name: args[0], Local: local})
			renderNotices(rep, res.Notices)
			if err != nil {
				return err
			}
			if res.Remote != "" {
				rep.Success("Renamed %s to %s here and on %s", res.From, res.To, res.Remote)
			} else {
				rep.Success("Renamed %s to %s", res.From, res.To)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&local, "local", false, "rename only on this machine, leaving the remote branch and its pull request alone")
	return cmd
}
