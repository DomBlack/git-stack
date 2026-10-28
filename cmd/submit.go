package cmd

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
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
		down    bool
	)
	cmd := &cobra.Command{
		Use:     "submit [branch]",
		Aliases: []string{"ss"},
		Short:   "Push the stack and create or update its pull requests",
		Long: `Pushes every branch in the stack and creates or updates a PR for each one, each
based on the branch below it. With --down, or a branch name, it stops at the
current (or named) branch and leaves the ones above it alone. New PRs are ready
for review by default; pass --draft, or set git config stack.submit.default to
draft (or ask, to be asked each time).

Unless you pass --no-edit, --ai or --no-interactive, gh stack opens its editor
for the title and body of each new PR. --draft, --publish, --ai and the editor
only apply to new PRs. gh stack can only submit a whole stack, so with --down
we push those branches ourselves and have gh stack link add their PRs to the
stack; the editor isn't available there, so new PRs take their titles from
their commits unless you pass --ai.

If a PR lower in the stack has merged since you last synced, submit runs
git stack sync first, so the merged branch goes and the ones above it are
restacked onto trunk before anything is pushed; otherwise the next PR up would
show the merged commits too. A dry run only says which branches that would be.

Branches are pushed with --force-with-lease, so after a restack your
branches are force pushed. If a branch has commits on the remote that aren't in
your local one (someone else pushed to it), submit refuses rather than
overwrite them. Run git stack sync to pull them in, or pass --force if you
really do want them gone.`,
		Example: `  # push the stack and open PRs for any new branches
  git ss

  # push only the current branch and the ones below it
  git ss --down

  # push feat-b and the ones below it, whichever branch is checked out
  git ss feat-b

  # open the new PRs as drafts, with titles taken from the commits
  git ss -d -n

  # see what would be created without pushing anything
  git stack submit --dry-run`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: c.completeBranches,
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
			opts := app.SubmitOptions{
				Draft:   draft,
				Publish: publish,
				NoEdit:  noEdit || (!edit && c.globals.NoInteractive),
				DryRun:  dryRun,
				UseAI:   ai,
				Force:   force,
				Down:    down,
			}
			if len(args) == 1 {
				opts.Branch = args[0]
			}
			res, err := a.Submit(ctx, repo, opts)
			if res.Sync != nil {
				rep.Warn("%s, so the stack was synced first", mergedPhrase(res.Merged))
				if err != nil {
					// The error owns the summary line; what the sync did
					// manage is still worth seeing.
					renderSync(rep, *res.Sync)
					return err
				}
				_ = reportSync(rep, *res.Sync, nil)
			}
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
					rep.Info("%s  %s %s %s", rep.Branch(pr.Branch), rep.Ref(pr.Number, pr.URL, pr.State), marker, rep.Link(pr.URL))
				default:
					rep.Info("%s", rep.Branch(pr.Branch))
				}
			}
			for _, c := range res.Closed {
				gone := ", and deleted its branch"
				if !c.BranchDeleted {
					gone = ""
				}
				rep.Info("%s  %s closed%s", rep.Branch(c.Branch), rep.Ref(c.Number, "", forge.StateClosed), gone)
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
	cmd.Flags().BoolVar(&down, "down", false, "only submit the current branch and the ones below it")
	cmd.Flags().BoolVar(&useAI, "ai", false, "have Claude Code write titles and bodies for new PRs (see stack.ai.auto)")
	cmd.Flags().BoolVar(&noAI, "no-ai", false, "never use AI; takes precedence over --ai and stack.ai.auto")
	return cmd
}

// mergedPhrase says whose pull requests have merged: "a's pull request has
// merged", "the pull requests of a and b have merged".
func mergedPhrase(names []string) string {
	if len(names) == 1 {
		return names[0] + "'s pull request has merged"
	}
	return "the pull requests of " + joinAnd(names) + " have merged"
}
