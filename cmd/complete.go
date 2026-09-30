package cmd

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Shared completion helpers. Every completion function must be fast, must
// not prompt, must not touch the network and must only run git.

// completeNothing offers no candidates and suppresses file completion.
func completeNothing(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// completeDirs offers directories (used by --cwd).
func completeDirs(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// filterPrefix keeps the candidates starting with toComplete.
func filterPrefix(items []cobra.Completion, toComplete string) []cobra.Completion {
	var out []cobra.Completion
	for _, it := range items {
		value, _, _ := strings.Cut(it, "\t")
		if strings.HasPrefix(value, toComplete) {
			out = append(out, it)
		}
	}
	return out
}

// completeFixed offers a fixed list for the first positional argument only.
func completeFixed(items ...cobra.Completion) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return filterPrefix(items, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

var shellCompletions = []cobra.Completion{
	cobra.CompletionWithDesc("bash", "Bash (bash-completion 2)"),
	cobra.CompletionWithDesc("zsh", "Zsh"),
	cobra.CompletionWithDesc("fish", "Fish"),
}

var completeShells = completeFixed(shellCompletions...)

// completionView loads the stack tree cheaply for completions.
func (c *cli) completionView(cmd *cobra.Command) (*app.View, bool) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	a, repo, err := c.completionApp(ctx)
	if err != nil {
		return nil, false
	}
	v, err := a.View(ctx, repo, app.ViewOptions{IncludeUntracked: true, SkipRestackCheck: true, PRs: app.PRsCached})
	if err != nil {
		return nil, false
	}
	return v, true
}

// branchDescription renders "#123 · open · stack: feat/api"-style context.
func branchDescription(v *app.View, r app.Row) string {
	var parts []string
	if r.PR != nil {
		p := fmt.Sprintf("#%d", r.PR.Number)
		if r.PR.State != "" {
			p += " · " + string(r.PR.State)
		}
		parts = append(parts, p)
	}
	switch {
	case r.IsTrunk:
		parts = append(parts, "trunk")
	case r.Tracked:
		if s, _, ok := v.Graph.StackOf(r.Name); ok {
			parts = append(parts, "stack: "+s.Bottom())
		}
	default:
		parts = append(parts, "untracked")
	}
	if r.Worktree != "" {
		parts = append(parts, "worktree: "+r.Worktree)
	}
	if r.IsCurrent {
		parts = append(parts, "current")
	}
	return strings.Join(parts, " · ")
}

// completeBranches offers every local branch, stacked branches first.
func (c *cli) completeBranches(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	v, ok := c.completionView(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var stacked, trunks, untracked []cobra.Completion
	for _, r := range v.Rows {
		item := cobra.CompletionWithDesc(r.Name, branchDescription(v, r))
		switch {
		case r.IsTrunk:
			trunks = append(trunks, item)
		case r.Tracked:
			stacked = append(stacked, item)
		default:
			untracked = append(untracked, item)
		}
	}
	items := slices.Concat(stacked, trunks, untracked)
	return filterPrefix(items, toComplete), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}

// completeUpstackBranches offers the branches above the current one.
func (c *cli) completeUpstackBranches(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	v, ok := c.completionView(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var items []cobra.Completion
	if v.Graph.IsTrunk(v.Current) {
		for _, s := range v.Graph.StacksOn(v.Current) {
			for _, name := range s.Names() {
				if r, ok := v.Row(name); ok {
					items = append(items, cobra.CompletionWithDesc(name, branchDescription(v, r)))
				}
			}
		}
	} else if s, i, ok := v.Graph.StackOf(v.Current); ok {
		for _, name := range s.Names()[i+1:] {
			if r, ok := v.Row(name); ok {
				items = append(items, cobra.CompletionWithDesc(name, branchDescription(v, r)))
			}
		}
	}
	return filterPrefix(items, toComplete), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}

// maxSteps bounds the step suggestions.
const maxSteps = 50

// stepCompletions lists 1..N where N is how far the stack extends in dir,
// each described by the branch it lands on.
func (c *cli) stepCompletions(cmd *cobra.Command, dir stack.Direction, toComplete string) []cobra.Completion {
	v, ok := c.completionView(cmd)
	if !ok || v.Current == "" {
		return nil
	}
	var items []cobra.Completion
	for n := 1; n <= maxSteps; n++ {
		res, err := stack.Navigate(v.Graph, stack.NavRequest{From: v.Current, Dir: dir, Steps: n})
		if err != nil || res.Moved < n {
			break
		}
		items = append(items, cobra.CompletionWithDesc(strconv.Itoa(n), res.Target))
	}
	return filterPrefix(items, toComplete)
}

func (c *cli) completeSteps(dir stack.Direction) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return c.stepCompletions(cmd, dir, toComplete), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
	}
}

func (c *cli) completeStepsFlag(dir stack.Direction) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return c.stepCompletions(cmd, dir, toComplete), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
	}
}
