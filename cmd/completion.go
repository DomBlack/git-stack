package cmd

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/shell"
)

func newCompletionCmd(_ *cli) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <bash|zsh|fish>",
		Short: "Print the shell completion script",
		Long: `Print the completion script for a shell, including the git-integration hooks
that make "git stack <TAB>" and aliases such as "git co <TAB>" complete.
"git stack install --completion" writes it to the right place for you.

  bash:  git stack completion bash > ~/.local/share/bash-completion/completions/git-stack
  zsh:   git stack completion zsh  > ~/.zfunc/_git-stack   # with ~/.zfunc on $fpath
  fish:  git stack completion fish > ~/.config/fish/completions/git-stack.fish`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeShells,
		RunE: func(cmd *cobra.Command, args []string) error {
			sh, err := shell.Parse(args[0])
			if err != nil {
				return err
			}
			root := cmd.Root()
			var buf bytes.Buffer
			switch sh {
			case shell.Bash:
				err = root.GenBashCompletionV2(&buf, true)
			case shell.Zsh:
				err = root.GenZshCompletion(&buf)
			case shell.Fish:
				err = root.GenFishCompletion(&buf, true)
			}
			if err != nil {
				return err
			}
			script, err := shell.Compose(sh, buf.String())
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), script)
			return err
		},
	}
	return cmd
}
