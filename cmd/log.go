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
		Short: "Show every stack as a tree with PR state",
		Long: `Shows every stack in the repo as a tree; trunk at the bottom, each stack rising
out of it, the current branch marked, and each branch's PR, age and whether it
needs a restack or a push (you've changed it since the last push, so its PR is
behind). A repo with no stacks yet just shows its trunk. PR state comes from a
local cache and is refreshed when it goes stale.

Bare git stack runs this.`,
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
		// Not even a trunk: no stacks and no default branch to show.
		rep.Warn("No stacks yet; check out your trunk and run git stack create to start one")
		return nil
	}
	rep.Print(out)
	return nil
}
