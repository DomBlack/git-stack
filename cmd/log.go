package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newLogCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "log",
		Short: "Show every stack as a tree, with pull request state (also what bare `git stack` does)",
		Long: `Show the stacks in this repository the way gt log does: trunk at the bottom, each
stack rising from it, the current branch marked, and each branch's pull request,
age and whether it needs a restack. Pull request state comes from the local cache
and is refreshed when stale.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showLog(cmd.Context(), c)
		},
	}
}

// showLog renders the stack tree; it backs both `git stack log` and a bare
// `git stack`.
func showLog(ctx context.Context, c *cli) error {
	a, repo, err := c.app(ctx)
	if err != nil {
		return err
	}
	v, err := a.View(ctx, repo, app.ViewOptions{PRs: app.PRsFresh})
	if err != nil {
		return err
	}
	rep := c.report()
	out := ui.RenderLog(v, ui.LogOptions{Styles: rep.Styles(), Now: time.Now(), Links: rep.Links()})
	if out == "" {
		trunk := "your trunk"
		if len(v.Graph.Trunks) > 0 {
			trunk = v.Graph.Trunks[0]
		}
		rep.Warn("No stacks yet; check out %s and run git stack create to start one", trunk)
		return nil
	}
	rep.Print(out)
	return nil
}
