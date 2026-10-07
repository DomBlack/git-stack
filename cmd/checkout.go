package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func newCheckoutCmd(c *cli) *cobra.Command {
	var (
		trunk         bool
		currentStack  bool
		showUntracked bool
		all           bool
	)
	cmd := &cobra.Command{
		Use:     "checkout [branch]",
		Aliases: []string{"co"},
		Short:   "Switch to a branch",
		Long: `Switches to a branch. With no branch it opens an interactive picker showing
every stack as a tree from its trunk; type to filter, arrows or j/k to move,
enter to switch and esc to cancel.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: c.completeBranches,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			var target string
			switch {
			case trunk:
				target, err = a.Trunk(ctx, repo)
				if err != nil {
					return err
				}
			case len(args) == 1:
				target = args[0]
			default:
				target, err = c.pickBranch(ctx, a, repo, app.ViewOptions{
					CurrentStackOnly: currentStack,
					IncludeUntracked: showUntracked || all,
					PRs:              app.PRsCached,
				})
				if err != nil || target == "" {
					return err
				}
			}
			if err := a.Checkout(ctx, repo, target); err != nil {
				return err
			}
			rep := c.report()
			rep.Success("Checked out %s", rep.Branch(target))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&trunk, "trunk", "t", false, "check out the trunk of the current stack")
	cmd.Flags().BoolVarP(&currentStack, "stack", "s", false, "only show the current stack in the picker")
	cmd.Flags().BoolVarP(&showUntracked, "show-untracked", "u", false, "include branches that are in no stack in the picker")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "show every trunk and untracked branches in the picker")
	return cmd
}

// pickBranch opens the tree picker. It returns "" when the user cancels.
func (c *cli) pickBranch(ctx context.Context, a *app.App, repo git.Repo, o app.ViewOptions) (string, error) {
	rt := c.runtime()
	if !rt.Interactive {
		return "", stack.New(stack.KindInteractionRequired, "no branch given and no terminal for the picker").
			WithSteps("git stack checkout <branch>", "git stack checkout --trunk")
	}
	v, err := a.View(ctx, repo, o)
	if err != nil {
		return "", err
	}
	opts := ui.PickerOptions{Rows: v.Rows, Links: rt.Report.Links(), PRURL: rt.Report.PRURL}
	if _, state, _ := a.CachedPRs(repo); state != cache.Fresh {
		opts.Refresh = func(ctx context.Context) ([]forge.PullRequest, error) {
			return a.RefreshPRs(ctx, repo)
		}
	}
	done := rt.Report.Waiting("Select a branch")
	res, err := ui.RunPicker(ctx, rt.Streams.In, rt.Streams.Out, opts)
	done()
	if err != nil {
		return "", err
	}
	if res.Cancelled {
		return "", nil
	}
	return res.Branch, nil
}
