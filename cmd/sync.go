package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/shell"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newSyncCmd(c *cli) *cobra.Command {
	var (
		force     bool
		deleteAll bool
		noRestack bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Fetch, move trunk, delete merged branches and restack every stack",
		Long: `Brings every stack up to date with the remote, all without checking anything
out;

  1. fetches and fast forwards each trunk
  2. deletes branches whose PRs have been merged or closed, as long as every
     commit on your local branch made it onto the PR (set git config
     stack.sync.prune to "ask" to be asked first, or "never" to keep them)
  3. fast forwards any branch that moved on the remote
  4. restacks every stack onto its updated parent

Branches above a deleted one are restacked onto whatever was below it. Squash
merges are fine; only the commits a branch added on top of its parent are
replayed.

Nothing gets pushed; that's what git stack submit is for. If your local trunk
has diverged from the remote, it's only reset with -f or a yes at the prompt.`,
		Example: `  # the usual; fetch, tidy up merged branches and restack
  git sync

  # don't ask before deleting anything, and reset a diverged trunk
  git sync -f`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			res, err := a.Sync(ctx, repo, app.SyncOptions{Force: force, DeleteAll: deleteAll, NoRestack: noRestack})
			return reportSync(rep, res, err)
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "like -d, and also reset a diverged trunk to the remote")
	cmd.Flags().BoolVarP(&deleteAll, "delete-all", "d", false, "delete merged or closed branches without asking")
	cmd.Flags().BoolVar(&noRestack, "no-restack", false, "skip the restack")
	return cmd
}

// reportSync prints how a sync went: the one line summary (a warning when
// something it meant to update was left alone), what it did underneath, then
// the notices. When err is set (a conflict, a branch it couldn't update) the
// summary is left to the error, but what did happen is still shown, since
// one stack failing doesn't stop the others. merge uses it for the sync it
// runs afterwards.
func reportSync(rep *ui.Reporter, res app.SyncResult, err error) error {
	if err == nil {
		if line, partial := syncSummary(res); partial {
			rep.Partial("%s", line)
		} else {
			rep.Success("%s", line)
		}
	}
	// What was left and what to do about it goes straight under the summary.
	for _, n := range res.NotUpdated {
		if !n.Failed() {
			renderBlocked(rep, n)
		}
	}
	renderSync(rep, res)
	return err
}

// renderBlocked explains a branch sync left because the user's own files
// were in the way: which files, in which checkout, and what to run.
//
//	main: uncommitted changes in ~/src/app would be overwritten: a.go, b.go
//	↳ commit them, or git -C ~/src/app stash, then git stack sync again
func renderBlocked(rep *ui.Reporter, n app.NotUpdated) {
	where := ui.QuoteName(ui.ShortPath(n.Worktree))
	if len(n.Changed) > 0 {
		rep.Info("%s: uncommitted changes in %s would be overwritten: %s", rep.Branch(n.Name), where, fileList(n.Changed, n.MoreChanged))
	}
	if len(n.Untracked) > 0 {
		rep.Info("%s: untracked files in %s are in the way: %s", rep.Branch(n.Name), where, fileList(n.Untracked, n.MoreUntracked))
	}
	dir, pasteable := shell.Path(ui.ShortPath(n.Worktree))
	if !pasteable {
		// A control character in the path: any command we printed would
		// target the wrong place if pasted, so say what to do instead.
		switch {
		case len(n.Changed) > 0 && len(n.Untracked) > 0:
			rep.NextStep("commit, stash or move them in the checkout at %s, then git stack sync again", where)
		case len(n.Changed) > 0:
			rep.NextStep("commit or stash them in the checkout at %s, then git stack sync again", where)
		default:
			rep.NextStep("move or delete them in the checkout at %s, then git stack sync again", where)
		}
		return
	}
	switch {
	case len(n.Changed) > 0 && len(n.Untracked) > 0:
		rep.NextStep("commit or move them, or git -C %s stash -u, then git stack sync again", dir)
	case len(n.Changed) > 0:
		rep.NextStep("commit them, or git -C %s stash, then git stack sync again", dir)
	default:
		rep.NextStep("move or delete them, or git -C %s stash -u, then git stack sync again", dir)
	}
}

