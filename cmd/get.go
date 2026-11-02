package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newGetCmd(c *cli) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <pr|url|branch>",
		Short: "Pull a pull request's stack down from GitHub and check it out",
		Long: `Pulls a pull request down from GitHub so you can work on it: its branch and
the branches of its stack are created locally (or fast forwarded where the
remote is ahead), tracked as a stack, and its branch is checked out. Name it
by number (123 or #123), URL or branch name.

A pull request in a stack on GitHub brings the whole stack, through gh stack.
One that isn't comes with the open pull requests it's based on, down to the
first base that has none, which becomes the trunk. A stack you already have
is just brought up to date.

Nothing is rebased or pushed, and a branch that has diverged from the remote
is left alone with a warning. Pull requests from forks can't be got this way.`,
		Example: `  # a colleague's pull request, by number
  git get 418

  # or by URL, or by branch name
  git get https://github.com/acme/payments/pull/418
  git get payments-retry-queue`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: c.completeOpenPRs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			res, err := a.Get(ctx, repo, app.GetOptions{Target: args[0]})
			rep := c.report()
			if err == nil {
				rep.Success("%s", getSummary(rep, res))
			}
			if res.Branch == "" {
				return err
			}
			renderGet(rep, res)
			return err
		},
	}
	return cmd
}

// getSummary is the result line: what was got and what's checked out.
func getSummary(rep *ui.Reporter, res app.GetResult) string {
	n := len(res.Branches)
	switch res.Source {
	case app.GetLocal:
		return fmt.Sprintf("Checked out %s; its stack was already here", rep.Branch(res.Branch))
	case app.GetPRs:
		return fmt.Sprintf("Tracking %d %s on %s and checked out %s", n, branchNoun(n), rep.Branch(res.Trunk), rep.Branch(res.Branch))
	default:
		return fmt.Sprintf("Pulled down a stack of %d %s on %s and checked out %s", n, branchNoun(n), rep.Branch(res.Trunk), rep.Branch(res.Branch))
	}
}

// renderGet prints what get did underneath the result line.
func renderGet(rep *ui.Reporter, res app.GetResult) {
	if res.Source == app.GetPRs {
		rep.Info("the pull request isn't in a stack on GitHub, so it was tracked as a stack of its own")
	}
	if len(res.Created) > 0 {
		names := make([]string, len(res.Created))
		for i, name := range res.Created {
			names[i] = rep.Branch(name)
		}
		rep.Info("created %s from the remote", joinAnd(names))
	}
	for _, u := range res.Updated {
		rep.Info("%s fast forwarded to %s", rep.Branch(u.Name), rep.SHA(u.To))
	}
	for _, n := range res.NotUpdated {
		if !n.Failed() {
			renderBlocked(rep, n)
		}
	}
	if len(res.NeedsRestack) > 0 {
		rep.Warn("%s %s not on the branch below; run git stack restack", strings.Join(res.NeedsRestack, ", "), isAre(len(res.NeedsRestack)))
	}
	for _, n := range res.Notices {
		rep.Warn("%s", n)
	}
}
