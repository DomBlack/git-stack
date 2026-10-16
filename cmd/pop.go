package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

func newPopCmd(c *cli) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pop",
		Short: "Take the current branch out of its stack, keeping its changes staged",
		Long: `Deletes the current branch and takes it out of its stack, leaving its changes
staged on its parent, as if you had run git reset --soft onto the parent. The
branches above it are restacked onto the parent first, so they no longer carry
its commits.

The working tree must have no changes to tracked files (untracked files are
fine), the branch must sit on its parent (git stack restack puts it there), and
the parent can't be checked out in another worktree. A conflict stops like a
restack does: fix the files, git add them and run "git stack continue", which
then finishes the pop, or "git stack abort" to leave everything as it was. If
the branch has an open pull request, the next submit closes it.`,
		Example: `  # redo the current branch from scratch
  git pop`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			res, err := a.Pop(ctx, repo)
			renderNotices(rep, res.Notices)
			if err != nil {
				return err
			}
			gone := ""
			if res.StackGone {
				gone = " (the stack is gone)"
			}
			rep.Success("Popped %s; its changes are staged on %s%s", res.Branch, res.Parent, gone)
			if len(res.Restacked) > 0 {
				rep.Success("Restacked %s", strings.Join(res.Restacked, ", "))
			}
			if res.Closing > 0 {
				rep.Info("#%d will be closed at the next submit", res.Closing)
			}
			return nil
		},
	}
	return cmd
}
