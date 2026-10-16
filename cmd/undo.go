package cmd

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newUndoCmd(c *cli) *cobra.Command {
	var list, force bool
	cmd := &cobra.Command{
		Use:   "undo",
		Short: "Undo the last git-stack command",
		Long: `Puts back what the last git-stack command run in this worktree changed:
branches, the stack metadata and what was checked out. Run it again to step
further back, up to 20 commands. After create, modify or absorb the changes
they committed come back staged; after anything else your local changes stay
as they are.

Undo is local only. Submit and merge change GitHub, so they clear the history.
It refuses when a branch has moved since the command; --force throws that away
(but never your uncommitted changes).`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			res, err := a.Undo(ctx, repo, app.UndoOptions{List: list, Force: force})
			if err != nil {
				return err
			}
			renderUndo(c.report(), res, list, time.Now())
			return nil
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "show what can be undone, newest first, without changing anything")
	cmd.Flags().BoolVar(&force, "force", false, "undo even when branches moved since, throwing away what changed on them")
	return cmd
}

func renderUndo(rep *ui.Reporter, res app.UndoResult, list bool, now time.Time) {
	if list {
		switch n := len(res.Entries); n {
		case 0:
			rep.Success("Nothing to undo")
		case 1:
			rep.Success("1 command can be undone")
		default:
			rep.Success("%d commands can be undone", n)
		}
		for _, e := range res.Entries {
			rep.Info("%s  %s", ui.RelativeTime(e.At, now), e.Command)
		}
		return
	}
	for _, n := range res.Notices {
		rep.Warn("%s", n)
	}
	rep.Success("Undid %s (%s)", res.Undone.Command, ui.RelativeTime(res.Undone.At, now))
	for _, r := range res.Undone.Restores {
		rep.Info("%s", r)
	}
	if res.Staged {
		rep.Info("your changes from it are staged")
	}
}
