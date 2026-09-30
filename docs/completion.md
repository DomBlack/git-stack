# Shell completion

`git-stack` completes every command, positional argument and flag value through cobra's
`__complete` engine, and the same engine serves `git stack <TAB>` and the git aliases
(`git co <TAB>`, `git ss --<TAB>`, …) in bash, zsh and fish.

`git stack install --completion` writes the script for your shell (from `$SHELL`, or
`--shell bash|zsh|fish`, repeatable). `git stack completion <shell>` prints the same script.

| Shell | File | Notes |
|---|---|---|
| fish | `~/.config/fish/completions/git-stack.fish` | picked up automatically |
| bash | `~/.local/share/bash-completion/completions/git-stack` | bash-completion 2 loads it on first use of `git stack …`; without bash-completion, `source` it from `~/.bashrc` |
| zsh | `~/.zfunc/_git-stack` | add `fpath=(~/.zfunc $fpath)` before `compinit` in `~/.zshrc` |

`XDG_CONFIG_HOME` / `XDG_DATA_HOME` are honoured.

## Rules the code enforces

- Every command sets `ValidArgsFunction`; every non-bool flag registers a completion
  function; the default directive is `NoFileComp`. `cmd/complete_test.go` walks the whole
  command tree and fails otherwise.
- Completion never prompts, never touches the network and only runs `git`: it reads the
  stack from gh-stack's metadata file and PR state from the local cache.
- Descriptions carry context: `feat/api  #123 · open · stack: feat/api`, `1  feat/b`.

## How the git integration works

**fish.** The embedded git completion registers, for every `git-<name>` binary on `PATH`,
`complete -c git -n '__fish_git_using_command <name>' -a '(__fish_git_complete_custom_command <name>)'`,
which re-enters completion as `complete -C "git-<name> <args>"`. Aliases are resolved to
their first word (so `git ss` counts as `stack`) but the tail (`submit --stack`) is dropped.
fish leaves the typed subcommand in `$__fish_git_cmd` and the command line in
`$__fish_git_cmdline`; the hook prepended to cobra's script (`__git_stack_git_args`)
re-reads the alias with `git config --get alias.<x>` and re-inserts the tail, then clears
those variables so a later direct `git-stack <TAB>` is not mistaken for the alias.

**bash.** `git-completion.bash` calls `_git_<subcommand>` for external subcommands; for an
alias it resolves the first git word and calls `_git_stack` with `words[1]` rewritten, but the
alias name is still in `COMP_WORDS[__git_cmd_idx]`. The hook appended to cobra's script
(`_git_stack`) re-resolves the alias, rebuilds `COMP_WORDS`/`COMP_LINE` as
`git-stack <tail> <rest> <cur>`, turns off the file fallback and calls cobra's
`__start_git-stack`. bash-completion's loader finds the file because it is named after the
`git-stack` command.

**zsh.** zsh's bundled `_git` expands aliases into `$words` before dispatch, and completes
external subcommands through an autoloaded `_git-<name>` function on `$fpath` (listed with the
`#description` line). The installed `_git-stack` renames cobra's function, points
`words[1]` at `git-stack` and calls it. Because zsh runs the whole autoloaded file as the
first call, the file ends by invoking `_git-stack` itself when `$funcstack[1]` says so;
without that the first Tab after a fresh shell does nothing. git's own `git-completion.zsh`
wrapper (bash-style dispatch) is served by a `_git_stack` function in the same file.

## Tests

- `go test ./cmd/` covers the cobra engine (`__complete` output, tree-walk enforcement,
  branch/steps descriptions).
- `go test -tags shellintegration ./pkg/shell/` builds the binary, installs the aliases and
  scripts into a temporary `HOME`, and completes real command lines:
  - fish: `fish -c 'complete -C "git ss --"'`
  - bash: sources `git-completion.bash` and the installed file, sets `COMP_WORDS`, runs
    `__git_wrap__git_main`
  - zsh: drives an interactive zsh (`zsh -f -i`, the bundled `_git`) through `zsh/zpty`,
    pressing Tab for real, including the first Tab after autoload

## Manual checklist

After `git stack install`, in a repository with a stack:

1. `git stack <TAB>` lists the commands with descriptions; no file names appear.
2. `git co <TAB>` lists stacked branches first, then trunk, then others, each with `#PR ·
   state · stack:` context. `git co fe<TAB>` narrows.
3. `git ss --<TAB>` lists submit's flags; `git c <TAB>` offers nothing (and no files).
4. `git u <TAB>` lists `1..N` with the destination branch; `git d <TAB>` ends at the trunk.
5. `git stack install --shell <TAB>` lists `bash zsh fish`.
6. zsh only: `git-stack <TAB>` works after adding `~/.zfunc` to `fpath` and restarting.
