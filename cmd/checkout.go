package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/stack"
)

func newCheckoutCmd(c *cli) *cobra.Command {
	var (
		trunk         bool
		currentStack  bool
		showUntracked bool
		all           bool
	)
	cmd := &cobra.Command{
		Use:     "checkout [branch]",
		Aliases: []string{"co"},
		Short:   "Switch to a branch",
		Long: `Switch to a branch. With no branch, opens an interactive picker showing every
stack as a tree rooted at its trunk.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: c.completeBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			var target string
			switch {
			case trunk:
				target, err = a.Trunk(ctx, repo)
				if err != nil {
					return err
				}
			case len(args) == 1:
				target = args[0]
			default:
				// The interactive tree picker comes later; for now a branch name is required.
				_ = currentStack
				_ = showUntracked
				_ = all
				return stack.New(stack.KindInteractionRequired, "no branch given").
					WithSteps("git stack checkout <branch>", "git stack checkout --trunk")
			}
			if err := a.Checkout(ctx, repo, target); err != nil {
				return err
			}
			if !c.globals.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "Checked out %s.\n", target)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&trunk, "trunk", "t", false, "check out the trunk of the current stack")
	cmd.Flags().BoolVarP(&currentStack, "stack", "s", false, "only show the current stack in the picker")
	cmd.Flags().BoolVarP(&showUntracked, "show-untracked", "u", false, "include branches that are in no stack in the picker")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "show every trunk and untracked branches in the picker")
	return cmd
}
