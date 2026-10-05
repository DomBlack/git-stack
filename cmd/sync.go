package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newSyncCmd(c *cli) *cobra.Command {
	var (
		force     bool
		deleteAll bool
		noRestack bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Fetch, move trunk, delete merged branches and restack every stack",
		Long: `Sync every stack with the remote, like gt sync: fetch, fast forward each trunk,
delete branches whose pull requests merged or closed (set git config stack.sync.prune to
"ask" to be asked first or "never" to keep them), fast forward branches that moved on the
remote, then restack every stack onto its updated parents without checking anything out.
Nothing is pushed; git stack submit does that. A trunk that has diverged from the remote
is only reset with -f or a yes at the prompt.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			res, err := a.Sync(ctx, repo, app.SyncOptions{Force: force, DeleteAll: deleteAll, NoRestack: noRestack})
			// A conflict in one stack doesn't stop the others, so there is
			// always something to show before the error.
			for _, t := range res.Trunks {
				switch t.Status {
				case app.TrunkFastForwarded:
					rep.Info("%s fast forwarded to %s", rep.Branch(t.Name), rep.SHA(t.To))
				case app.TrunkReset:
					rep.Info("%s reset to %s/%s at %s", rep.Branch(t.Name), res.Remote, t.Name, rep.SHA(t.To))
				case app.TrunkAhead:
					rep.Info("%s is ahead of %s/%s; left alone", rep.Branch(t.Name), res.Remote, t.Name)
				}
			}
			for _, d := range res.Deleted {
				if d.Head == "" {
					rep.Info("deleted %s (%s)", rep.Branch(d.Name), d.Reason)
					continue
				}
				rep.Info("deleted %s (%s, was %s)", rep.Branch(d.Name), d.Reason, rep.SHA(d.Head))
			}
			for _, u := range res.Updated {
				rep.Info("%s fast forwarded to %s from %s", rep.Branch(u.Name), rep.SHA(u.To), res.Remote)
			}
			for _, m := range res.Restacked {
				rep.Info("restacked %s", rep.Branch(m.Name))
			}
			for _, n := range res.Notices {
				rep.Warn("%s", n)
			}
			if err != nil {
				return err
			}
			rep.Success("%s", syncSummary(res))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "reset a diverged trunk and delete merged or closed branches without asking")
	cmd.Flags().BoolVarP(&deleteAll, "delete-all", "d", false, "delete merged or closed branches without asking")
	cmd.Flags().BoolVar(&noRestack, "no-restack", false, "skip restacking")
	return cmd
}

// syncSummary is the one line result: "Synced: 2 branches deleted, 3 restacked".
func syncSummary(res app.SyncResult) string {
	var parts []string
	if n := len(res.Deleted); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s deleted", n, branchNoun(n)))
	}
	if n := len(res.Updated); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s updated from the remote", n, branchNoun(n)))
	}
	if n := len(res.Restacked); n > 0 {
		parts = append(parts, fmt.Sprintf("%d restacked", n))
	}
	if len(parts) == 0 {
		return "Synced, nothing to do"
	}
	return "Synced: " + strings.Join(parts, ", ")
}
