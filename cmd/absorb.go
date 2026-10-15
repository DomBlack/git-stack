package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newAbsorbCmd(c *cli) *cobra.Command {
	var (
		st     stagingFlags
		dryRun bool
		force  bool
	)
	cmd := &cobra.Command{
		Use:     "absorb",
		Aliases: []string{"ab"},
		Short:   "Fold staged changes into the commits in this stack that last touched those lines",
		Long: `Takes what is staged and folds each changed run of lines into the commit in
this stack that last changed those lines: the current branch's commits and the
branches below it, down to (not including) trunk or a merged branch. Those
commits and everything above them are rewritten, keeping their messages and
authors, and the branches above the current one are restacked.

It goes by lines, not by meaning. A run of changed lines spanning lines that
two commits changed goes into the newer of the two. A change to lines no commit
in the stack touched stays staged, as does a new or deleted file. On a
terminal you see the plan and get asked before anything is rewritten.

-a, -u and -p stage first, dry run or not. The old tips are printed, so
"git branch -f <branch> <old tip>" followed by "git stack restack" undoes it.`,
		Example: `  # stage your fixes, see where each one would go
  git add -p
  git absorb --dry-run

  # fold every change to tracked files into the right commits
  git absorb -u`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			rep := c.report()
			res, err := a.Absorb(ctx, repo, app.AbsorbOptions{
				Staging: st.mode(), DryRun: dryRun, Force: force,
				Preview: func(p *app.AbsorbPlan) { renderAbsorbPlan(rep, p) },
			})
			if res.Applied {
				// Shown before any error: the absorb happened even when the
				// restack above it stopped on a conflict.
				renderAbsorbResult(rep, res)
			}
			if err != nil {
				return err
			}
			if !res.Applied {
				for _, n := range res.Notices {
					rep.Warn("%s", n)
				}
				switch {
				case res.Plan.Absorbed == 0:
					rep.Success("Nothing to absorb")
				case res.DryRun:
					rep.Warn("Dry run; nothing was absorbed")
				}
			}
			return nil
		},
	}
	st.add(cmd)
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "show where each change would go without rewriting any commit")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "don't ask before rewriting the commits")
	return cmd
}

// renderAbsorbPlan prints one line per staged hunk with its destination,
// then the files that were skipped.
func renderAbsorbPlan(rep *ui.Reporter, p *app.AbsorbPlan) {
	for _, h := range p.Hunks {
		loc := ui.QuoteName(h.Path) + ":" + hunkLines(h)
		if h.Target == nil {
			rep.Info("%s  not absorbed; no commit in this stack changed those lines", loc)
			continue
		}
		rep.Info("%s  into %s %s (%s)", loc, rep.SHA(h.Target.SHA), h.Target.Subject, rep.Branch(h.Target.Branch))
	}
	for _, s := range p.Skipped {
		rep.Info("%s  skipped; %s", ui.QuoteName(s.Path), s.Reason)
	}
}

// hunkLines says which lines of the file a hunk changes: "120-125", "12",
// or "after 300" for an insertion.
func hunkLines(h app.AbsorbHunk) string {
	switch h.OldCount {
	case 0:
		return fmt.Sprintf("after %d", h.OldStart)
	case 1:
		return fmt.Sprintf("%d", h.OldStart)
	}
	return fmt.Sprintf("%d-%d", h.OldStart, h.OldStart+h.OldCount-1)
}

// renderAbsorbResult prints what an applied absorb did.
func renderAbsorbResult(rep *ui.Reporter, res app.AbsorbResult) {
	for _, n := range res.Notices {
		rep.Warn("%s", n)
	}
	rep.Success("Absorbed %d %s into %d %s", res.Plan.Absorbed, hunkNoun(res.Plan.Absorbed), res.Plan.Commits, commitNoun(res.Plan.Commits))
	for _, m := range res.Rewritten {
		rep.Info("%s  rewritten, was %s", rep.Branch(m.Name), rep.SHA(m.From))
	}
	if n := len(res.Restacked); n > 0 {
		rep.Success("Restacked %d %s above %s", n, branchNoun(n), rep.Branch(res.Branch))
	}
	stillStaged(rep, res.Plan)
}

// stillStaged notes what an applied absorb left in the index: hunks it
// found no commit for and files it skipped.
func stillStaged(rep *ui.Reporter, p app.AbsorbPlan) {
	var parts []string
	if p.Unabsorbed > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", p.Unabsorbed, hunkNoun(p.Unabsorbed)))
	}
	if n := len(p.Skipped); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralWord(n, "file", "files")))
	}
	if len(parts) == 0 {
		return
	}
	rep.Warn("Still staged: %s", strings.Join(parts, ", "))
}

func hunkNoun(n int) string   { return pluralWord(n, "hunk", "hunks") }
func commitNoun(n int) string { return pluralWord(n, "commit", "commits") }

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
