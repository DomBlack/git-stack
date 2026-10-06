# git-stack

> Graphite like commands for GitHub native stacked PR's.

This CLI installs an extension into git, giving you `git stack create`, `git stack modify`
and `git stack submit` commands, as well as setting up aliases in your `~/.gitconfig` allowing
nice shortcuts like `git up` (to navigate to the next PR up the stack), `git down` etc, all built
on-top of Github's own Stack's and this binary plays nicely along side Github's own CLI for stacks.

Once installed, git stack also exposes itself to your local agents via a local MCP server, giving
your coding agents the ability to create and manage stacks of PR's too.

With the aliases installed a typical stack looks like this;

```sh
git stack                   # view all stacked PR's checked out in the repo (alias for git stack log)
git sync                    # fetch upstream changes and rebase all local PR's which can be rebased
                            # cleanly including main. (alias for git stack sync)
git co main                 # start from trunk, use without main to get an interactive picker
                            # (alias for git stack checkout)
# ...hack on the first change...
git create -a --ai          # stage everything, Claude names the branch and writes the commit
                            # (alias for git stack create)
# ...hack on the next change, which depends on the first...
git create -a               # second branch, stacked on the first - but this time you write the commit
git down                    # go back down one branch (alias for git stack down)
# ...hack some more changes...
git modify -a               # stage everything and update the original branches commit
                            # (alias for git stack modify)
git top                     # jump to the top of the stack  (alias for git stack top)
git ss                      # push the whole stack; new PRs are ready for review (alias for git stack submit)
```

For older PR's you need to update due to conflicts;
```sh
git co my-feature    # Checkout the top of your stack with the conflict
git sync             # attempt to cleanly sync it with main
git restack          # start restacking it on main
# ... fix conflicts on the first PR  of the stack ...
git add .
git continue         # once the conflicts have been fixed, continue the restack 
                     # you can also run `git abort` if you want to stop and undo the restack.
# ... fix conflicts on the third PR of the stack ...
git continue -a      # the -a stages everything
git ss               # push the fixed branches back to your remote
```

## Install

If you have Go installed, the easiest way is;

```sh
go install github.com/DomBlack/git-stack@latest
git stack install # Setup's the git aliases, shell completion and agent MCP registration.
```

