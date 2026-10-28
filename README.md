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
git ab -u                   # or let absorb work out which commit each fix belongs to and put it there
                            # (alias for git stack absorb)
git reorder                 # change the order of the stack's branches in a list
                            # (alias for git stack reorder)
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

gh stack can only add branches at the top of a stack, so `git create --insert` does the middle
of a stack itself, the same way a reorder does. Reordering a stack, moving branches onto another
stack, folding a branch into the one below it, dropping one from a stack, adopting branches you made
by hand and letting go of them again are `git stack reorder`, `git stack move`, `git stack fold`,
`git stack pop`, `git stack init` and `git stack untrack`, so nothing needs gh stack directly any
more.

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

- **Aliases** (`--aliases`): `git create|modify|absorb|reorder|move|fold|rename|untrack|track|pop|restack|continue|abort|undo|submit|sync|up|down|top|bottom|pr`
  and the short ones `git c|m|ab|rs|ss|u|d|t|b|co`, written with `git config --global`. Anything that
  clashes with a git builtin or an installed command is skipped, and an alias you already have is
  only replaced if you pass `--force` or say yes when asked.
- **Completion** (`--completion`, `--shell bash|zsh|fish`, repeatable, defaults to your `$SHELL`):
  the cobra completion script plus the hooks that make `git stack <TAB>` and the aliases complete. See
  [`docs/completion.md`](docs/completion.md).
- **Agents** (`--agents`): registers `git stack mcp` with
  Claude Code (`claude mcp add -s user`) and Codex (`codex mcp add`) using the absolute path of the
  binary. If neither is installed this does nothing. See [`docs/mcp.md`](docs/mcp.md).
- **Skill** (`--skill`, off unless you pass it or say yes when asked): a skill describing the
  stacked workflow, at `~/.claude/skills/git-stack/SKILL.md` for Claude Code and
  `~/.codex/skills/git-stack/SKILL.md` for Codex (`$CODEX_HOME` if you set it). Each one is only
  written if you have that agent's config directory.

### Updating

`git stack update` fetches the latest GitHub release, checks the archive against the release's
`checksums.txt` and swaps the binary in place. `--check` just tells you whether there's something
newer.

Either way it lists what you're getting: one line per release between yours and the latest
(so if you skipped a couple you see those too), any breaking changes in them, and a link to the
full release notes. Each version links to its own release page on a terminal.

```
❯ git stack update
✔ Updated git-stack v0.2.3 to v0.3.1 (~/.local/bin/git-stack)
  v0.3.1  git stack update now keeps your install up to date too, and continuing…
  v0.3.0  This release adds absorb, reorder, move and init, so fixing a commit…
  Release notes: https://github.com/DomBlack/git-stack/releases
```

Once the new binary is in place it brings whatever `git stack install` set up before in line with
it, without asking: aliases the new version adds, the completion script, the MCP registration (if
the binary moved) and the Claude Code and Codex skills, each only if you installed it in the first
place. It only prints what it changed, so if nothing did you just get the update line. An alias
you've pointed somewhere else, or a completion file that isn't ours, is left alone.

If you built from source, `git stack version` says `dev` along with the commit, and `update` leaves
you alone unless you pass `--force`; i.e. we assume you built it that way on purpose.

Commands also check for a newer release in the background, at most once a day. It's a single
request to the GitHub releases API and the answer is cached in your OS cache directory
(`~/Library/Caches/git-stack` on macOS, `~/.cache/git-stack` on Linux). If you're behind you get a
one line note after the command's own output, and `git stack update --check` shows what changed
before you take it. Nothing ever waits on it, it only runs on a terminal
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
`git top` first, or pass `--insert` to put the new branch right above the one you're on: the
branches that were above it are restacked onto it, a conflict stops like a restack (`git continue`
or `git abort`, which puts those branches back and keeps the new branch with its commit), and if
the stack already has PRs the next `git ss` creates it on GitHub again in the new order.

```sh
git down 2
git create -i -a -m "Split out the config parsing"
                            # new branch right above this one; the rest are restacked onto it
```

Flags:
- `-a` / `--all` stage all changes, including untracked files, before committing
- `-u` / `--update` stage changes to tracked files only
- `-p` / `--patch` pick which hunks to stage
- `-m "msg"` / `--message "msg"` write the commit message inline (repeat it for more paragraphs)
- `--ai` have Claude come up with the branch name and commit message from the diff
  (`git config stack.ai.auto true` makes this the default)
