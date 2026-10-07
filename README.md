# git-stack

> Git commands for GitHub's native stacked PRs; restack, sync and merge whole stacks.

A stacked PR is a chain of small PRs where each one is based on the one below it, so a big change
can be reviewed and merged in pieces. GitHub added native support for them in
[July 2026](https://github.blog/changelog/2026-07-30-stacked-pull-requests-are-now-in-public-preview/)
(still in public preview), and [`gh stack`](https://github.com/github/gh-stack) is their CLI for it.

git-stack sits on top of `gh stack`. It restacks without checking branches out (unless it hits a
conflict), can `continue` or `abort` across the whole stack, syncs and prunes merged branches, and
merges a whole stack in one go. It also installs short git aliases like `git up`, `git down` and
`git ss` for all of it.

Both tools read and write the same metadata, so you can mix `gh stack` and `git stack` commands in
the same repo. The one difference to know about is sync; `gh stack sync` pushes and only covers the
stack you're on, while `git stack sync` covers every stack and never pushes. Your reviewers don't
need either installed; they just see a stack on GitHub.

```
❯ git stack
● billing-webhook-retries
│  #418 Retry failed billing webhooks with backoff
│  open · needs push · 2h ago
│
│  • 7ac32ef - Give up on a webhook after five attempts
│  • 4f21abc - Retry failed billing webhooks with backoff
│
○ billing-webhook-schema
│  #412 Version the billing webhook payload
│  open · 1d ago
│
│  • 9b1d2e0 - Version the billing webhook payload
│
│ ○ fix-login-timeout
│ │  #399 Stop the login form timing out on slow networks
│ │  draft · needs restack · 3d ago
│ │
│ │  • 3c4d5e6 - Raise the login request timeout to 30s
│ │
├─┘
■ main  20m ago
```

There's no setup per repo; run `git create` on main and you've started a stack. With the aliases
installed, a typical day looks like this;

```sh
git sync                    # fetch, fast forward main and restack everything that rebases cleanly
                            # (alias for git stack sync)
git co main                 # start from trunk; without a branch you get an interactive picker
                            # (alias for git stack checkout)
# ...hack on the first change...
git create -a -m "Add retries to the billing webhook"
                            # stage everything, create a branch on top and commit
                            # (alias for git stack create)
# ...hack on the next change, which depends on the first...
git create -a -m "Retry on 5xx from Stripe"
                            # second branch, stacked on the first
git down                    # go back down one branch (alias for git stack down)
# ...fix something the reviewer spotted on the first PR...
git modify -a               # stage everything, amend that branch's commit and restack everything above
                            # (alias for git stack modify)
git top                     # jump to the top of the stack (alias for git stack top)
git ss                      # push the whole stack and create or update the PRs
                            # (alias for git stack submit)
git stack                   # see every stack in the repo with its PRs (runs git stack log)
```

When an older stack needs updating and main has moved underneath it with conflicts;

```sh
git co my-feature    # check out the stack with the conflict
git sync             # fetches main and restacks what it can; this stack conflicts so it's skipped
git restack          # restack this stack onto main, stopping at the first conflict
# ...fix the conflicts on the first PR of the stack...
git add .
git continue         # carry on with the restack
                     # (or `git abort` to stop and put every branch back where it was)
# ...fix the conflicts on the third PR of the stack...
git continue -a      # -a stages everything for you
git ss               # push the fixed branches
```

## Why not just use...

**`gh stack` on its own?** You can, and git-stack doesn't replace it; it uses the same metadata,
so you can switch between the two whenever you like. git-stack is the day to day workflow I
wanted on top of it; restacks that don't check branches out unless there's a conflict, a `sync`
that covers every stack rather than just the one you're on and never pushes, a submit that won't
push over someone else's commits, a tree of every stack showing what needs restacking or
pushing, a picker, and short aliases for all of it.

**Graphite?** Graphite inspired this, but it's a separate service that tracks stacks itself.
This uses GitHub's own stacks instead.

**spr, ghstack, git-branchless or Sapling?** They predate GitHub's native stacks, so they each
track stacks their own way. If one already works for you there's no reason to switch; this is
for if you want to use the stacks GitHub now has built in.

**`git rebase --update-refs`?** That handles the local rebase, but not creating, chaining or
merging the PRs on GitHub.

## What it doesn't do

gh stack can only add branches at the top of a stack, so neither can git-stack. For anything that
reshapes a stack, use gh stack directly and then `git restack`;

- inserting, reordering or removing branches in the middle of a stack: `gh stack modify`
- turning branches you already have into a stack: `gh stack init <branch> <branch>...`

## Install

You'll need:

- git 2.40+
- `gh` (the [GitHub CLI](https://cli.github.com/)) logged in via `gh auth login`, plus the stack
  extension; `gh extension install github/gh-stack`

Then grab the archive for your system from the
[releases page](https://github.com/DomBlack/git-stack/releases). Builds are for Linux and macOS
(amd64 and arm64); there's no Windows build yet.

```sh
tar xzf git-stack_*_darwin_arm64.tar.gz      # or whichever one you downloaded
xattr -d com.apple.quarantine git-stack      # macOS only; the binary isn't signed yet
mkdir -p ~/.local/bin && mv git-stack ~/.local/bin/   # anywhere on your $PATH works
git stack install --dry-run                  # see exactly what it'll change first, if you want
git stack install
```

Or if you have Go 1.27+, `go install github.com/DomBlack/git-stack@latest` and then
`git stack install`. Make sure `$(go env GOPATH)/bin` is on your `$PATH`, otherwise git won't
find `git stack`.

### What `git stack install` changes

By default it sets up the git aliases and shell completion, and **registers an MCP server with
Claude Code and Codex if you have them installed**. Each of those is on by default and
`--aliases=false`, `--completion=false` or `--agents=false` skips it. `--dry-run` prints every
change before it happens, and `--uninstall` removes what it recorded installing and nothing else
(that record lives in `git config --global stack.managed*`).

- **Aliases** (`--aliases`): `git create|modify|restack|continue|abort|submit|sync|up|down|top|bottom`
  and the short ones `git c|m|rs|ss|u|d|t|b|co`, written with `git config --global`. Anything that
  clashes with a git builtin or an installed command is skipped, and an alias you already have is
  only replaced if you pass `--force` or say yes when asked.
- **Completion** (`--completion`, `--shell bash|zsh|fish`, repeatable, defaults to your `$SHELL`):
  the cobra completion script plus the hooks that make `git stack <TAB>` and the aliases complete. See
  [`docs/completion.md`](docs/completion.md).
- **Agents** (`--agents`): registers `git stack mcp` with
  Claude Code (`claude mcp add -s user`) and Codex (`codex mcp add`) using the absolute path of the
  binary. If neither is installed this does nothing. See [`docs/mcp.md`](docs/mcp.md).
- **Skill** (`--skill`, off unless you pass it or say yes when asked): a Claude Code skill at
  `~/.claude/skills/git-stack/SKILL.md` describing the stacked workflow.

### Updating

`git stack update` fetches the latest GitHub release, checks the archive against the release's
`checksums.txt` and swaps the binary in place. `--check` just tells you whether there's something
newer.

If you built from source, `git stack version` says `dev` along with the commit, and `update` leaves
you alone unless you pass `--force`; i.e. we assume you built it that way on purpose.

Commands also check for a newer release in the background, at most once a day. It's a single
request to the GitHub releases API and the answer is cached in your OS cache directory
(`~/Library/Caches/git-stack` on macOS, `~/.cache/git-stack` on Linux). If you're behind you get a
one line note after the command's own output. Nothing ever waits on it, it only runs on a terminal
(never for scripts, agents or the MCP server) and `GIT_STACK_NO_UPDATE_CHECK=1` turns it off.

### In the terminal

PR numbers (`#418`) and PR links are clickable wherever git-stack prints them to a terminal; the
log, the checkout picker, submit and merge results, notices, errors and the lines relayed from gh
stack. They're underlined OSC 8 hyperlinks, which Ghostty, iTerm2, WezTerm, kitty and most other modern
terminals understand. Piped output never has them.

Under tmux they only work once tmux knows your terminal can do hyperlinks, otherwise it quietly
strips them. Add the `hyperlinks` feature for your terminal, e.g. for Ghostty;

```tmux
set -as terminal-features ',xterm-ghostty:hyperlinks'
```

then restart the tmux server (`tmux kill-server`) and check `tmux display -p '#{client_termfeatures}'`
lists `hyperlinks`.

While something slow is running (a restack, a sync, a submit) the window or tab title says what,
e.g. `git stack: Restacking 3 branches`, and it's put back when the command ends, including on an
error or Ctrl-C. Under tmux that sets the pane title; the outer window only follows if you have
`set -g set-titles on`.

It also reports its status with the [program status protocol](https://www.superlogical.com/rex/docs/build/program-status)
(OSC 7501), so a terminal that supports it can show which tabs are busy, which are waiting on a
question from git-stack, and how each finished (done, or an error such as a restack stopping on a
conflict). Terminals that don't know it ignore it. Under tmux it's sent through tmux's passthrough,
which needs `set -g allow-passthrough on`.

Set `GIT_STACK_NO_TERMINAL_STATUS=1` to turn off both the title and the status reports.

## Agents and AI (optional)

None of this is needed to use git-stack, but if you use coding agents;

- `git stack mcp` is an MCP server exposing the same operations as the CLI, so Claude Code or Codex
  can create, restack and submit stacks too. `install` registers it for you.
- If Claude Code (`claude`) is on your `PATH`, the `--ai` flag on `create` and `submit` will have it
  write the branch name and commit message, or the PR titles and descriptions, from the diff. It's off
  unless you ask for it (or set `stack.ai.auto`).

## Commands

These are the commands you'll use day to day, with their common flags. `git stack help` has
everything else, and `git stack <command> --help` has every flag.

### `git stack create [branch name]`

> Aliases: `git create`, `git c`

Creates a new branch on top of the current one and commits whatever is staged. It needs a name,
`-m` or `--ai`; without a name the branch name comes from the commit message (or from `--ai`). If
nothing is staged you get an empty branch.

It works from main (starting a new stack) or from the top of a stack. If you're further down, run
`git top` first.

Flags:
- `-a` / `--all` stage all changes, including untracked files, before committing
- `-u` / `--update` stage changes to tracked files only
- `-p` / `--patch` pick which hunks to stage
- `-m "msg"` / `--message "msg"` write the commit message inline (repeat it for more paragraphs)
- `--ai` have Claude come up with the branch name and commit message from the diff
  (`git config stack.ai.auto true` makes this the default)
- `--no-ai` don't use AI, even if `stack.ai.auto` is set
- `--no-verify` skip git hooks when committing

### `git stack modify`

> Aliases: `git modify`, `git m`

Amends the current branch's commit (or adds a new one with `-c`), then restacks every branch above
it. If the branch has no commits of its own yet, a new commit is created so the parent's commit is
never rewritten.

Flags:
- `-a` / `--all` stage all changes, including untracked files, first
- `-u` / `--update` stage changes to tracked files only
- `-p` / `--patch` pick which hunks to stage
- `-m "msg"` / `--message "msg"` replace the commit message inline
- `-c` / `--commit` create a new commit instead of amending
- `-e` / `--edit` open an editor to change the commit message when amending
- `--no-edit` keep the commit message as it is (the default when amending without `-m`)
- `--reset-author` set the author to you when amending
- `--no-verify` skip git hooks when committing

### `git stack submit`

> Aliases: `git submit`, `git ss`

Pushes every branch in the stack and creates or updates a PR for each one, each based on the branch
below it. New PRs are ready for review by default.

Unless you pass `-n` / `--no-edit`, `--ai` or `--no-interactive`, gh stack opens its editor for the
title and body of each new PR. `--draft`, `--publish`, `--ai` and the editor only apply to new PRs.

gh stack does the push, with `--force-with-lease`, so after a restack your branches are force
pushed. If a branch has commits on the remote that aren't in your local one (someone else pushed
to it), submit refuses rather than overwrite them. `git sync` pulls them in when the remote is
simply ahead; otherwise merge or cherry-pick them by hand, or pass `--force` if you really do want
them gone.

Flags:
- `-d` / `--draft` create new PRs as drafts
- `-p` / `--publish` create new PRs ready for review (the default; `git config stack.submit.default`
  takes `publish`, `draft` or `ask` if you want to change it)
- `-e` / `--edit` open the editor for new PRs (already the default on a terminal)
- `-n` / `--no-edit` don't open the editor; titles come from the commits
- `--ai` have Claude write the titles and descriptions for new PRs
- `--no-ai` don't use AI, even if `stack.ai.auto` is set
- `-f` / `--force` push even if it overwrites commits someone else pushed
- `--dry-run` show what would be submitted without pushing anything

### `git stack up` / `git stack down` / `git stack top` / `git stack bottom`

> Aliases: `git up` / `git u`, `git down` / `git d`, `git top` / `git t`, `git bottom` / `git b`

Moves you around the stack. The bottom is the branch sitting on trunk and the top is the tip of the
stack. `down` from the bottom branch checks out trunk.

Flags:
- `[steps]` / `-n` / `--steps` on `up` and `down`, move more than one branch (`git up 2`)
- `--to <branch>` when a branch has more than one child, which way to go (`top` and `bottom` take
  it too, for picking a stack when you're on a trunk with several)

### `git stack checkout [branch]`

> Alias: `git co`

Switches to a branch. With no branch it opens an interactive picker showing every stack as a tree
from its trunk.

Flags:
- `-a` / `--all` show every trunk and untracked branches in the picker
- `-u` / `--show-untracked` include branches that aren't in any stack
- `-s` / `--stack` only show the current stack
- `-t` / `--trunk` check out the trunk of the current stack

### `git stack sync`

> Alias: `git sync`

Brings every stack up to date with the remote, all without checking anything out;

1. fetches and fast forwards each trunk
2. deletes branches whose PRs have been merged or closed, as long as every commit on your local
   branch made it onto the PR (anything newer is kept and you're told about it). Set
   `git config stack.sync.prune` to `ask` to be asked first, or `never` to keep them all
3. fast forwards any branch that moved on the remote
4. restacks every stack onto its updated parent

Branches above a deleted one are restacked onto whatever was below it. Squash merges are fine;
only the commits a branch added on top of its parent get replayed, so the squashed commits don't
come back. If a branch was deleted by mistake, the output says which commit it was at (`was
2f1c0a9`), so `git branch <name> 2f1c0a9` brings it back.

Nothing gets pushed; that's what `git ss` is for. If your local trunk has diverged from the remote,
it's only reset with `-f` or a yes at the prompt.

Stacks are per worktree (that's how gh stack stores them), but sync covers all of them. A branch
checked out in another worktree is fast forwarded there too, and like `git pull` any uncommitted
changes it doesn't touch come along. If a change or an untracked file is in the way, the branch
is left where it was and sync ends on `⚠ Synced, but main was not updated`, naming the files
and the checkout, with the `git -C … stash` to run. If another git
process is holding that worktree's index lock (an IDE, a prompt or status bar running
`git status`), sync waits up to about two seconds for it; if the lock is still there it leaves
the branch, finishes everything else and exits non-zero saying which lock file was held. A
lock on the branch itself (`.git/refs/heads/main.lock`, which can happen whether or not it's
checked out) is reported straight away with its path rather than waited on, because by then
git may already have updated the checkout. It never deletes a lock itself.

Flags:
- `-d` / `--delete-all` delete merged or closed branches without asking
- `-f` / `--force` like `-d`, and also reset a diverged trunk to the remote
- `--no-restack` skip the restack

### `git stack restack`

> Aliases: `git restack`, `git rs`

Rebases each branch in the stack onto its parent where needed, without checking anything out. It
doesn't fetch, so the bottom branch goes onto your local trunk (run `git sync` first if you want the
latest main).

If a branch conflicts it stops there, and that's the one time it checks a branch out; it starts a
real `git rebase` on it so you can fix things, which needs a clean working tree. Fix the conflicts,
`git add` the files and run `git continue`, or `git abort` to put every branch it already moved back
where it was.

Flags:
- `-d` / `--downstack` only restack this branch and the ones below it
- `-o` / `--only` only restack this branch
- `-u` / `--upstack` only restack this branch and the ones above it
- `--branch <name>` work the scope out from another branch instead of the current one

### `git stack continue`

> Aliases: `git continue`, `git stack cont`

Once you've fixed and `git add`ed the conflicts, finishes the rebase a restack or modify stopped on
and then restacks whatever was left above it.

Flags:
- `-a` / `--all` stage every change first

### `git stack abort`

> Alias: `git abort`

Gives up on the rebase a restack or modify stopped on and puts back every branch the operation had
already moved, metadata included.

### `git stack log`

Bare `git stack` runs this.

Shows every stack in the repo as a tree (like the example at the top of this page); trunk at the
bottom, each stack rising out of it and the current branch marked. Under each branch you get;

- its PR number and title (`no PR` if it hasn't got one yet), with the title cut short to fit the
  terminal
- the PR's state, whether the branch needs a restack or a push (i.e. you've changed it since it was
  last pushed, so the PR is behind until the next `git ss`), its age and the worktree it's checked
  out in, if that isn't this one
- the branch's own commits, newest first; the ones that aren't on the branch below it (trunk for
  the bottom branch). After ten it just says how many more there are.

PR state and titles come from a local cache that's refreshed when it goes stale, and the commits
come from local git, so it stays quick.

### `git stack merge [branch]`

There's no `git merge` alias for this one, for obvious reasons.

Merges the PRs in the current stack, up to and including a branch (the current one by default),
into trunk as one all or nothing operation on GitHub; if any of them can't be merged, none are. It
then runs a sync, so the merged branches get cleaned up and anything left above them is restacked
onto the new trunk. Nothing is pushed after that, so run `git ss` to update the PRs that are left.

Every PR being merged has to exist and be ready for review; a draft or closed one stops the merge
before anything happens. GitHub's own rules still apply (required checks, reviews, merge queues).
It uses the repo's default merge method unless you pass one, or set `git config stack.merge.method`.

```
❯ git stack merge
🔀 Merging 2 pull requests into main…
✔ Merged 2 pull requests into main at 99953c3
  billing-webhook-schema  #412
  billing-webhook-retries  #418
  main fast forwarded to 99953c3
  deleted billing-webhook-schema (merged, was 2f1c0a9)
  deleted billing-webhook-retries (merged, was 8d41e6b)
✔ Synced: 2 branches deleted
```

Flags:
- `--no-sync` don't sync afterwards
- `--merge` merge with a merge commit
- `--rebase` rebase the commits onto trunk as they are
- `--squash` squash each PR into one commit

## Configuration

Everything is plain `git config`, so set it globally or per repo as you like.

| `git config` key | Default | Meaning |
|---|---|---|
| `stack.branchPrefix` | (none) | prefix for generated branch names, e.g. `dom/` (a bare `dom` gets the `/` added) |
| `stack.submit.default` | `publish` | `publish`, `draft` or `ask` (prompt on a terminal) for new PRs |
| `stack.sync.prune` | `always` | what `sync` does with branches whose PRs merged or closed; `ask` on a terminal, or `never` |
| `stack.merge.method` | (repo default) | `merge`, `squash` or `rebase` for `git stack merge` |
| `stack.cacheTTL` | `5m` | how long PR state is cached for |
| `stack.ai.auto` | `false` | `true` makes `create` and `submit` behave as if `--ai` was passed; `--no-ai` still wins |
| `stack.ai.command` | `claude` | the Claude Code binary used for `--ai` |
| `stack.ai.model` | `haiku` | model alias passed to `--model` |
| `stack.ai.extraPrompt` | (none) | house style instructions added to the prompts |
| `stack.ai.timeout` | `60s` | timeout per AI call |

## Development

There's no task runner on purpose; this is the whole check:

```sh
test -z "$(gofmt -l .)" && go build ./... && go vet ./... && golangci-lint run && go test ./...
```

The rules for contributors (human or agent) are in [`AGENTS.md`](AGENTS.md), the architecture
notes and what we learnt about how gh stack works are in [`docs/architecture.md`](docs/architecture.md),
and how the CLI should look and talk is in [`docs/style.md`](docs/style.md).

### Releasing

Tag and push, that's it;

```sh
git tag v0.1.0
git push origin v0.1.0
```

The release workflow runs goreleaser, which builds Linux and macOS on amd64 and arm64, stamps the
version in, and publishes the archives plus `checksums.txt` to a GitHub release. `git stack update`
and `go install github.com/DomBlack/git-stack@latest` both pick it up.

## License

MIT, see [`LICENSE`](LICENSE).
