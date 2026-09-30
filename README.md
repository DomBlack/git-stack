# git-stack

Graphite like commands for GitHub native stacked PR's.

It's a CLI for stacked branches, installed as `git-stack` so git picks it up as
`git stack <cmd>`. It mirrors the `gt` workflow (`create`, `modify`, `restack`, `submit`,
`sync`, `up`/`down`/`top`/`bottom`, `checkout`) but sits on top of GitHub's official
[`gh stack`](https://github.com/github/gh-stack) extension rather than a third party service,
i.e. the metadata lives in your repo and the PRs are plain GitHub PRs. The same binary also
runs as a stdio MCP server so coding agents can drive the stack the same way you do.

The design, what we learnt about gh stack and the reasoning behind the decisions are in
[`docs/architecture.md`](docs/architecture.md), if you want to know why things are the way
they are.

## Install

```sh
go install github.com/DomBlack/git-stack@latest
git stack install          # git aliases, shell completion, agent MCP registration
```

### Prerequisites

- git 2.40+
- `gh` (the GitHub CLI) logged in via `gh auth login`, plus the extension;
  `gh extension install github/gh-stack`
- Claude Code (`claude`) on your `PATH` if you want the `--ai` flags (optional)

## Commands

If you know `gt` you already know most of this; the table is the mapping.

| Graphite | git-stack | Notes |
|---|---|---|
| `gt create [name]` | `git stack create [name]` (`git c`) | `-a` `-u` `-p` `-m` `--ai` |
| `gt modify` | `git stack modify` (`git m`) | amend (or `-c` for a new commit), then restack everything above |
| `gt restack` | `git stack restack` (`git rs`) | `--upstack` `--downstack` `--continue` `--abort` |
| `gt up/down/top/bottom` | `git stack up/down/top/bottom` (`git u/d/t/b`) | `down` from the bottom branch takes you to trunk |
| `gt checkout` | `git stack checkout` (`git co`) | interactive tree picker |
| `gt submit` / `gt ss` | `git stack submit` (`git ss`) | `-d` `-p` `--no-edit` `--dry-run` `--ai` |
| `gt sync` | `git stack sync` (`git sync`) | `-f` prunes merged branches |
| (none) | `git stack install` | aliases, completion, agent MCP registration |
| (none) | `git stack completion <shell>` | bash, zsh, fish |
| (none) | `git stack mcp` | stdio MCP server |

## Day to day

With the aliases installed a typical stack looks like this;

```sh
git co main                 # start from trunk
# ...hack on the first change...
git create -a --ai          # stage everything, Claude names the branch and writes the commit
# ...hack on the next change, which depends on the first...
git create -a --ai          # second branch, stacked on the first
git ss                      # push the whole stack; new PRs are drafts (or it asks)
```

Review comes back on the first PR;

```sh
git down                    # back to the first branch
# ...fix it up...
git modify -a               # amend, and the branch above is restacked for you
git ss                      # push both again
```

Once the bottom PR merges;

```sh
git sync -f                 # fetch, move trunk, restack what's left, prune the merged branch
git co                      # the picker, if you've lost track of where you are
```

`--ai` is optional everywhere; `git create -a -m "message"` or plain `git create name`
works just as well. `git ss --ai -p` drafts the PR titles and bodies for you and opens
them ready for review rather than as drafts.

## `git stack install`

One command sets everything up. `--dry-run` prints every change before it happens, and
`--uninstall` removes exactly what was installed and nothing else (we record what we touched in
`git config --global stack.managed*`, so we never guess).

- **Aliases** (`--aliases`, on by default): `git create|modify|restack|submit|sync|up|down|top|bottom`
  and the short ones `git c|m|rs|ss|u|d|t|b|co`, written with `git config --global`. Anything
  that clashes with a git builtin or an installed command is skipped, and an alias you already
  have is only replaced if you pass `--force` or say yes when asked.
- **Completion** (`--completion`, `--shell bash|zsh|fish`): the cobra completion script plus the
  hooks that make `git stack <TAB>` and the aliases complete. See
  [`docs/completion.md`](docs/completion.md).
- **Agents** (`--agents`): registers `git stack mcp` with Claude Code (`claude mcp add -s user`)
  and Codex (`codex mcp add`) using the absolute path of the binary. If a CLI isn't installed
  it's skipped. See [`docs/mcp.md`](docs/mcp.md).
- **Skill** (`--skill`, asked interactively if you don't pass it): a Claude Code skill at
  `~/.claude/skills/git-stack/SKILL.md` describing the stacked workflow.

## Configuration

Everything is plain `git config`, so set it globally or per repo as you like.

| `git config` key | Default | Meaning |
|---|---|---|
| `stack.branchPrefix` | (none) | prefix for generated branch names, e.g. `dom/` |
| `stack.ai.command` | `claude` | the Claude Code binary used for `--ai` |
| `stack.ai.model` | `haiku` | model alias passed to `--model` |
| `stack.ai.extraPrompt` | (none) | house style instructions appended to the prompts |
| `stack.ai.timeout` | `60s` | timeout per AI call |
| `stack.cacheTTL` | `5m` | how long pull request state is cached for |
| `stack.submit.default` | `ask` | `draft`, `publish` or `ask` for new PRs |

## Differences from Graphite

It's worth being upfront about this; gh stack is linear and only lets you add branches at the
top, so a handful of `gt` behaviours aren't possible yet: `create --insert`, `restack --only`,
`submit --update-only`, `sync --all` and `modify --into`. Each one prints a single line saying
why rather than silently doing something else.

`submit` also always submits the whole stack (gt submits downstack by default). Pass `--stack`
to acknowledge that's what you want.

## Development

There's no task runner on purpose; this is the whole check:

```sh
go build ./... && go vet ./... && golangci-lint run && go test ./...
```

The rules for contributors (human or agent) are in [`AGENTS.md`](AGENTS.md), and the
architecture notes are in [`docs/architecture.md`](docs/architecture.md).

## License

MIT, see [`LICENSE`](LICENSE).
