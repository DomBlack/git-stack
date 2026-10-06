package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newAbortCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "abort",
		Short: "Abort an interrupted restack and put every moved branch back",
		Long: `Gives up on the rebase a restack or modify stopped on and puts back every branch
the operation had already moved, metadata included. The same as
"git stack restack --abort".`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			res, err := a.Restack(ctx, repo, app.RestackOptions{Abort: true})
			if err != nil {
				return err
			}
			renderRestack(c.report(), res, true)
			return nil
		},
	}
}
