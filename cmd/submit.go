package cmd

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newSubmitCmd(c *cli) *cobra.Command {
	var (
		draft   bool
		publish bool
		edit    bool
		noEdit  bool
		dryRun  bool
		useAI   bool
		noAI    bool
		force   bool
	)
	cmd := &cobra.Command{
		Use:     "submit",
		Aliases: []string{"ss"},
		Short:   "Push the stack and create or update its pull requests",
		Long: `Pushes every branch in the stack and creates or updates a PR for each one, each
based on the branch below it. New PRs are ready for review by default; pass
--draft, or set git config stack.submit.default to draft (or ask, to be asked
each time).

Unless you pass --no-edit, --ai or --no-interactive, gh stack opens its editor
for the title and body of each new PR. --draft, --publish, --ai and the editor
only apply to new PRs.

gh stack does the push, with --force-with-lease, so after a restack your
branches are force pushed. If a branch has commits on the remote that aren't in
your local one (someone else pushed to it), submit refuses rather than
overwrite them. Run git stack sync to pull them in, or pass --force if you
really do want them gone.`,
		Example: `  # push the stack and open PRs for any new branches
  git ss

  # open the new PRs as drafts, with titles taken from the commits
  git ss -d -n

  # see what would be created without pushing anything
  git stack submit --dry-run`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			ai, err := c.wantAI(ctx, useAI, noAI)
			if err != nil {
				return err
			}
			res, err := a.Submit(ctx, repo, app.SubmitOptions{
				Draft:   draft,
				Publish: publish,
				NoEdit:  noEdit || (!edit && c.globals.NoInteractive),
				DryRun:  dryRun,
				UseAI:   ai,
				Force:   force,
			})
			if err != nil {
				return err
			}
			if res.Output != "" {
				// Not streamed live (no terminal gutter was available); show it now.
				_, _ = io.WriteString(rep.Stream(), res.Output+"\n")
			}
			if res.DryRun {
				rep.Warn("Dry run; nothing was pushed. Would submit:")
			} else {
				n := len(res.PullRequests)
				rep.Success("Submitted %d %s", n, branchNoun(n))
			}
			for _, pr := range res.PullRequests {
				switch {
				case pr.WouldCreate:
					state := "draft"
					if !res.Draft {
						state = "ready for review"
					}
					rep.Info("%s  (new PR, %s)", rep.Branch(pr.Branch), state)
				case pr.Number > 0:
					marker := "updated"
					if pr.Created {
						marker = "created"
					}
					if res.DryRun {
						marker = "update"
					}
					rep.Info("%s  %s %s %s", rep.Branch(pr.Branch), rep.Ref(pr.Number, pr.URL), marker, rep.Link(pr.URL))
				default:
					rep.Info("%s", rep.Branch(pr.Branch))
				}
			}
			for _, n := range res.Notices {
				rep.Warn("%s", n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&draft, "draft", "d", false, "create new PRs as drafts")
	cmd.Flags().BoolVarP(&publish, "publish", "p", false, "create new PRs ready for review (default; see stack.submit.default)")
	cmd.MarkFlagsMutuallyExclusive("draft", "publish")
	cmd.Flags().BoolVarP(&edit, "edit", "e", false, "open gh stack's editor for new PRs (default on a terminal)")
	cmd.Flags().BoolVarP(&noEdit, "no-edit", "n", false, "never open the editor; gh stack fills in titles from commits")
	cmd.MarkFlagsMutuallyExclusive("edit", "no-edit")
	// --stack used to silence a notice, and git ss was installed as
	// "stack submit --stack". Keep accepting it so those aliases still work.
	cmd.Flags().BoolP("stack", "s", false, "backwards compatibility - no-op")
	must(cmd.Flags().MarkHidden("stack"))
	cmd.Flags().BoolVarP(&force, "force", "f", false, "push even if it overwrites commits someone else pushed")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be submitted without pushing")
	cmd.Flags().BoolVar(&useAI, "ai", false, "have Claude Code write titles and bodies for new PRs (see stack.ai.auto)")
	cmd.Flags().BoolVar(&noAI, "no-ai", false, "never use AI; takes precedence over --ai and stack.ai.auto")
	return cmd
}
