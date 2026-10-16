package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newUntrackCmd(c *cli) *cobra.Command {
	var only bool
	cmd := &cobra.Command{
		Use:   "untrack [branch]",
		Short: "Stop tracking a branch, and the ones above it, as part of a stack",
		Long: `Takes a branch (the current one unless named) out of its stack, together with
every branch above it, since gh stack keeps a stack as a single line and it
can't have a gap. The branches themselves are left exactly as they are: no
commit, ref or checkout changes. A stack left empty goes away.

With --only just that branch is untracked, and the branches above it close the
gap: they now sit on its parent and are restacked there, which drops its
commits from them. Conflicts stop like a restack and "git stack abort" puts
everything back.

Pull requests stay open. A stack GitHub already knows is regrouped at the next
submit. "git stack init" tracks the branches again.`,
		Example: `  # stop tracking this branch and everything above it
  git untrack

  # drop one branch from the middle of its stack
  git untrack feat/experiment --only`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return c.completeStackedBranches(cmd, args, toComplete)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			o := app.UntrackOptions{Only: only}
			if len(args) > 0 {
				o.Branch = args[0]
			}
			rep := c.report()
			res, err := a.Untrack(ctx, repo, o)
			renderNotices(rep, res.Notices)
			if err != nil {
				return err
			}
			rep.Success("Untracked %s", strings.Join(res.Untracked, ", "))
			if len(res.Restacked) > 0 {
				rep.Success("Restacked %s onto %s", strings.Join(res.Restacked, ", "), rep.Branch(res.Parent))
			}
			for _, step := range res.NextSteps {
				rep.Info("%s tracks %s again", step, pluralWord(len(res.Untracked), "it", "them"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&only, "only", false, "untrack just this branch; the branches above it move down onto its parent")
	return cmd
}
