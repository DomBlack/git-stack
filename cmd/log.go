package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newLogCmd(c *cli) *cobra.Command {
	var currentStack bool
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show every stack as a tree with PR state",
		Long: `Shows every stack in the repo as a tree; trunk at the bottom, each stack rising
out of it, the current branch marked, and under each branch its PR and title,
the PR's state, its age, whether it needs a restack or a push (you've changed it
since the last push, so its PR is behind), and its own commits, newest first
(ten, then a count). A repo with no stacks yet just shows its trunk. PR state
and titles come from a local cache and are refreshed when they go stale; the
commits come from local git.

--stack shows only the current branch's stack (every stack on it, on a trunk).
git stack log short shows just the branch names and git stack log long the
full tree; plain log and bare git stack show the full tree unless
git config stack.log.short is true.

Bare git stack runs this.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showLog(cmd.Context(), c, logMode{currentStack: currentStack})
		},
	}
	cmd.PersistentFlags().BoolVarP(&currentStack, "stack", "s", false, "only show the current branch's stack")
	cmd.AddCommand(newLogShortCmd(c, &currentStack), newLogLongCmd(c, &currentStack))
	return cmd
}

// logFormat is how much of each branch the log shows.
type logFormat int

const (
	// logDefault follows stack.log.short.
	logDefault logFormat = iota
	logShort
	logLong
)

// logMode picks what showLog renders.
type logMode struct {
	format logFormat
	// currentStack shows only the current branch's stack.
	currentStack bool
}

// showLog renders the stack tree; it backs `git stack log` (and its short
// and long), and a bare `git stack`.
func showLog(ctx context.Context, c *cli, m logMode) error {
	a, repo, err := c.app(ctx)
	if err != nil {
		return err
	}
	short := m.format == logShort
	if m.format == logDefault {
		cfg, err := c.runtime().Config(ctx)
		if err != nil {
			return err
		}
		short = cfg.LogShort
	}
	o := app.ViewOptions{CurrentStackOnly: m.currentStack, PRs: app.PRsFresh, Commits: true}
	if short {
		// Names and restack markers only: no PR state or commits to work out.
		o = app.ViewOptions{CurrentStackOnly: m.currentStack}
	}
	v, err := a.View(ctx, repo, o)
	if err != nil {
		return err
	}
	rep := c.report()
	if m.currentStack && len(v.Graph.Stacks) > 0 && !v.Graph.Tracked(v.Current) && !v.Graph.IsTrunk(v.Current) {
		if v.Current == "" {
			rep.Warn("Not on a branch, so showing every stack")
		} else {
			rep.Warn("%s is in no stack, so showing every stack", rep.Branch(v.Current))
		}
	}
	out := ui.RenderLog(v, ui.LogOptions{Styles: rep.Styles(), Now: time.Now(), Links: rep.Links(), PRURL: rep.PRURL, Width: rep.Width(), Short: short})
	if out == "" {
		// Not even a trunk: no stacks and no default branch to show.
		rep.Warn("No stacks yet; check out your trunk and run git stack create to start one")
		return nil
	}
	rep.Print(out)
	return nil
}
