package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newContinueCmd(c *cli) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "continue",
		Aliases: []string{"cont"},
		Short:   "Continue an interrupted restack once its conflicts are resolved",
		Long: `Finish the rebase a restack or modify stopped on, now that the conflicts are resolved
and git added, then restack whatever was left above it. The same as
"git stack restack --continue".`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			res, err := a.Restack(ctx, repo, app.RestackOptions{Continue: true, StageAll: all})
			if err != nil {
				return err
			}
			renderRestack(c.report(), res, false)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "stage every change first")
	return cmd
}
