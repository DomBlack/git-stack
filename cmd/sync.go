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
		Short: "Sync every stack with the remote: fetch, update trunk, restack, push, prune merged branches",
		Long: `Fetch the remote, fast-forward trunk, restack and push every stack, like gt sync: the
one you are on first, then the rest, whether or not they are checked out and whichever
worktree holds them (a stack that is not checked out gets a branch checked out in its
worktree for the sync, then the previous branch back). Branches whose pull requests have
merged are deleted; set git config stack.sync.prune to "ask" to be asked first or "never"
to keep them. -f deletes them whatever the config says.`,
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
				if n := len(res.Stacks); n > 1 {
					rep.Success("Synced %d stacks", n)
				} else {
					rep.Success("Synced")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "delete merged branches even when stack.sync.prune is ask or never")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "accepted for gt parity; every stack is synced anyway")
	cmd.Flags().BoolVar(&noRestack, "no-restack", false, "not available with gh stack")
	return cmd
}
