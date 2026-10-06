package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/stack"
)

func newUpCmd(c *cli) *cobra.Command {
	var o navOptions
	cmd := &cobra.Command{
		Use:     "up [steps]",
		Aliases: []string{"u"},
		Short:   "Switch to the child of the current branch",
		Long:    `Switches to the child of the current branch. Stops at the top of the stack.`,
		Example: `  # move up two branches
  git up 2

  # this branch has two children; head towards fix-login-timeout
  git up --to fix-login-timeout`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: c.completeSteps(stack.Up),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.navigate(cmd, stack.Up, args, o)
		},
	}
	addStepsFlag(cmd, &o, c, stack.Up)
	cmd.Flags().StringVar(&o.to, "to", "", "branch to navigate towards; selects the path when several children exist")
	must(cmd.RegisterFlagCompletionFunc("to", c.completeUpstackBranches))
	return cmd
}
