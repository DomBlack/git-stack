package cmd

import (
	"github.com/spf13/cobra"
)

// newLogShortCmd is `git stack log short`; currentStack is log's persistent
// --stack flag.
func newLogShortCmd(c *cli, currentStack *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "short",
		Short: "Show every stack as a tree of branch names",
		Long: `Shows the same tree as git stack log with only the branch names; no PRs, state
or commits. It reads nothing but local git, so it never touches the network.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showLog(cmd.Context(), c, logMode{format: logShort, currentStack: *currentStack})
		},
	}
}