// maxShownFiles is how many files a warning names before "and N more".
const maxShownFiles = 3

// fileList names up to three files: "a.go", "a.go and b.go", "a.go, b.go,
// c.go and 4 more". more counts files already left out of files.
func fileList(files []string, more int) string {
	if len(files) > maxShownFiles {
		more += len(files) - maxShownFiles
		files = files[:maxShownFiles]
	}
	// Names come from the repository, so anything odd in them is quoted.
	shown := make([]string, len(files))
	for i, f := range files {
		shown[i] = ui.QuoteName(f)
	}
	files = shown
	if more > 0 {
		return strings.Join(files, ", ") + fmt.Sprintf(" and %d more", more)
	}
	return joinAnd(files)
}

// renderSync prints what a sync did, line by line.
func renderSync(rep *ui.Reporter, res app.SyncResult) {
	for _, t := range res.Trunks {
		switch t.Status {
		case app.TrunkFastForwarded:
			rep.Info("%s fast forwarded to %s", rep.Branch(t.Name), rep.SHA(t.To))
		case app.TrunkReset:
			rep.Info("%s reset to %s/%s at %s", rep.Branch(t.Name), res.Remote, t.Name, rep.SHA(t.To))
		case app.TrunkAhead:
			rep.Info("%s is ahead of %s/%s; left alone", rep.Branch(t.Name), res.Remote, t.Name)
		}
	}
	for _, d := range res.Deleted {
		if d.Head == "" {
			rep.Info("deleted %s (%s)", rep.Branch(d.Name), d.Reason)
			continue
		}
		rep.Info("deleted %s (%s, was %s)", rep.Branch(d.Name), d.Reason, rep.SHA(d.Head))
	}
	for _, u := range res.Updated {
		rep.Info("%s fast forwarded to %s from %s", rep.Branch(u.Name), rep.SHA(u.To), res.Remote)
	}
	for _, m := range res.Restacked {
		rep.Info("restacked %s", rep.Branch(m.Name))
	}
	for _, n := range res.Notices {
		rep.Warn("%s", n)
	}
}

// syncSummary is the one line result, and whether it is a partial one:
// "Synced: main fast forwarded, 2 branches deleted, 3 restacked", "Synced,
// nothing to do", or "Synced, but main was not updated" when something sync
// meant to update was left alone for a reason the user has to deal with.
func syncSummary(res app.SyncResult) (string, bool) {
	var parts, left []string
	for _, t := range res.Trunks {
		switch t.Status {
		case app.TrunkFastForwarded:
			parts = append(parts, t.Name+" fast forwarded")
		case app.TrunkReset:
			parts = append(parts, fmt.Sprintf("%s reset to %s/%s", t.Name, res.Remote, t.Name))
		case app.TrunkDiverged:
			left = append(left, fmt.Sprintf("%s has diverged from %s/%s", t.Name, res.Remote, t.Name))
		}
	}
	if n := len(res.Deleted); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s deleted", n, branchNoun(n)))
	}
	if n := len(res.Updated); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s updated from the remote", n, branchNoun(n)))
	}
	if n := len(res.Restacked); n > 0 {
		parts = append(parts, fmt.Sprintf("%d restacked", n))
	}
	var stuck []string
	for _, n := range res.NotUpdated {
		stuck = append(stuck, n.Name)
	}
	if len(stuck) > 0 {
		verb := "was"
		if len(stuck) > 1 {
			verb = "were"
		}
		left = append(left, joinAnd(stuck)+" "+verb+" not updated")
	}
	line := "Synced, nothing to do"
	if len(parts) > 0 {
		line = "Synced: " + strings.Join(parts, ", ")
	}
	if len(left) > 0 {
		if len(parts) == 0 {
			line = "Synced"
		}
		line += ", but " + strings.Join(left, " and ")
	}
	return line, len(left) > 0
}

// joinAnd lists names as "a", "a and b" or "a, b and c".
func joinAnd(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
