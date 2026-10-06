package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newCreateCmd(c *cli) *cobra.Command {
	var (
		messages []string
		st       stagingFlags
		useAI    bool
		noAI     bool
		noVerify bool
	)
	cmd := &cobra.Command{
		Use:     "create [name]",
		Aliases: []string{"c"},
		Short:   "Create a new branch stacked on top of the current branch",
		Long: `Creates a new branch on top of the current one and commits whatever is staged.
It needs a name, -m or --ai; without a name the branch name comes from the
commit message (or from --ai). If nothing is staged you get an empty branch.

It works from trunk (starting a new stack) or from the top of a stack, since gh
stack can only add branches at the top. Run git stack top first if you're
further down.`,
		Example: `  # stage everything; the branch name comes from the message
  git stack create -a -m "Add retries to the billing webhook"

  # pick the name yourself, with a second paragraph for the commit body
  git create fix-login -m "Fix the login timeout" -m "Sessions expired early."

  # let Claude name the branch and write the message from the diff
  git create -a --ai`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			staging := st.mode()
			a, repo, err := c.app(ctx)
			if err != nil {
				return err
			}
			ai, err := c.wantAI(ctx, useAI, noAI)
			if err != nil {
				return err
			}
			o := app.CreateOptions{
				Message:  messages,
				Staging:  staging,
				UseAI:    ai,
				NoVerify: noVerify,
			}
			if len(args) == 1 {
				o.Name = args[0]
			}
			res, err := a.Create(ctx, repo, o)
			if err != nil {
				return err
			}
			rep := c.report()
			switch {
			case res.Commit != nil:
				rep.Success("Created %s on %s  %s %s", rep.Branch(res.Branch), rep.Branch(res.Parent), rep.SHA(res.Commit.Short()), res.Commit.Subject)
			default:
				rep.Success("Created empty branch %s on %s (nothing was staged)", rep.Branch(res.Branch), rep.Branch(res.Parent))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&messages, "message", "m", nil, "commit message; repeat it for more paragraphs")
	must(cmd.RegisterFlagCompletionFunc("message", completeNothing))
	st.add(cmd)
	cmd.Flags().BoolVar(&useAI, "ai", false, "have Claude Code name the branch and write the message")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip git hooks when committing")
	cmd.Flags().BoolVar(&noAI, "no-ai", false, "never use AI; takes precedence over --ai and stack.ai.auto")
	return cmd
}

// stagingFlags are the -a / -u / -p flags shared by create and modify.
type stagingFlags struct {
	all, update, patch bool
}

func (s *stagingFlags) add(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&s.all, "all", "a", false, "stage all changes, including untracked files, before committing")
	cmd.Flags().BoolVarP(&s.update, "update", "u", false, "stage changes to tracked files only, before committing")
	cmd.Flags().BoolVarP(&s.patch, "patch", "p", false, "pick hunks to stage before committing")
	cmd.MarkFlagsMutuallyExclusive("all", "update", "patch")
}

func (s *stagingFlags) mode() app.StagingMode {
	switch {
	case s.all:
		return app.StageAll
	case s.update:
		return app.StageUpdate
	case s.patch:
		return app.StagePatch
	default:
		return app.StageNone
	}
}
