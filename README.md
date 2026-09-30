# git-stack

Graphite like commands for GitHub native stacked PR's.

It's a CLI for stacked branches, installed as `git-stack` so git picks it up as
`git stack <cmd>`. It mirrors the `gt` workflow (`create`, `modify`, `restack`, `submit`,
`sync`, `up`/`down`/`top`/`bottom`, `checkout`) but sits on top of GitHub's official
[`gh stack`](https://github.com/github/gh-stack) extension rather than a third party service,
i.e. the metadata lives in your repo and the PRs are plain GitHub PRs. The same binary also
runs as a stdio MCP server so coding agents can drive the stack the same way you do.

**Status:** under construction. The design, what we learnt about gh stack and the reasoning
behind the decisions are in [`docs/architecture.md`](docs/architecture.md).

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

## Shell completion

`git stack install` sets up completion for your shell, and that includes `git stack <TAB>`
and the aliases (`git co <TAB>`), which is the bit that normally doesn't work with git
subcommands. Details in [`docs/completion.md`](docs/completion.md).

## Agents

`git stack install` also registers `git stack mcp` with Claude Code and Codex at user scope,
and can drop in a Claude Code skill that explains the stacked workflow so the agent actually
uses it. See [`docs/mcp.md`](docs/mcp.md).

## Development

There's no task runner on purpose; this is the whole check:

```sh
go build ./... && go vet ./... && golangci-lint run && go test ./...
```

The rules for contributors (human or agent) are in [`AGENTS.md`](AGENTS.md), and the
architecture notes are in [`docs/architecture.md`](docs/architecture.md).

## License

MIT, see [`LICENSE`](LICENSE).
