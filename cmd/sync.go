package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newSyncCmd(c *cli) *cobra.Command {
	var (
		force     bool
		all       bool
		noRestack bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync the stack with the remote: fetch, update trunk, restack, prune merged branches",
		Long: `Fetch the remote, fast-forward trunk, restack the current stack onto it, push, and
with -f delete local branches whose pull requests were merged.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if noRestack {
				return fmt.Errorf("--no-restack is not available: gh stack decides itself whether a restack is needed")
			}
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			res, err := a.Sync(ctx, repo, app.SyncOptions{Prune: force, All: all})
			if err != nil {
				return err
			}
			if res.Output != "" {
				_, _ = io.WriteString(rep.Stream(), res.Output+"\n")
			}
			for _, n := range res.Notices {
				rep.Warn("%s", n)
			}
			if !res.Aborted {
				rep.Success("Synced")
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "delete merged branches without asking (gh stack sync --prune)")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "sync every stack (gh stack only syncs the current one; a notice is printed)")
	cmd.Flags().BoolVar(&noRestack, "no-restack", false, "not available with gh stack")
	return cmd
}
