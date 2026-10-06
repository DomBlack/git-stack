package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/stack"
)

func newTopCmd(c *cli) *cobra.Command {
	var o navOptions
	cmd := &cobra.Command{
		Use:     "top",
		Aliases: []string{"t"},
		Short:   "Switch to the tip branch of the current stack",
		Long: `Switches to the tip branch of the current stack. On a trunk with several stacks,
--to picks which one.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.navigate(cmd, stack.Top, args, o)
		},
	}
	cmd.Flags().StringVar(&o.to, "to", "", "branch whose stack to climb when starting from a trunk with several stacks")
	must(cmd.RegisterFlagCompletionFunc("to", c.completeUpstackBranches))
	return cmd
}
