package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

func newMergeCmd(c *cli) *cobra.Command {
	var (
		squash bool
		rebase bool
		merge  bool
		noSync bool
	)
	cmd := &cobra.Command{
		Use:   "merge [branch]",
		Short: "Merge the stack's pull requests up to a branch, all at once",
		Long: `Merges the PRs in the current stack, up to and including a branch (the current
one by default), into trunk as one all or nothing operation on GitHub; if any of
them can't be merged, none are. It then runs a sync, so the merged branches get
cleaned up and anything left above them is restacked onto the new trunk. Nothing
is pushed after that, so run git stack submit to update the PRs that are left.

Every PR being merged has to exist and be ready for review; a draft or closed
one stops the merge before anything happens. GitHub's own rules still apply
(required checks, reviews, merge queues). It uses the repo's default merge
method unless you pass one, or set git config stack.merge.method.`,
		Example: `  # merge every PR up to and including the current branch
  git stack merge

  # merge up to billing-webhook-schema, squashing each PR, then push the rest
  git stack merge billing-webhook-schema --squash
  git ss`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: c.completeBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			o := app.MergeOptions{NoSync: noSync}
			if len(args) == 1 {
				o.Branch = args[0]
			}
			switch {
			case squash:
				o.Method = forge.MergeSquash
			case rebase:
				o.Method = forge.MergeRebase
			case merge:
				o.Method = forge.MergeMerge
			}
			res, err := a.Merge(ctx, repo, o)
			if res.Status == "" {
				// Nothing landed; the error says why.
				return err
			}
			what := prNoun(len(res.PullRequests))
			switch {
			case res.Status == forge.MergeEnqueued:
				rep.Success("Queued %s for %s's merge queue", what, rep.Branch(res.Trunk))
			case res.SHA != "":
				rep.Success("Merged %s into %s at %s", what, rep.Branch(res.Trunk), rep.SHA(res.SHA))
			default:
				rep.Success("Merged %s into %s", what, rep.Branch(res.Trunk))
			}
			for _, pr := range res.PullRequests {
				rep.Info("%s  %s", rep.Branch(pr.Branch), rep.Ref(pr.Number, pr.URL))
			}
			for _, note := range res.Notices {
				rep.Warn("%s", note)
			}
			if res.Sync != nil {
				renderSync(rep, *res.Sync)
				if err != nil {
					return err
				}
				rep.Success("%s", syncSummary(*res.Sync))
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&squash, "squash", false, "squash each pull request into one commit")
	cmd.Flags().BoolVar(&rebase, "rebase", false, "rebase the commits onto trunk as they are")
	cmd.Flags().BoolVar(&merge, "merge", false, "merge with a merge commit")
	cmd.MarkFlagsMutuallyExclusive("squash", "rebase", "merge")
	cmd.Flags().BoolVar(&noSync, "no-sync", false, "don't sync afterwards")
	return cmd
}

// prNoun counts pull requests: "1 pull request", "3 pull requests".
func prNoun(n int) string {
	if n == 1 {
		return "1 pull request"
	}
	return fmt.Sprintf("%d pull requests", n)
}
