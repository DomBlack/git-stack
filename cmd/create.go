package cmd

import (
	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
)

func newCreateCmd(c *cli) *cobra.Command {
	var (
		messages []string
		st       stagingFlags
		insert   bool
		useAI    bool
		noAI     bool
	)
	cmd := &cobra.Command{
		Use:     "create [name]",
		Aliases: []string{"c"},
		Short:   "Create a new branch stacked on top of the current branch",
		Long: `Create a new branch stacked on top of the current branch and commit the staged
changes. Without a name the branch name is derived from the commit message (or drafted
by --ai). With nothing staged an empty branch is created.`,
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
				Insert:   insert,
				UseAI:    ai,
				NoVerify: c.globals.NoVerify,
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
	cmd.Flags().StringArrayVarP(&messages, "message", "m", nil, "commit message (repeat for paragraphs)")
	must(cmd.RegisterFlagCompletionFunc("message", completeNothing))
	st.add(cmd)
	cmd.Flags().BoolVarP(&insert, "insert", "i", false, "insert between the current branch and its child (not supported by gh stack yet)")
	cmd.Flags().BoolVar(&useAI, "ai", false, "draft the branch name (and the commit message if -m is absent) with Claude Code; git config stack.ai.auto true makes this the default")
	cmd.Flags().BoolVar(&noAI, "no-ai", false, "never use AI; takes precedence over --ai and stack.ai.auto")
	return cmd
}

// stagingFlags are the -a / -u / -p flags shared by create and modify.
type stagingFlags struct {
	all, update, patch bool
}

func (s *stagingFlags) add(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&s.all, "all", "a", false, "stage all changes, including untracked files, before committing")
	cmd.Flags().BoolVarP(&s.update, "update", "u", false, "stage all changes to tracked files before committing")
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
