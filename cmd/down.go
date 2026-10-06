package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/stack"
)

func newDownCmd(c *cli) *cobra.Command {
	var o navOptions
	cmd := &cobra.Command{
		Use:     "down [steps]",
		Aliases: []string{"d"},
		Short:   "Switch to the parent of the current branch",
		Long: `Switches to the parent of the current branch. From the bottom branch it checks
out trunk.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: c.completeSteps(stack.Down),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.navigate(cmd, stack.Down, args, o)
		},
	}
	addStepsFlag(cmd, &o, c, stack.Down)
	return cmd
}
