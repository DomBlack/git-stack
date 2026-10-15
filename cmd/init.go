package cmd

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newInitCmd(c *cli) *cobra.Command {
	var base string
	cmd := &cobra.Command{
		Use:     "init [branch...]",
		Aliases: []string{"track"},
		Short:   "Track branches you already have as a new stack",
		Long: `Records a new stack from branches that already exist, bottom to top; with no
arguments, the current branch. The trunk is --base, else the only trunk the
repository has, else its default branch. Nothing is committed, rebased or
checked out: if a branch isn't on the one below it, you're told and
"git stack restack" puts it there.

New branches are made with "git stack create"; this only adopts existing ones.`,
		Example: `  # track the branch you're on as a stack of its own
  git track

  # track three branches made by hand, bottom first
  git track feat/db feat/api feat/ui`,
		ValidArgsFunction: c.completeUntrackedBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			res, err := a.Init(ctx, repo, app.InitOptions{Branches: args, Base: base})
			if err != nil {
				return err
			}
			rep := c.report()
			rep.Success("Tracking %s on %s", strings.Join(res.Branches, ", "), rep.Branch(res.Trunk))
			parent := res.Trunk
			for _, b := range res.Branches {
				if slices.Contains(res.NeedsRestack, b) {
					rep.Warn("%s is not on %s; run git stack restack", b, parent)
				}
				parent = b
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&base, "base", "b", "", "the trunk to stack on (default: the repository's trunk)")
	must(cmd.RegisterFlagCompletionFunc("base", c.completeTrunks))
	return cmd
}
