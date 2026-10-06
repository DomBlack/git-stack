package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newRestackCmd(c *cli) *cobra.Command {
	var (
		up, down, only bool
		branch         string
		cont, abort    bool
		all            bool
	)
	cmd := &cobra.Command{
		Use:     "restack",
		Aliases: []string{"rs"},
		Short:   "Rebase each branch of the current stack onto its parent",
		Long: `Rebases each branch in the stack onto its parent where needed, without checking
anything out. It doesn't fetch, so the bottom branch goes onto your local trunk
(run "git stack sync" first if you want the latest trunk).

If a branch conflicts it stops there, and that's the one time it checks a branch
out: it starts a real git rebase on it so you can fix things, which needs a
clean working tree. Fix the conflicts, git add the files and run
"git stack continue", or "git stack abort" to put every branch it already moved
back where it was.`,
		Example: `  # restack the whole stack onto your local trunk
  git restack

  # only restack this branch and the ones above it
  git restack -u

  # it stopped on a conflict; fix the files, then carry on
  git add -A
  git continue`,
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
			res, err := a.Restack(ctx, repo, app.RestackOptions{Scope: scope, Branch: branch, Continue: cont, Abort: abort, StageAll: all})
			if err != nil {
				return err
			}
			renderRestack(c.report(), res, abort)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&up, "upstack", "u", false, "only restack this branch and the ones above it")
	cmd.Flags().BoolVarP(&down, "downstack", "d", false, "only restack this branch and the ones below it")
	cmd.Flags().BoolVarP(&only, "only", "o", false, "only restack this branch")
	cmd.MarkFlagsMutuallyExclusive("upstack", "downstack", "only")
	cmd.Flags().StringVar(&branch, "branch", "", "work the scope out from this branch instead of the current one")
	must(cmd.RegisterFlagCompletionFunc("branch", c.completeBranches))
	cmd.Flags().BoolVar(&cont, "continue", false, "same as git stack continue")
	cmd.Flags().BoolVar(&abort, "abort", false, "same as git stack abort")
	cmd.MarkFlagsMutuallyExclusive("continue", "abort")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "with --continue: stage every change first")
	return cmd
}

// renderRestack prints what a restack did: notices first, then what moved
// or that nothing needed to.
func renderRestack(rep *ui.Reporter, res app.RestackResult, aborted bool) {
	for _, n := range res.Notices {
		rep.Warn("%s", n)
	}
	if aborted {
		if len(res.Restored) > 0 {
			rep.Success("Restack aborted; put back %s", strings.Join(res.Restored, ", "))
		} else {
			rep.Success("Restack aborted")
		}
		return
	}
	if len(res.Moved) == 0 {
		if len(res.InPlace) == 0 {
			rep.Success("Nothing to restack")
			return
		}
		rep.Success("Nothing to restack; %s %s already in place", strings.Join(res.InPlace, ", "), isAre(len(res.InPlace)))
		return
	}
	if len(res.InPlace) > 0 {
		rep.Info("%s already in place", strings.Join(res.InPlace, ", "))
	}
	names := make([]string, len(res.Moved))
	for i, m := range res.Moved {
		names[i] = m.Name
	}
	rep.Success("Restacked %s", strings.Join(names, ", "))
}

// isAre picks the verb for a list of n names.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
