package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newCompletionCmd(_ *cli) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <bash|zsh|fish>",
		Short: "Print the shell completion script",
		Long: `Print the completion script for a shell.

The script completes git-stack itself. "git stack install --completion" additionally
installs the git-integration hooks so that "git stack <TAB>" and aliases such as
"git co <TAB>" complete too.

  bash:  git stack completion bash > ~/.local/share/bash-completion/completions/git-stack
  zsh:   git stack completion zsh  > ~/.zfunc/_git-stack   # with ~/.zfunc on $fpath
  fish:  git stack completion fish > ~/.config/fish/completions/git-stack.fish`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeShells,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			out := cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(out, true)
			case "zsh":
				return root.GenZshCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			default:
				return fmt.Errorf("unsupported shell %q (expected bash, zsh or fish)", args[0])
			}
		},
	}
	return cmd
}
