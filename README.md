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
| `gt log` | `git stack` or `git stack log` | every stack as a tree, trunk at the bottom, with PR state |
| `gt submit` / `gt ss` | `git stack submit` (`git ss`) | `-d` `-p` `--no-edit` `--dry-run` `--ai` |
| `gt sync` | `git stack sync` (`git sync`) | fetch, fast forward trunk, delete merged and closed branches (`stack.sync.prune`, `-d`, `-f`), fast forward branches the remote moved, restack every stack without a checkout; nothing is pushed |
| `gt merge` | `git stack merge [branch]` | merge the stack's PRs up to a branch into trunk, all or nothing, then sync; `--squash` `--rebase` `--merge` `--no-sync` |
| (none) | `git stack install` | aliases, completion, agent MCP registration |
| (none) | `git stack completion <shell>` | bash, zsh, fish |
| (none) | `git stack mcp` | stdio MCP server |
| (none) | `git stack version` | `--json` for scripts |
| (none) | `git stack update` | `--check` `--force`; plain HTTPS, no login needed |

## Day to day

With the aliases installed a typical stack looks like this;

```sh
git co main                 # start from trunk
# ...hack on the first change...
git create -a --ai          # stage everything, Claude names the branch and writes the commit
# ...hack on the next change, which depends on the first...
git create -a --ai          # second branch, stacked on the first
git ss                      # push the whole stack; new PRs are ready for review
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
git sync                    # fetch, move trunk, delete the merged branch, restack what's left
git stack                   # where am I? every stack as a tree with its PRs
git co                      # the picker, to jump somewhere else
```

`--ai` is optional everywhere; `git create -a -m "message"` or plain `git create name`
works just as well. `git ss --ai -p` drafts the PR titles and bodies for you and opens
them ready for review rather than as drafts. If you always want the drafting, set
`git config --global stack.ai.auto true` and drop the flag; `--no-ai` turns it off for one
run.

### Where am I?

`git stack` on its own (or `git stack log`) shows every stack the way `gt log` does; trunk at
the bottom, newest branch at the top, `●` on the branch you're on, and under each branch its
PR (clickable in terminals that support it), how old it is and whether it needs a restack.
Two stacks on the same trunk sit side by side and join above it. A repo with no stacks yet
just shows its trunk.

```
● billing-webhook-retries
│  #418 open · 2h ago
│
○ billing-webhook-schema
│  #412 open · 1d ago
│
│ ○ fix-login-timeout
│ │  #399 draft · needs restack · 3d ago
├─┘
■ main  20m ago
```

The rest of the output follows the same rules (one headline with a spinner while something
runs, `✔` `✖` `⚠` result lines, plain `ok:`/`note:`/`error:` when piped); that's all written
down in [`docs/style.md`](docs/style.md).

### Landing it

`git stack merge` merges the stack's PRs up to and including the current branch (or the one
you name) into trunk in one go, using GitHub's all or nothing stack merge; if any PR in the
way can't be merged, none are. Plain `gh pr merge` refuses stacked PRs, which is the main
reason this exists. Every PR below has to be ready for review first (`git stack submit
--publish`), and GitHub's own rules still apply. Afterwards it syncs, so the merged branches
go and anything left above is restacked onto the new trunk. `--squash`, `--rebase` and
`--merge` pick how the commits land; `stack.merge.method` makes one the default.

```
❯ git stack merge
🔀 Merging 3 pull requests into main…
✔ Merged 3 pull requests into main at 99953c3
  billing-webhook-schema  #412
  billing-webhook-retries  #418
  billing-webhook-docs  #419
  main fast forwarded to 99953c3
  deleted billing-webhook-schema (merged, was 2f1c0a9)
  ...
✔ Synced: 3 branches deleted
```

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
| `stack.branchPrefix` | (none) | prefix for generated branch names, e.g. `dom/` (a bare `dom` gets the `/` added) |
| `stack.ai.command` | `claude` | the Claude Code binary used for `--ai` |
| `stack.ai.model` | `haiku` | model alias passed to `--model` |
| `stack.ai.extraPrompt` | (none) | house style instructions appended to the prompts |
| `stack.ai.timeout` | `60s` | timeout per AI call |
| `stack.ai.auto` | `false` | `true` makes `create` and `submit` behave as if `--ai` was passed; `--no-ai` still wins |
| `stack.cacheTTL` | `5m` | how long pull request state is cached for |
| `stack.submit.default` | `publish` | `publish`, `draft` or `ask` (prompt on a terminal) for new PRs |
| `stack.sync.prune` | `always` | what `sync` does with branches whose PRs merged or closed; `ask` on a terminal, or `never` |
| `stack.merge.method` | (repo default) | `merge`, `squash` or `rebase` for `git stack merge` |

## Differences from Graphite

It's worth being upfront about this; gh stack is linear and only lets you add branches at the
top, so a handful of `gt` behaviours aren't possible yet: `create --insert`, `restack --only`,
`submit --update-only` and `modify --into`. Each one prints a single line saying
why rather than silently doing something else.

`submit` also always submits the whole stack (gt submits downstack by default). Pass `--stack`
to acknowledge that's what you want.

`sync --all` is not accepted at all: every trunk is synced, so it has nothing to select.

One thing we actively correct; gh stack treats a PR that is queued for merge like a merged one
and bases the next PR on main, so a new PR shows the whole stack's diff. `submit` moves such a
base back onto the real parent and tells you.

## Updating

`git stack update` fetches the latest GitHub release over plain HTTPS (no `gh` login
needed), checks the archive against the release's `checksums.txt` and swaps the binary in
place. `--check` just tells you whether there's something newer.

If you built from source, `git stack version` says `dev` along with the commit, and `update`
leaves you alone unless you pass `--force`; i.e. we assume you built it that way on purpose.

Every command also checks for a newer release in the background, at most once a day, and
remembers the answer in the OS cache directory (`~/Library/Caches/git-stack` on macOS,
`~/.cache/git-stack` on Linux). When you're behind, a one line note after the command's own
output says which version is out. Nothing waits for the check; a slow network just means you
hear about it on the next run. It only happens on a terminal (never for scripts, agents or the
MCP server) and `GIT_STACK_NO_UPDATE_CHECK=1` turns it off.

## Releasing

Tag and push, that's it;

```sh
git tag v0.1.0
git push origin v0.1.0
```

The release workflow runs goreleaser, which builds linux and darwin on amd64 and arm64,
stamps the version in, and publishes the archives plus `checksums.txt` to a GitHub release.
`git stack update` and `go install github.com/DomBlack/git-stack@latest` both pick it up.

## Development

There's no task runner on purpose; this is the whole check:

```sh
go build ./... && go vet ./... && golangci-lint run && go test ./...
```

The rules for contributors (human or agent) are in [`AGENTS.md`](AGENTS.md), the
architecture notes are in [`docs/architecture.md`](docs/architecture.md), and how the CLI
should look and talk is in [`docs/style.md`](docs/style.md).

## License

MIT, see [`LICENSE`](LICENSE).
