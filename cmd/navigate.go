package cmd

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/stack"
)

// navOptions are the flags shared by up and down.
type navOptions struct {
	steps int
	to    string
}

func addStepsFlag(cmd *cobra.Command, o *navOptions, c *cli, dir stack.Direction) {
	cmd.Flags().IntVarP(&o.steps, "steps", "n", 1, "number of levels to traverse")
	must(cmd.RegisterFlagCompletionFunc("steps", c.completeStepsFlag(dir)))
}

// navigate runs a navigation command and prints the outcome Graphite-style.
func (c *cli) navigate(cmd *cobra.Command, dir stack.Direction, args []string, o navOptions) error {
	ctx := cmd.Context()
	steps := o.steps
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return stack.Newf(stack.KindInvalidArgs, "steps must be a positive integer, got %q", args[0])
		}
		steps = n
	}
	switch dir {
	case stack.Up, stack.Down:
		if steps < 1 {
			return stack.Newf(stack.KindInvalidArgs, "--steps must be at least 1, got %d", steps)
		}
	default:
		steps = 0
	}

	a, repo, err := c.app(ctx)
	if err != nil {
		return err
	}
	res, err := a.Navigate(ctx, repo, stack.NavRequest{Dir: dir, Steps: steps, To: o.to})
	if err != nil {
		return err
	}
	rep := c.report()
	if res.Moved == 0 {
		rep.Warn("%s", res.Message)
		return nil
	}
	rep.Success("Checked out %s", rep.Branch(res.Target))
	return nil
}
