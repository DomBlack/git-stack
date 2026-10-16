package cmd

import (
	"runtime"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// browserCommand is what opens a URL in the user's browser.
func browserCommand() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func newPRCmd(c *cli) *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "pr [branch]",
		Short: "Open a branch's pull request in the browser",
		Long: `Opens the pull request of a stacked branch (the current one by default) in
your browser. Without a terminal, or with --print, it prints the pull
request's URL instead.

The pull request comes from what git-stack already knows locally; GitHub is
only asked when nothing local knows the branch's pull request.`,
		Example: `  # open this branch's pull request
  git pr

  # print the URL of feat-a's pull request
  git pr --print feat-a`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: c.completeStackedBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			var branch string
			if len(args) == 1 {
				branch = args[0]
			}
			pr, err := a.BranchPR(ctx, repo, branch)
			if err != nil {
				return err
			}
			rt := c.runtime()
			rep := rt.Report
			if printOnly || !rt.Interactive {
				rep.Print(rep.Link(pr.URL) + "\n")
				return nil
			}
			if _, err := rt.Runner.Run(ctx, exec.Cmd{Name: browserCommand(), Args: []string{pr.URL}}); err != nil {
				rt.Log.Debug("open browser", "err", err)
				rep.Warn("Couldn't open your browser; here's the link to #%d (%s)", pr.Number, pr.Head)
				rep.Print(rep.Link(pr.URL) + "\n")
				return nil
			}
			rep.Success("Opened %s (%s) in your browser", rep.Ref(pr.Number, pr.URL, pr.State), rep.Branch(pr.Head))
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the URL instead of opening it")
	return cmd
}