If you do not have Go installed, you can download the latest binary for your system from the
[releases page](https://github.com/DomBlack/git-stack/releases). Once downloaded put the binary
into your `$PATH` and then run `git stack install` to have it setup the aliases, shell autocomplete
and MCP setup for your coding harnesses.

### Prerequisites

- git 2.40+
- `gh` (the [GitHub CLI](https://cli.github.com/)) logged in via `gh auth login`, plus the stack extension;
  `gh extension install github/gh-stack`
- Claude Code (`claude`) on your `PATH` if you want the `--ai` flags, which will write commit messages, 
  set branch names and write your PR's for you (optional)

### Updating

`git stack update` fetches the latest GitHub release, checks the archive against the
release's `checksums.txt` and swaps the binary in place. `--check` just tells you whether there's
something newer.

If you built from source, `git stack version` says `dev` along with the commit, and `update`
leaves you alone unless you pass `--force`; i.e. we assume you built it that way on purpose.

Every command also checks for a newer release in the background, at most once a day, and
remembers the answer in the OS cache directory (`~/Library/Caches/git-stack` on macOS,
`~/.cache/git-stack` on Linux). When you're behind, a one line note after the command's own
output says which version is out. Nothing waits for the check; a slow network just means you
hear about it on the next run. It only happens on a terminal (never for scripts, agents or the
MCP server) and `GIT_STACK_NO_UPDATE_CHECK=1` turns it off.


## Commands

This section contains details of the main commands you'll use day to day from this extension, but all
commands in the entire extension are availaible in `git stack help`.

#### `git stack create [branch name]`

> Alias: `git create [branch name]`

Create a new branch stacked on top of the current branch and commit the staged
changes. Without a name the branch name is derived from the commit message (or drafted
by `--ai`). With nothing staged an empty branch is created.

Flags:
- `-a` / `--all`  stage all changes, including untracked files, before committing
- `-p` / `--patch` pick which hunks to stage
- `--ai` uses claude to come up with the branch name and commit message based on the patch
  (`git config stack.ai.auto true` makes this the default)
- `--no-ai` stops AI being used if auto is set to true.
- `-m "msg"` / `--message "msg"` Write the commit message inline

#### `git stack modify`

> Alias: `git modify`

Modify the current branch by amending its commit, or creating a new one with -c, then
restack every branch above it. If the branch has no commits of its own, a new commit is
created so the parent's commit is never rewritten.

Same flags as create, plus:
- `-c` / `--commit` creates a new commit instead of amending
- `-e` / `--edit`  open an editor to edit the commit message when amending
- `--reset-author` set the author to the current user when amending

#### `git stack submit`

> Alias: `git ss`

Push every branch of the current stack and create or update a pull request for
each, chained onto its parent. New PRs are ready for review unless --draft is
given (or git config stack.submit.default says draft, or ask to be asked on a
terminal). Without --no-edit, --ai or --no-interactive, gh stack's editor opens
for new PRs.

Flags:
- `-d` / `--draft` create new PRs as drafts
- `-p` / `--publish` creates new PRs in a ready to review state (the default)
  (`git config stack.submit.default [publish/draft/ask]` to change the default)
- `--ai` draft titles and descriptions for new PRs with Claude Code
- `--no-ai` stops AI being used if set to auto.
- `--dry-run` report what would be submitted without pushing

#### `git stack up` / `git stack down` / `git stack top` / `git stack bottom`

> Aliases: `git up` / `git down` / `git top` / `git bottom`

Allows you to navigate around your stack, where the bottom is the branch created
ontop of your main branch and top is the last branch you created in the stack.
Up/Down navigate one branch at a time.

#### `git stack checkout`

> Alias: `git co`

Switch to a branch. With no branch, opens an interactive picker showing every
stack as a tree rooted at its trunk.

Flags:
- `-a` / `--all` show every trunk and untracked branches in the picker
- `-u` / `--show-untracked` includes branches that are in no stack in the picker
- `-s` / `--stack` only show the current stack in the picker
- `-t` / `--trunk` check out the trunk of the current stack

#### `git stack sync`

> Alias: `git sync`

Sync every stack with the remote; fetch and then fast forward each trunk,
delete branches whose pull requests merged or closed (set `git config stack.sync.prune` to
"ask" to be asked first or "never" to keep them), fast forward branches that moved on the
remote, then restack every stack onto its updated parents without checking anything out.
Nothing is pushed; `git stack submit` does that. A trunk that has diverged from the remote
is only reset with -f or a yes at the prompt.

Flags:
- `-d` / `--delete-all` delete merged or closed branches without asking
- `-f` / `--force` reset a diverged trunk and delete merged or closed branches without asking
- `--no-restack` skip restacking

#### `git stack restack`

> Alias: `git restack`

Make sure each branch in the stack has its parent in its history, rebasing where
needed without checking anything out (no fetch; the bottom branch goes onto the local
trunk). A conflict stops at that branch: resolve it, git add the files, then run
`git stack continue`, or `git stack abort` to put every moved branch back.

Flags:
- `-d` / `--downstack` only restack this branch and its ancestors
- `-o`, `--only` only restack this branch
- `-u` / `--upstack` only restack this branch and its descendants

#### `git stack continue`

> Alias: `git continue`

Finish the rebase a restack or modify stopped on, now that the conflicts are resolved
and git added, then restack whatever was left above it.

Flags:
- `-a` / `--all` stage every change first

#### `git stack abort`

> Alias: `git abort`

Give up the rebase a restack or modify stopped on and put back every branch the
operation had already moved, metadata included.

Flags:
_none_

#### `git stack log`

> Alias: `git stack`

Show the stacks in this repository the way gt log does: trunk at the bottom, each
stack rising from it, the current branch marked, and each branch's pull request,
age and whether it needs a restack. A repository with no stacks yet shows just its
trunk. Pull request state comes from the local cache and is refreshed when stale.

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

Flags:
_none_

#### `git stack merge`

Merge the pull requests of the current stack up to and including a branch (the
current one by default) into trunk, in one all or nothing operation on GitHub:
if any of them can't be merged, none are. Then sync, so the merged branches go
and whatever is left above is restacked onto the new trunk.

Every pull request in the way must exist and be ready for review; a draft or
closed one stops the merge before anything happens. GitHub's own rules (required
checks, reviews, a merge queue, `git config stack.merge.method` makes one the default.).


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

Flags:
- `--no-sync` don't sync afterwards
- `--merge` merge with a merge commit
- `--rebase` rebase the commits onto trunk as they are
- `--squash` squash each pull request into one commit

## `git stack install`

One command sets everything up. `--dry-run` prints every change before it happens, and
`--uninstall` removes exactly what was installed and nothing else (we record what we touched in
`git config --global stack.managed*`, so we never guess).

- **Aliases** (`--aliases`, on by default): `git create|modify|restack|continue|abort|submit|sync|up|down|top|bottom`
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
