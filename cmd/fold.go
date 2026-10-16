package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newFoldCmd(c *cli) *cobra.Command {
	var keep bool
	cmd := &cobra.Command{
		Use:   "fold",
		Short: "Fold the current branch into the branch below it",
		Long: `Merges the current branch into its parent, so the two become one branch with
one pull request and both sets of commits. The parent keeps its name and pull
request and you're left on it; with --keep it's the other way round, the
current branch takes the parent's place and the parent is deleted.

The current branch has to sit on its parent (run "git stack restack" first if
it doesn't). Nothing is rebased: the branches above already have every commit
involved, only their parent changes. Local changes come along. The pull request
of the branch that went is closed at the next submit, with a comment saying
where it went.`,
		Example: `  # fold this branch into the one below it
  git fold

  # keep this branch's name and pull request instead
  git fold --keep`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			res, err := a.Fold(ctx, repo, app.FoldOptions{Keep: keep})
			renderNotices(rep, res.Notices)
			if err != nil {
				return err
			}
			rep.Success("Folded %s into %s", res.Folded, res.Into)
			if res.Closing != 0 {
				rep.Info("#%d (%s) will be closed at the next submit", res.Closing, res.Folded)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&keep, "keep", false, "keep this branch's name and pull request instead of the parent's")
	return cmd
}