- `--no-ai` don't use AI, even if `stack.ai.auto` is set
- `--no-verify` skip git hooks when committing
- `-i` / `--insert` put the branch right above the current one, even in the middle of a stack

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

### `git stack absorb`

> Aliases: `git absorb`, `git ab`

Takes whatever is staged and folds each changed run of lines into the commit in this stack that
last changed those lines, e.g. the fix for a review comment three PRs down goes into that PR's
commit without you going there. It looks at the current branch's commits and the branches below
it (never trunk, and never a branch whose PR has merged), rewrites the commit it picks and every
commit above it keeping messages and authors, then restacks the branches above you. It doesn't
push.

It goes by lines, not by meaning, so two things to know. A change to lines no commit in the stack
touched can't be placed and stays staged, as does a new or deleted file; the output says so. And one
run of changed lines that spans lines two different commits changed goes into the newer of the two,
not split between them.

```
❯ git absorb
  pkg/app/sync.go:120-125  into 7ac32ef Give up on a webhook after five attempts (billing-webhook-retries)
  pkg/app/sync.go:300  into 4f21abc Version the billing webhook payload (billing-webhook-schema)
  README.md:12  not absorbed; no commit in this stack changed those lines
? Absorb 2 hunks into 2 commits? [Y/n]
✔ Absorbed 2 hunks into 2 commits
  billing-webhook-schema  rewritten, was 9b1d2e0
  billing-webhook-retries  rewritten, was 7ac32ef
🧱 Restacking 1 branch above billing-webhook-retries…
✔ Restacked 1 branch above billing-webhook-retries
⚠ Still staged: 1 hunk
```

On a terminal you see the plan and get asked first. The old tips are printed. If it picked the wrong
commit, put things back with `git reset --soft <old tip of the branch you're on>` (your changes come
back staged), `git branch -f <branch> <old tip>` for each other branch it rewrote, then
`git restack`. Every branch it needs to rewrite has to be sitting on its parent already (run
`git restack` if not), and the bottom branch of the stack is fine behind main.

Flags:
- `-a` / `--all`, `-u` / `--update`, `-p` / `--patch` stage first, like `modify`; they stage even
  with `--dry-run`
- `-d` / `--dry-run` show where each change would go and change nothing
- `-f` / `--force` don't ask before rewriting

### `git stack reorder [branch...]`

> Alias: `git reorder`

Changes the order of the branches in the stack you're on. With no arguments on a terminal it
shows the stack as a list, top first with trunk at the bottom; `j`/`k` move the cursor, `J`/`K`
(or shift with the arrows) move the branch under it, enter applies and esc cancels. Otherwise pass
the new order as arguments, bottom to top, naming every branch of the stack once.

Nothing is checked out. Each branch keeps its own commits and is replayed onto its new parent, so a
conflict stops just like a restack: fix it, `git add` and `git continue`, or `git abort` to put the
old order back. If a conflict isn't the reason it stopped, `git continue` finishes it and
`git abort` undoes all of it. A branch checked out with uncommitted changes in another worktree
stops the reorder before anything changes. GitHub can't reorder a stack in place, so if the stack
already has PRs the next `git ss` dissolves it on GitHub and creates it again in the new order. The
PRs stay the same, but the stack's number changes.

The list goes away when you press enter, so what's left in your terminal is the result:

```
❯ git reorder
✔ Reordered the stack: billing-webhook-schema, billing-webhook-alerts, billing-webhook-retries
✔ Restacked billing-webhook-alerts, billing-webhook-retries
```

### `git stack move`

> Alias: `git move`

Takes the current branch out of its stack, along with every branch above it, and puts it on top of
another stack (`--onto` that stack's top branch) or starts a new stack with it (`--onto` a trunk,
which can be a different one, e.g. moving a stack from `main` to `develop`). With `--only` the
branch goes on its own; the branches above it stay where they were, now on its old parent and
without its commits. Without `--onto` on a terminal you get the checkout picker, showing only the
places it can go.

gh stack keeps each stack as a single line, so a branch can only land on the top of a stack or on a
trunk; to change the order within a stack use `git reorder`. Nothing is checked out. A conflict
(or any other stop after some branches moved) is finished with `git continue`, which covers the
branches an `--only` left behind, or undone with `git abort`. A branch checked out with uncommitted
changes in another worktree stops the move before anything changes. The next `git ss` dissolves
the stale stack on GitHub, lets gh stack create the one you submit, and then creates the moved
branches' old stack again itself, so it is grouped again by the same submit. The notices name each
stack. What's left to you: a stack with fewer than two PRs has nothing to group and the notice
says so; a branch in it with no open PR, or a failed create, gets a notice saying to submit from
that stack. (Without `gh` set up as the forge, the notice just says to submit from it.)

