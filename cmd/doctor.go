package cmd

import (
	"context"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/gitconfig"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/ui"
	"github.com/DomBlack/git-stack/pkg/update"
)

func newDoctorCmd(c *cli) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that git-stack has what it needs, and fix what it can",
		Long: `Runs a list of checks, each with the checks that only make sense once it
passes: git and its version, gh and its login, the gh-stack extension and its
version, git-stack's own aliases and whether a newer release is out, and
inside a repository gh stack's stack file and the stacks
themselves (an interrupted restack, tracked branches that are gone, an empty
branch under others that submit would stop at).

Every problem says what to do about it. With --fix, the ones git-stack can
put right itself are fixed (installing or upgrading gh-stack, setting the
missing aliases, updating git-stack, moving stack files gh-stack 0.2 kept per
worktree into the shared one, forgetting branches that are gone) and everything is checked again. It exits non-zero while
anything still fails.`,
		Example: `  # see what's wrong
  git stack doctor

  # and fix what can be fixed
  git stack doctor --fix`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			a, repo, err := c.doctorApp(ctx)
			if err != nil {
				return err
			}
			target, err := c.updatePath()
			if err != nil {
				return err
			}
			info := c.currentVersion()
			extra := []doctor.Provider{
				gitconfig.New(c.runtime().Git),
				&update.Doctor{
					Client:  c.updaterClient(info),
					Options: update.Options{Current: info, Target: target, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
					AfterUpdate: func(ctx context.Context, exe string) error {
						return refreshInstall(ctx, c, exe)
					},
				},
			}
			res, err := a.Doctor(ctx, repo, app.DoctorOptions{Fix: fix, Extra: extra})
			if err != nil {
				return err
			}
			rep := c.report()
			rep.Print(ui.RenderDoctor(res.Checks, rep.Styles(), rep.Links()))
			return doctorOutcome(rep, res)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "fix what can be fixed automatically, then check again")
	return cmd
}

// doctorOutcome prints the result line, and is an error while a check
// still fails.
func doctorOutcome(rep *ui.Reporter, res app.DoctorResult) error {
	fixed := ""
	if res.Fixed > 0 {
		fixed = fmt.Sprintf(", %d fixed", res.Fixed)
	}
	if res.Failed > 0 {
		e := stack.Newf(stack.KindPartial, "%d %s failed%s", res.Failed, pluralWord(res.Failed, "check", "checks"), fixed)
		if res.Fixable > 0 {
			e = e.WithSteps("git stack doctor --fix fixes what it can")
		}
		return e
	}
	if res.Warned > 0 {
		rep.Partial("Checked git-stack's setup: %d %s%s", res.Warned, pluralWord(res.Warned, "warning", "warnings"), fixed)
		if res.Fixable > 0 {
			rep.NextStep("git stack doctor --fix fixes what it can")
		}
		return nil
	}
	rep.Success("Checked git-stack's setup: everything is in order%s", fixed)
	return nil
}
