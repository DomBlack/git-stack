package cmd

import (
	"fmt"

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
	)
	cmd := &cobra.Command{
		Use:     "modify",
		Aliases: []string{"m"},
		Short:   "Amend the current branch's commit (or add one) and restack its descendants",
		Long: `Modify the current branch by amending its commit, or creating a new one with -c, then
restack every branch above it. If the branch has no commits of its own, a new commit is
created so the parent's commit is never rewritten.`,
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
				NoVerify:    c.globals.NoVerify,
				Continue:    cont,
				Abort:       abort,
			})
			if err != nil {
				return err
			}
			if c.globals.Quiet {
				return nil
			}
			out := cmd.OutOrStdout()
			switch {
			case cont:
				fmt.Fprintln(out, "Restack continued.")
			case abort:
				fmt.Fprintln(out, "Restack aborted; branches restored.")
			case res.Amended:
				fmt.Fprintf(out, "Amended %s: %s %s\n", res.Branch, res.Commit.Short(), res.Commit.Subject)
			default:
				if res.ForcedNewCommit {
					fmt.Fprintf(out, "%s had no commits of its own; created one instead of amending.\n", res.Branch)
				}
				fmt.Fprintf(out, "Committed to %s: %s %s\n", res.Branch, res.Commit.Short(), res.Commit.Subject)
			}
			if n := len(res.Restacked); n > 0 {
				fmt.Fprintf(out, "Restacked %d %s above %s.\n", n, plural(n, "branch", "branches"), res.Branch)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&messages, "message", "m", nil, "commit message (repeat for paragraphs); no editor is opened")
	must(cmd.RegisterFlagCompletionFunc("message", completeNothing))
	st.add(cmd)
	cmd.Flags().BoolVarP(&newCommit, "commit", "c", false, "create a new commit instead of amending")
	cmd.Flags().BoolVarP(&edit, "edit", "e", false, "open an editor to edit the commit message when amending")
	cmd.Flags().BoolVar(&noEdit, "no-edit", false, "keep the commit message (default when amending without -m)")
	cmd.Flags().BoolVar(&resetAuthor, "reset-author", false, "set the author to the current user when amending")
	cmd.Flags().BoolVar(&cont, "continue", false, "continue an interrupted restack after resolving conflicts")
	cmd.Flags().BoolVar(&abort, "abort", false, "abort an interrupted restack and restore the branches")
	cmd.MarkFlagsMutuallyExclusive("continue", "abort")
	return cmd
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