```
# on billing-webhook-alerts
❯ git move --onto main --only
✔ Moved billing-webhook-alerts from billing-webhook-schema onto main (new stack)
  billing-webhook-retries stays on billing-webhook-schema
✔ Restacked billing-webhook-alerts, billing-webhook-retries

# a separate run, on billing-webhook-retries
❯ git move --onto fix-login-timeout
✔ Moved billing-webhook-retries from billing-webhook-alerts onto fix-login-timeout
✔ Restacked billing-webhook-retries
```

Flags:
- `-o` / `--onto <branch>` where to put it: a trunk, or the top branch of another stack
- `-s` / `--source <branch>` move this branch instead of the current one
- `--only` move just the branch and leave the ones above it behind

### `git stack fold`

> Alias: `git fold`

Merges the current branch into the branch below it, for when two PRs should really have been one.
The parent keeps its name and PR and ends up with both sets of commits; the current branch is
deleted and you're left on the parent, with any uncommitted changes carried along. With `--keep`
it's the other way round: the current branch keeps its name and PR and takes the parent's place in
the stack, and the parent is deleted.

Nothing is rebased, since the branches above already have every commit involved and only their
parent changes, so the branch has to be sitting on its parent first (`git restack` if it isn't).
It won't fold the bottom branch of a stack into trunk, and it stops before anything changes when
the parent is checked out in another worktree. The PR of the branch that went is closed at the next
`git ss`, with a comment pointing at the one it went into, and its branch on GitHub is deleted once
nothing is based on it any more.

```
# on billing-webhook-alerts
❯ git fold
✔ Folded billing-webhook-alerts into billing-webhook-schema
  #418 (billing-webhook-alerts) will be closed at the next submit
```

Flags:
- `--keep` keep this branch's name and pull request instead of the parent's

### `git stack rename <name>`

> Alias: `git rename`

Gives the current branch a new name, for when the work turned out to be about something else. It
keeps its place in the stack, its commits and its PR, and nothing is rebased; the branch above now
sits on the new name. The name is used as you give it, without `stack.branchPrefix`.

If the branch has been pushed it's renamed on GitHub first, which keeps the PR attached and
retargets any PR based on it, and only then here (the upstream follows it). If GitHub won't do it,
because you're offline or don't have permission, nothing changes and the error says so. `--local`
renames just this copy: the PR stays on the old branch name and the next `git ss` opens a new one.
It won't rename a trunk, a branch that isn't in a stack or one whose PR has merged, or take a name
that's already in use here or on GitHub.

```
# on billing-webhook-alerts
❯ git rename billing-webhook-retries
✔ Renamed billing-webhook-alerts to billing-webhook-retries here and on origin
```

Flags:
- `--local` rename only on this machine, leaving the remote branch and its pull request alone

### `git stack init [branch...]`

> Alias: `git track`; `git stack track` works too

Tracks branches you already have, e.g. ones made by hand with plain git, as a new stack. List them
bottom to top; with no arguments it's just the branch you're on. The trunk is `--base` if you
give it, otherwise the repo's only trunk, otherwise its default branch. The alias is `track`
because git won't let an alias replace its own `git init`.

It only records the stack. Nothing is committed, rebased or checked out, so a branch that isn't
sitting on the one below it is pointed out and `git restack` puts it there. New branches are still
made with `git create`; this only adopts ones that exist.

```
❯ git track search-index search-api search-ui
✔ Tracking search-index, search-api, search-ui on main
⚠ search-api is not on search-index; run git stack restack
❯ git restack
  search-index already in place
✔ Restacked search-api, search-ui
```

Flags:
- `-b` / `--base <trunk>` the trunk to stack on

### `git stack pop`

> Alias: `git pop`

Deletes the branch you're on and takes it out of its stack, leaving its changes staged on its
parent, much like `git reset --soft` onto the parent would. Handy when you want to redo a branch
from scratch, or when it shouldn't be its own PR yet. The branches above it are restacked onto the
parent first, so they no longer carry its commits.

It wants a clean working tree (untracked files are fine), so the popped changes don't get mixed up
with anything else, and the branch has to be sitting on its parent; `git restack` puts it there.
It also won't run while the parent is checked out in another worktree, since the parent is about
to be checked out here. A conflict in the restack stops like any other: fix it, `git add` and
`git continue`, which then finishes the pop, or `git abort` to leave everything as it was. If the
branch has an open PR, the next `git ss` closes it with a comment saying why.

```
# on billing-webhook-alerts
❯ git pop
✔ Popped billing-webhook-alerts; its changes are staged on billing-webhook-schema
✔ Restacked billing-webhook-retries
  #412 will be closed at the next submit
```

### `git stack untrack [branch]`

> Alias: `git untrack`

The opposite of `git track`: stops tracking a branch (the one you're on unless you name one) and
every branch above it. gh stack keeps a stack as a single line, so taking a branch out of the
middle can't leave a gap above it. The branches themselves aren't touched at all, no commit, ref or
checkout changes, and a stack left with nothing in it goes away.

With `--only` just that branch comes out and the branches above it close the gap: they now sit on
its parent and get restacked there, which drops its commits from them. A conflict stops like any
restack, and `git abort` puts everything back, the untracked branch included.

PRs are left open either way. If GitHub already had the stack, the next `git ss` regroups what's
left of it. `git track` with the same branches tracks them again (as a stack of their own).

```
# on billing-webhook-alerts, in the middle of its stack
❯ git untrack --only
✔ Untracked billing-webhook-alerts
✔ Restacked billing-webhook-retries onto billing-webhook-schema
  git stack init billing-webhook-alerts tracks it again
```

Flags:
- `--only` untrack just this branch; the branches above it move down onto its parent

### `git stack submit`

> Aliases: `git submit`, `git ss`

Pushes every branch in the stack and creates or updates a PR for each one, each based on the branch
below it. `--down` stops at the current branch, and `git stack submit <branch>` at the named one,
leaving the branches above alone; handy for getting the bottom of a stack reviewed while you keep
working on the top. New PRs are ready for review by default.

Unless you pass `-n` / `--no-edit`, `--ai` or `--no-interactive`, gh stack opens its editor for the
title and body of each new PR. `--draft`, `--publish`, `--ai` and the editor only apply to new PRs.
gh stack can only submit a whole stack, so with `--down` git-stack pushes those branches itself,
creates their missing PRs and has `gh stack link` add them to the stack on GitHub; the editor isn't
available there, so new PRs take their titles from their commits unless you pass `--ai`.

If a PR lower in the stack has merged since you last synced, submit runs `git stack sync` first,
so the merged branch goes and the ones above it are restacked onto trunk before anything is pushed;
otherwise the next PR up would show the merged commits too. A dry run only says which branches that
would be.

Branches are pushed with `--force-with-lease`, so after a restack your branches are force
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
- `--down` only submit the current branch (or the one you name) and the ones below it

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

### `git stack pr [branch]`

> Alias: `git pr`

Opens the pull request of a branch (the current one if you don't name one) in your browser. Without
a terminal it prints the URL instead, so `git pr | pbcopy` does what you'd hope. The PR comes from
what git-stack already knows locally and GitHub is only asked when nothing local knows the branch's
PR (one opened by hand on github.com, say). A branch with no PR yet tells you to `git ss` first.

```
❯ git pr
✔ Opened #418 (payments-retry-queue) in your browser

❯ git pr --print billing-webhook-alerts
https://github.com/acme/payments/pull/421
```

Flags:
- `--print` print the URL rather than opening it

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

If the branch being deleted is the one you're on, sync switches you to its trunk first, and like
`git switch` your uncommitted changes come along. It only keeps the branch when git says the
switch would overwrite them, and then names the files. A worktree whose directory you deleted
by hand (rather than with `git worktree remove`) is forgotten at the start of the sync, so its
branches can be cleaned up like any other; a locked one is left alone with a notice.

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

Once you've fixed and `git add`ed the conflicts, finishes the rebase a restack, modify, reorder,
move or pop stopped on and then restacks whatever was left above it (and, for a pop, then finishes
the pop).

Flags:
- `-a` / `--all` stage every change first

### `git stack abort`

> Alias: `git abort`

Gives up on the rebase a restack, modify, reorder or move stopped on and puts back every branch the
operation had already moved, metadata included; after a reorder or move that means the stack order
too.

### `git stack undo`

> Alias: `git undo`

Puts back what the last git-stack command you ran in this worktree changed. Run it again to step
further back, up to 20 commands.

```sh
git create -a -m "Add the retry queue"   # oops, that should have been a modify
git undo                                 # back on the branch you were on, changes staged again
git modify
```

It covers create (`--insert` too), modify, absorb, restack, reorder, move, fold, pop, untrack,
init (`track`), sync and a rename that stayed on your machine. Every branch they touched goes back
to where it was (a branch sync or pop deleted comes back, one create made goes away), the stack
metadata is put back exactly, and so is what you had checked out. After create, modify or absorb
the changes they committed come back as staged changes, and after a pop you're back on the popped
branch with nothing staged; after anything else
your uncommitted changes stay as they are, and if one of them is in the way undo refuses before
it moves anything. A branch checked out in another worktree follows too, and the same rule
applies there.

Undo is local only and never touches GitHub. Submit, merge and a rename that renamed the branch
on GitHub change what's there, so a successful one clears the history (putting your branches back to before it would only make them
disagree with GitHub). Each worktree has its own history, so an undo here never reverses what
you or an agent did in another checkout.

If a branch has moved since the command (you committed on top of it, say), undo refuses and names
it. `--force` goes ahead and throws away what changed on those branches since; it still never
touches your uncommitted changes.

To keep the commits it might need from being garbage collected, undo points
`refs/git-stack/undo` at an empty commit whose parents are those commits. That ref shows up in
`git log --all`; it goes away once the history is empty.

Flags:
- `--list` show what can be undone, newest first, without changing anything
- `--force` undo even when branches moved since the command

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

`git stack log short` draws the same tree with just the branch names, one line each, and
`(needs restack)` after any branch whose parent has moved on. It only reads local git, so it never
waits on GitHub. To make that what `git stack` and `git stack log` show,
`git config --global stack.log.short true`; `git stack log long` still gets you the full tree.

```
❯ git stack log short
○ billing-webhook-retries (needs restack)
● billing-webhook-schema
│ ○ fix-login-timeout
├─┘
■ main
```

Flags (on `log`, `log short` and `log long`):
- `-s` / `--stack` only show the current branch's stack (on a trunk, every stack on it). On a
  branch that's in no stack it shows them all and says so.

### `git stack merge [branch]`

There's no `git merge` alias for this one, for obvious reasons.

Merges the PRs in the current stack, up to and including a branch (the current one by default),
into trunk as one all or nothing operation on GitHub; if any of them can't be merged, none are. It
then runs a sync, so the merged branches get cleaned up and anything left above them is restacked
onto the new trunk. Nothing is pushed after that, so run `git ss` to update the PRs that are left.

Every PR being merged has to exist and be ready for review; a draft or closed one stops the merge
before anything happens. So does a check that failed or is still running on any of those PRs'
head commits; they're all looked up in one request first, and if that request fails the merge
goes ahead with a notice rather than being blocked by our own check. `--force` skips it. GitHub's
own rules still apply either way (required checks, reviews, merge queues), so `--force` can't get
a PR past a required check. It uses the repo's default merge method unless you pass one, or set
`git config stack.merge.method`.

```
❯ git stack merge
🔀 Merging 3 pull requests into main…
✖ 2 pull requests have failing checks; nothing was merged
  #201 auth/api: lint, test (ubuntu-latest)
  #203 auth/ui: test (macos-latest); build still running
  ↳ fix them and run git stack submit, or git stack merge --force to merge anyway
```

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
- `-f` / `--force` merge even if a check failed or is still running
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
| `stack.log.short` | `false` | `true` makes `git stack` and `git stack log` show only branch names, like `git stack log short` |
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

Once main is green, write the release notes and tag with them as the message;

```sh
git tag -a v0.3.2 --cleanup=verbatim -F notes.md
git push origin v0.3.2
```

The tag has to be annotated (the release workflow refuses a lightweight one), and
`--cleanup=verbatim` matters because git otherwise drops every line starting with `#`, i.e. your
markdown headings. The notes are what people read first, so write them for someone who uses
git-stack rather than someone who reads the diff: a one line summary as the first line (`git stack
update` shows it in the terminal, so keep it short; it's cut to fit), then what's
new and why you'd care, then anything they have to do after upgrading (run `git stack install`
again, say). The same text works as the announcement.

The release workflow runs goreleaser, which builds Linux and macOS on amd64 and arm64, stamps the
version in, and publishes the archives plus `checksums.txt` to a GitHub release. The release
page is your notes, then GitHub's list of every pull request merged since the last tag, sorted
into sections by label (see [`.github/release.yml`](.github/release.yml)), then how to upgrade.
To see that list before tagging;

```sh
gh api repos/DomBlack/git-stack/releases/generate-notes -f tag_name=v0.3.2 -f previous_tag_name=v0.3.1 --jq .body
```

`git stack update` and `go install github.com/DomBlack/git-stack@latest` both pick the release up.

## License

MIT, see [`LICENSE`](LICENSE).
