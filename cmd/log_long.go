package cmd

import (
	"github.com/spf13/cobra"
)

// newLogLongCmd is `git stack log long`; currentStack is log's persistent
// --stack flag.
func newLogLongCmd(c *cli, currentStack *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "long",
		Short: "Show every stack as a tree with PR state and commits",
		Long: `Shows the full tree, as git stack log does without stack.log.short: under each
branch its PR, state and own commits.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showLog(cmd.Context(), c, logMode{format: logLong, currentStack: *currentStack})
		},
	}
}
