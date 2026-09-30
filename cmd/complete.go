package cmd

import (
	"strings"

	"github.com/spf13/cobra"
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
