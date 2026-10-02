package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func newRestackCmd(c *cli) *cobra.Command {
	var (
		up, down, only bool
		cont, abort    bool
	)
	cmd := &cobra.Command{
		Use:     "restack",
		Aliases: []string{"rs"},
		Short:   "Rebase each branch of the current stack onto its parent",
		Long: `Ensure each branch in the current stack has its parent in its history, rebasing
locally where needed (no fetch). Conflicts stop the restack; resolve them, git add the
files, then run "git stack restack --continue" (or --abort).`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			scope := stack.ScopeAll
			switch {
			case up:
				scope = stack.ScopeUpstack
			case down:
				scope = stack.ScopeDownstack
			case only:
				scope = stack.ScopeOnly
			}
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			res, err := a.Restack(ctx, repo, app.RestackOptions{Scope: scope, Continue: cont, Abort: abort})
			if err != nil {
				return err
			}
			rep := c.report()
			switch {
			case cont:
				rep.Success("Restack continued")
			case abort:
				rep.Success("Restack aborted; branches restored")
			default:
				rep.Success("Restacked %s", strings.Join(res.Branches, ", "))
			}
			if res.BottomBehindTrunk {
				rep.Warn("%s is behind %s; restack never rebases onto trunk locally, run git stack sync for that", res.Bottom, res.Trunk)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&up, "upstack", "u", false, "only restack this branch and its descendants")
	cmd.Flags().BoolVarP(&down, "downstack", "d", false, "only restack this branch and its ancestors")
	cmd.Flags().BoolVarP(&only, "only", "o", false, "only restack this branch (not supported by gh stack yet)")
	cmd.MarkFlagsMutuallyExclusive("upstack", "downstack", "only")
	cmd.Flags().BoolVar(&cont, "continue", false, "continue an interrupted restack after resolving conflicts")
	cmd.Flags().BoolVar(&abort, "abort", false, "abort an interrupted restack and restore the branches")
	cmd.MarkFlagsMutuallyExclusive("continue", "abort")
	return cmd
}
