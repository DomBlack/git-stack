package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newModifyCmd(c *cli) *cobra.Command {
	var (
		messages    []string
		st          stagingFlags
		newCommit   bool
		edit        bool
		noEdit      bool
		resetAuthor bool
		cont        bool
		abort       bool
		noVerify    bool
	)
	cmd := &cobra.Command{
		Use:     "modify",
		Aliases: []string{"m"},
		Short:   "Amend the current branch's commit (or add one) and restack everything above it",
		Long: `Amends the current branch's commit (or adds a new one with -c), then restacks
every branch above it. If the branch has no commits of its own yet, a new commit
is created so the parent's commit is never rewritten.`,
		Example: `  # fold everything you've changed into this branch's commit
  git modify -a

  # add a new commit on this branch rather than amending
  git modify -a -c -m "Address review comments"

  # reword this branch's commit
  git modify -e`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			staging := st.mode()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			res, err := a.Modify(ctx, repo, app.ModifyOptions{
				NewCommit:   newCommit,
				Staging:     staging,
				Message:     messages,
				Edit:        edit && !noEdit,
				ResetAuthor: resetAuthor,
				NoVerify:    noVerify,
				Continue:    cont,
				Abort:       abort,
			})
			if err != nil {
				return err
			}
			rep := c.report()
			switch {
			case cont:
				if n := len(res.Restacked); n > 0 {
					rep.Success("Restack continued; restacked %d %s", n, branchNoun(n))
				} else {
					rep.Success("Restack continued")
				}
			case abort:
				rep.Success("Restack aborted; branches put back")
			case res.Amended:
				rep.Success("Amended %s  %s %s", rep.Branch(res.Branch), rep.SHA(res.Commit.Short()), res.Commit.Subject)
			default:
				if res.ForcedNewCommit {
					rep.Warn("%s had no commits of its own; created one instead of amending", res.Branch)
				}
				rep.Success("Committed to %s  %s %s", rep.Branch(res.Branch), rep.SHA(res.Commit.Short()), res.Commit.Subject)
			}
			if n := len(res.Restacked); n > 0 {
				rep.Success("Restacked %d %s above %s", n, branchNoun(n), rep.Branch(res.Branch))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&messages, "message", "m", nil, "commit message; repeat it for more paragraphs (no editor is opened)")
	must(cmd.RegisterFlagCompletionFunc("message", completeNothing))
	st.add(cmd)
	cmd.Flags().BoolVarP(&newCommit, "commit", "c", false, "create a new commit instead of amending")
	cmd.Flags().BoolVarP(&edit, "edit", "e", false, "open an editor to edit the commit message when amending")
	cmd.Flags().BoolVar(&noEdit, "no-edit", false, "keep the commit message (default when amending without -m)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip git hooks when committing")
	cmd.Flags().BoolVar(&resetAuthor, "reset-author", false, "set the author to the current user when amending")
	cmd.Flags().BoolVar(&cont, "continue", false, "same as git stack continue")
	cmd.Flags().BoolVar(&abort, "abort", false, "same as git stack abort")
	cmd.MarkFlagsMutuallyExclusive("continue", "abort")
	return cmd
}

// branchNoun is "branch" or "branches" for n.
func branchNoun(n int) string {
	if n == 1 {
		return "branch"
	}
	return "branches"
}
