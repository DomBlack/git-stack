package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newMoveCmd(c *cli) *cobra.Command {
	var (
		source, onto string
		only         bool
	)
	cmd := &cobra.Command{
		Use:   "move",
		Short: "Move a branch, and what's above it, onto another stack or onto trunk",
		Long: `Takes a branch out of its stack, together with every branch above it, and puts
it on top of another stack (--onto its top branch) or starts a new stack with it
(--onto a trunk). With --only the branch goes alone and the branches above it
stay where they were, on its old parent. The branch is the current one unless
--source says otherwise. Without --onto on a terminal, a picker lists the
places it can go.

gh stack keeps each stack as a single line, so a branch can only land on the
top of a stack or on a trunk; to change the order inside a stack use
"git stack reorder". Nothing is checked out; conflicts stop like a restack and
"git stack abort" puts everything back.`,
		Example: `  # take this branch (and the ones above) onto the top of another stack
  git move --onto feat/api-top

  # start a new stack on main with just this branch
  git move --onto main --only`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			res, err := a.Move(ctx, repo, app.MoveOptions{Source: source, Onto: onto, Only: only, Pick: c.pickDestination(ctx, a, repo)})
			renderNotices(rep, res.Notices)
			if err != nil {
				return err
			}
			if res.Cancelled {
				rep.Info("Move cancelled; nothing was changed")
				return nil
			}
			what := res.Branch
			if len(res.Moved) > 1 {
				what = strings.Join(res.Moved, ", ")
			}
			suffix := ""
			if res.NewStack {
				suffix = " (new stack)"
			}
			rep.Success("Moved %s onto %s%s", what, rep.Branch(res.Onto), suffix)
			if len(res.LeftBehind) > 0 {
				rep.Info("%s %s on %s", strings.Join(res.LeftBehind, ", "), stayVerb(len(res.LeftBehind)), rep.Branch(res.From))
			}
			if len(res.Restacked) > 0 {
				rep.Success("Restacked %s", strings.Join(res.Restacked, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&source, "source", "s", "", "the branch to move (default: the current branch)")
	must(cmd.RegisterFlagCompletionFunc("source", c.completeStackedBranches))
	cmd.Flags().StringVarP(&onto, "onto", "o", "", "where to put it: a trunk, or the top branch of another stack")
	must(cmd.RegisterFlagCompletionFunc("onto", c.completeMoveTargets))
	cmd.Flags().BoolVar(&only, "only", false, "move just this branch; the branches above it stay on its old parent")
	return cmd
}

// stayVerb is "stays" or "stay".
func stayVerb(n int) string { return pluralWord(n, "stays", "stay") }

// pickDestination opens the checkout picker limited to the valid
// destinations when there is a terminal; nil otherwise.
func (c *cli) pickDestination(ctx context.Context, a *app.App, repo git.Repo) func([]app.MoveDestination) (string, error) {
	rt := c.runtime()
	if !rt.Interactive {
		return nil
	}
	return func(dests []app.MoveDestination) (string, error) {
		v, err := a.View(ctx, repo, app.ViewOptions{PRs: app.PRsCached})
		if err != nil {
			return "", err
		}
		var rows []app.Row
		for _, d := range dests {
			r, ok := v.Row(d.Name)
			if !ok {
				r = app.Row{Name: d.Name, IsTrunk: d.IsTrunk, Tracked: true}
			}
			if d.IsTrunk {
				r.Depth = 0
			} else {
				r.Depth = 1
				r.Parent = d.Trunk
			}
			rows = append(rows, r)
		}
		done := rt.Report.Waiting("Move onto")
		res, err := ui.RunPicker(ctx, rt.Streams.In, rt.Streams.Out, ui.PickerOptions{Title: "Move onto", Rows: rows, Links: rt.Report.Links(), PRURL: rt.Report.PRURL})
		done()
		if err != nil || res.Cancelled {
			return "", err
		}
		return res.Branch, nil
	}
}
