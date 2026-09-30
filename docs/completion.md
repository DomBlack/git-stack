# Shell completion

`git-stack` ships cobra completions for bash, zsh and fish (`git stack completion <shell>`),
plus git integration hooks so that `git stack <TAB>` and aliases such as `git co <TAB>` and
`git ss <TAB>` complete through the same engine (`git-stack __complete ...`).

How each shell dispatches external git subcommands and aliases, the install locations and
the manual checklist get filled in once the install command lands. The design side (why completion only
ever runs `git`) is in [`architecture.md`](architecture.md).
