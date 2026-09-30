package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newSubmitCmd(c *cli) *cobra.Command {
	var (
		wholeStack bool
		draft      bool
		publish    bool
		edit       bool
		noEdit     bool
		updateOnly bool
		dryRun     bool
		useAI      bool
		noAI       bool
	)
	cmd := &cobra.Command{
		Use:     "submit",
		Aliases: []string{"ss"},
		Short:   "Push the stack and create or update its pull requests",
		Long: `Push every branch of the current stack and create or update a pull request for
each, chained onto its parent. New PRs are drafts unless --publish is given (or
git config stack.submit.default says otherwise). Without --no-edit, --ai or
--no-interactive, gh stack's editor opens for new PRs.

gh stack always submits the whole stack; Graphite's default is downstack only, so
pass --stack to acknowledge the difference and silence the notice.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			res, err := a.Submit(ctx, repo, app.SubmitOptions{
				Draft:      draft,
				Publish:    publish,
				NoEdit:     noEdit || (!edit && c.globals.NoInteractive),
				DryRun:     dryRun,
				UseAI:      useAI && !noAI,
				UpdateOnly: updateOnly,
			})
			if err != nil {
				return err
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			if !wholeStack && len(res.Stack) > 1 {
				fmt.Fprintln(errOut, "note: gh stack submits the whole stack (Graphite submits downstack by default); pass --stack to silence this")
			}
			if res.Output != "" && !c.globals.Quiet {
				fmt.Fprintln(errOut, res.Output)
			}
			if res.DryRun {
				fmt.Fprintln(out, "Dry run; nothing was pushed. Would submit:")
			}
			for _, pr := range res.PullRequests {
				switch {
				case pr.WouldCreate:
					state := "draft"
					if !res.Draft {
						state = "ready for review"
					}
					fmt.Fprintf(out, "  %s  (new PR, %s)\n", pr.Branch, state)
				case pr.Number > 0:
					marker := "updated"
					if pr.Created {
						marker = "created"
					}
					if res.DryRun {
						marker = "update"
					}
					fmt.Fprintf(out, "  %s  #%d %s %s\n", pr.Branch, pr.Number, marker, pr.URL)
				default:
					fmt.Fprintf(out, "  %s\n", pr.Branch)
				}
			}
			for _, n := range res.Notices {
				fmt.Fprintln(errOut, "note: "+n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&wholeStack, "stack", "s", false, "submit the whole stack (always the case with gh stack; silences the notice)")
	cmd.Flags().BoolVarP(&draft, "draft", "d", false, "create new PRs as drafts")
	cmd.Flags().BoolVarP(&publish, "publish", "p", false, "create new PRs ready for review")
	cmd.MarkFlagsMutuallyExclusive("draft", "publish")
	cmd.Flags().BoolVarP(&edit, "edit", "e", false, "open gh stack's editor for new PRs (default on a terminal)")
	cmd.Flags().BoolVarP(&noEdit, "no-edit", "n", false, "never open the editor; gh stack fills in titles from commits")
	cmd.MarkFlagsMutuallyExclusive("edit", "no-edit")
	cmd.Flags().BoolVarP(&updateOnly, "update-only", "u", false, "only update branches that already have PRs (not supported by gh stack)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be submitted without pushing")
	cmd.Flags().BoolVar(&useAI, "ai", false, "draft titles and descriptions for new PRs with Claude Code")
	cmd.Flags().BoolVar(&noAI, "no-ai", false, "never use AI; takes precedence over --ai")
	return cmd
}
