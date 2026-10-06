package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/stack"
)

func newBottomCmd(c *cli) *cobra.Command {
	var o navOptions
	cmd := &cobra.Command{
		Use:     "bottom",
		Aliases: []string{"b"},
		Short:   "Switch to the branch closest to trunk in the current stack",
		Long: `Switches to the branch closest to trunk in the current stack. On a trunk with
several stacks, --to picks which one.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.navigate(cmd, stack.Bottom, args, o)
		},
	}
	cmd.Flags().StringVar(&o.to, "to", "", "branch whose stack to enter when starting from a trunk with several stacks")
	must(cmd.RegisterFlagCompletionFunc("to", c.completeUpstackBranches))
	return cmd
}
