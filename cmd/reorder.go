package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newReorderCmd(c *cli) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reorder [branch...]",
		Short: "Change the order of the branches in this stack",
		Long: `Changes the order of the branches in the current stack and restacks them.
On a terminal with no arguments it shows the stack as a list you rearrange:
j/k move the cursor, shift with the arrows (or J/K) moves the branch under it,
enter applies, esc cancels. Otherwise pass the new order as arguments, bottom
to top, naming every branch of the stack once.

Nothing is checked out. Each branch keeps its own commits and is replayed onto
its new parent. A conflict stops like a restack does: fix the files, git add
them and run "git stack continue", or "git stack abort" to put the old order
back. If the stack already has pull requests, the next submit creates the
stack on GitHub again in the new order.`,
		Example: `  # rearrange the stack in the list
  git reorder

  # put b under a, keep c on top
  git reorder b a c`,
		ValidArgsFunction: c.completeStackOrder,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			res, err := a.Reorder(ctx, repo, app.ReorderOptions{Order: args, Pick: c.pickOrder(ctx, a, repo)})
			renderNotices(rep, res.Notices)
			if err != nil {
				return err
			}
			switch {
			case res.Cancelled:
				rep.Info("Reorder cancelled; nothing was changed")
			case !res.Changed:
				rep.Success("Nothing to reorder; the stack is already in that order")
			default:
				rep.Success("Reordered the stack: %s", strings.Join(res.After, ", "))
				if n := len(res.Restacked); n > 0 {
					rep.Success("Restacked %s", strings.Join(res.Restacked, ", "))
				}
			}
			return nil
		},
	}
	return cmd
}

// pickOrder opens the reorder list when there is a terminal; without one
// it is nil and the use case says to pass the order as arguments.
func (c *cli) pickOrder(ctx context.Context, a *app.App, repo git.Repo) func(trunk string, branches []string, current string) ([]string, error) {
	rt := c.runtime()
	if !rt.Interactive {
		return nil
	}
	return func(trunk string, branches []string, current string) ([]string, error) {
		prs := map[string]int{}
		if known, _, _ := a.CachedPRs(repo); known != nil {
			for name, pr := range app.PRsFor(known) {
				prs[name] = pr.Number
			}
		}
		done := rt.Report.Waiting("Reorder the stack")
		res, err := ui.RunReorder(ctx, rt.Streams.In, rt.Streams.Out, ui.ReorderOptions{Trunk: trunk, Branches: branches, Current: current, PRs: prs})
		done()
		if err != nil {
			return nil, err
		}
		if res.Cancelled {
			return nil, nil
		}
		return res.Order, nil
	}
}
