# Architecture

This is the design doc for git-stack. It started life as the plan before any code was
written and has since been accepted and trimmed down to the bits that still matter; i.e.
how the thing is put together, what we learnt about `gh stack` by reading its source, and
the decisions that fell out of that (with the reasoning, so nobody has to rediscover it).

If you want to know *what* a command does, `git stack <cmd> --help` and the README are the
source of truth. This doc is about *why* it does it that way.

## The shape of it

It's a ports and adapters app, wired by hand in `cmd/root.go`.

```
cmd/            cobra commands (one per file)   -+
pkg/mcp/        MCP tools                        +-> pkg/app (use cases) -> ports
                                                 |      pkg/stack  Metadata, Tracker, Restacker, Submitter, Syncer
                                                 |      pkg/forge  Forge (pull requests)
                                                 |      pkg/ai     Drafter (commit and PR text)
adapters: pkg/backend/ghstack, pkg/forge/github, pkg/ai/claudecode
infra:    pkg/exec (subprocesses), pkg/git (typed git), pkg/config, pkg/cache, pkg/ui
```

The important rule is that `cmd/` and `pkg/mcp` are both thin. They parse input, call
`pkg/app`, render output. Everything the CLI can do the MCP server can do, because they
call the same use cases; if you find yourself writing logic in `cmd/` it's in the wrong
place.

The ports:

- `pkg/stack` is the domain; the `Graph`/`Stack`/`Branch` model, pure navigation, and the
  five stack ports. The only adapter today is `pkg/backend/ghstack`, which implements all
  five by reading gh stack's metadata file and shelling out to `gh stack` for anything that
  mutates.
- `pkg/forge` is pull request operations with nothing GitHub specific in the signatures.
  Adapter: `pkg/forge/github` via the `gh` CLI.
- `pkg/ai` is the `Drafter` for commit messages, branch names and PR text. The prompts and
  JSON schemas live here (vendor neutral); `pkg/ai/claudecode` is the adapter that runs
  `claude -p`.

These layering rules are enforced by `cmd/imports_test.go` rather than by convention;
nothing in `pkg/` imports `cmd/`, adapters never import each other, only `cmd/root.go` and
`cmd/mcp.go` may import adapters, `pkg/mcp` must never (even transitively) pull in `pkg/ui`,
and nothing outside `pkg/exec` touches `os/exec`.

### Adding an adapter

1. Implement the port in a new subpackage (say `pkg/forge/gitlab`). Don't import other
   adapters.
2. Map subprocess or API failures to `*stack.Error` with a `Kind` and `NextSteps`. The
   next steps are what the CLI prints as bullets and what the MCP server returns as
   `next_steps`, so write them as things a person (or agent) can actually run.
3. Wire it in `cmd/root.go` (and `cmd/mcp.go` if the server needs it).
4. Fakes and tests; nothing in the default test run may hit the network.

### Subprocesses

Everything goes through `pkg/exec.Runner`. Capture is the default. Passthrough (a real
TTY handed to the child) is opt in, only exists on the CLI runner, and is only used for
`git add -p`, editors, and `gh stack submit`'s interactive editor. The MCP runner is built
without a TTY so it physically can't enter passthrough mode, which is how we guarantee an
MCP tool never blocks waiting on a terminal.

`pkg/git` also retries a command that failed because another process held
`.git/index.lock`, backing off from 25ms up to 400ms for about 2 seconds in total before
giving up with an error that names the command. IDEs and coding agents take that lock for a
few milliseconds every time they refresh status, and in a big repo a bare `git add -A` lands
in that window often enough to be annoying; git itself never retries.

## What gh stack actually is

All of this came from reading the gh-stack source (pinned at commit `2bd699a`, a copy of
its `schema.json` sits in `pkg/backend/ghstack/testdata`). Several of these findings
overturned assumptions in the original brief, so they're worth keeping.

**Storage.** One JSON file at `<git-dir>/gh-stack` (`schemaVersion: 1`), plus
`gh-stack.lock`, `gh-stack-rebase-state` and `gh-stack-modify-state` next to it. No refs,
no git config keys. The shape is roughly;

```json
{ "schemaVersion": 1, "repository": "github.com:owner/name",
  "stacks": [ { "id": "12345", "number": 7,
                "trunk": { "branch": "main", "head": "<sha>" },
                "branches": [ { "branch": "a", "head": "<sha>", "base": "<parent tip at last rebase>",
                                "pullRequest": { "number": 1, "id": "PR_x", "url": "...", "merged": false } } ] } ] }
```

- The parent relationship is array order (bottom to top). There is no parent field, so
  stacks are strictly linear.
- Trunk is per stack and several stacks can share one. A branch is in at most one stack.
- `<git-dir>` is per worktree, so a linked worktree doesn't see the main checkout's stacks.
- `gh stack view --json` exists but only covers the current stack, calls the GitHub API,
  and writes the stack file as a side effect. Useless for completion or for listing every
  stack.

**Exit codes** (from `cmd/utils.go` in gh-stack):

| Code | Meaning |
|---|---|
| 1 | generic, or already printed (cobra flag errors too) |
| 2 | not a repo / not in a stack |
| 3 | rebase conflict |
| 4 | GitHub API failure |
| 5 | invalid arguments at runtime (e.g. `add` when not at the top) |
| 6 | disambiguation needed (shared trunk, several stacks) |
| 7 | rebase already in progress (only from `modify`; `rebase` exits 1 for the same thing) |
| 8 | stack file locked or stale (5 second flock) |
| 9 | stacked PRs unavailable (in non-interactive `submit` this fires on *any* list failure, auth included) |
| 10 | `modify` recovery state present |

**`add`** is append only and must be run from the top (otherwise exit 5). Two quirks we had
to design around;

1. Running `add` from trunk appends to the existing stack on that trunk instead of starting
   a new one.
2. With `-m`/`-A`/`-u`, if the current branch has no commits beyond its parent the commit
   lands on the *current* branch and no new branch is created.

**`rebase --upstack --no-trunk`** is exactly the "restack after amend" operation we need.
With `--no-trunk` there's no fetch and no trunk update; it rebases the current branch onto
its parent and carries on upward. The bottom branch is never rebased onto trunk in this
mode. There is no `--only`.

**`submit`** has `--auto`, `--open` and `--remote`, nothing else. It always submits the
whole stack, new PRs are drafts unless `--open`, and there's no title/body/dry run. If
stdout is a TTY and `--auto` isn't passed it opens a full screen editor.

**`sync`** covers the current stack only; fetch, reconcile the remote stack, update trunk,
cascade rebase if needed, push, sync PRs, prune with `--prune`. If the remote has diverged
non-interactively it prints "Sync aborted" and exits 0 (yes, zero).

**Interactivity** is `stdout is a TTY || GH_FORCE_TTY is set`. Stdin isn't consulted. So
capturing stdout is a universal non-interactive switch, and we also strip `GH_FORCE_TTY`
from the environment in capture mode.

**It can't be a Go dependency.** The stack code is under `internal/`, and the one
importable `cmd` package drags in bubbletea v1. Hence the schema mirror in
`pkg/backend/ghstack/file.go`.

## Decisions and why

**Read the metadata file directly; write it only from one place.** Reading it is the only
way to see every stack, it works offline, it's fast enough for completion, and gh-stack
documents the file as a stable interface in its own AGENTS.md. For a long time nothing here
wrote it, but sync has to forget merged branches and finished stacks and gh stack has no
non-interactive command for that (`unstack --local` needs one of the stack's branches
checked out, which are exactly the ones that have gone). So `pkg/backend/ghstack.Update`
edits the file in place: it takes gh stack's own `gh-stack.lock` with `flock`, re-reads, lets
the caller edit the graph, and writes back by editing an ordered `jsontext` tree rather than
marshalling our structs, so members we don't model, PR records, stack ids and member order
survive untouched. Files whose stacks didn't change are not rewritten. Every other mutation
still goes through a `gh stack` command.

**Navigation is native.** `up`/`down`/`top`/`bottom` are a pure function over the graph
followed by `git switch`, with Graphite's semantics rather than gh-stack's; `down` from the
bottom branch lands on trunk, overshooting clamps silently, merged branches are skipped. We
also check `worktreepath` before switching so you get told which worktree has the branch
rather than a confusing git error.

**`create` from trunk runs `gh stack init`, from the top runs `gh stack add`, and we
always commit natively.** This sidesteps both `add` quirks above. From the middle of a
stack you get a `not_at_top` error with next steps; from an untracked branch you get told
the branch isn't in a stack (Graphite does the same). `--insert` is accepted and fails with
an explanation, so the flag is there for a future backend that can do it.

**`modify` amends natively, then `gh stack rebase --upstack --no-trunk`** if there's
anything above. Conflicts come back as a `conflict` error listing the files and the
`--continue`/`--abort` commands.

**`restack` and the bottom branch.** Because of `--no-trunk`, a plain restack never moves
the bottom branch onto a moved trunk. Rather than reimplement that step (which would need
our own continue state), we detect that the bottom branch is behind trunk and print a notice
pointing at `git stack sync`. Doing the bottom branch natively is the obvious follow up if
the notice gets annoying.

**`submit --ai` drafts first, then lets gh stack submit, then fixes the PRs up.** The
alternative was to push and create PRs ourselves and only use gh stack to link them. We
didn't, because gh stack owns the push (`--force-with-lease`), the base calculation, the
remote stack object and the PR records in the local file, and its `link` command pushes
without force and needs at least two PRs. The cost of our route is a few seconds where a new
PR shows gh stack's auto generated title before we overwrite it; drafting *before* the
submit keeps that window as small as possible. The MCP `stack_submit` tool goes down the
same path with the agent's own titles and bodies instead of the Drafter.

**New PRs: ask, and default to drafts.** On a terminal `submit` asks draft or publish
unless `-d`/`-p` is passed; non-interactively it drafts. `stack.submit.default` skips the
question.

**What we deliberately don't mirror from `gt` (yet).** `create --insert`, `restack --only`,
`submit --update-only`, `submit --edit-title/--edit-description` and
`modify --into` all need a backend that can do more than gh stack can. Each prints a single
line saying why. `sync --all` is not accepted at all: every trunk is synced, so there is
nothing for it to select.

**Sync is native.** `gh stack sync` only syncs the stack of the branch checked out where it
runs, so covering every stack meant checking each one out in turn, it never forgets a stack
whose every PR merged, it can't move trunk when there are no stacks, and a linked worktree's
metadata dies with the worktree. So `git stack sync` does what `gt sync` can be seen to do,
itself: one `git fetch --prune` of the first trunk's remote (`branch.<trunk>.remote`, else
`origin`; a second trunk that tracks another remote is still compared with the first
remote's copy, and gets a `no-remote` notice when there is none); fast forward every trunk (a diverged trunk is only reset
with `-f` or a yes at the prompt, a dirty checkout gets a notice); delete branches whose PR
merged or closed, whose tip is already in trunk, or, for untracked branches, whose merged PR
was for exactly the commit they're on (that last one is how branches orphaned by a dead
worktree get cleaned up), guarded by the PR's merge commit being in trunk so a PR merged
into its parent branch stays; fast forward any tracked branch the remote is strictly ahead
of, and say so when the remote holds commits we don't have (by patch id, so our own
unpushed restacks don't nag); then restack. A tracked branch whose merged or closed PR has a
head commit different from the local tip is kept with a notice, even under `-f`, when it
holds a change (by patch id, ignoring what came from trunk) the PR never saw; a branch
that sync restacked since its last push differs only by commit id and is deleted as usual.
Patch ids are `--verbatim` rather than `--stable`, so a whitespace only amend counts as a
change and keeps the branch.
A branch recorded in two worktrees' metadata files is read from the first; once it is
deleted the stale record in the other file shows up as gone on the next sync and is
dropped then, so it heals over two syncs. A candidate checked out in the current worktree whose
trunk is checked out in another worktree leaves HEAD detached at the trunk tip rather than
failing. Consent for deletion is `stack.sync.prune` (`always` by default), `-d`, or `-f`;
with no terminal and `ask`, branches are kept with a notice. Nothing is pushed; `submit`
does that.

**The restack never checks anything out.** Each branch's commits (from the metadata's
`base` when it is still an ancestor, else the merge base) are replayed onto the new parent
with `git merge-tree --write-tree` and `git commit-tree`, keeping author, date and message.
The fallback range for a branch with no usable recorded base starts at the merge base with
the parent's old tip, so when a squash merged parent was deleted and no base was recorded
the parent's commits get replayed again, which is why the metadata base matters. A
replayed commit whose changes are already in the new parent comes out with the parent's
tree and is dropped, as `git rebase` drops it, so that case costs nothing either.
Stacks are computed in parallel since that only creates objects, then each stack's branches
move in one `git update-ref --stdin` transaction with expected old values, so a stack moves
whole or not at all. A branch checked out in a clean worktree gets `reset --hard` there; a
dirty one, or one whose move would add a path that clashes with an untracked file in that
worktree (the same path, or one is a directory the other sits in; `reset --hard` would
delete it), is left and the branches above rebase onto its current
tip. A diverged trunk reset with `-f` gets the same untracked file check. A conflict stops that
stack at that branch with a notice pointing at `git stack restack`, which still goes through
`gh stack rebase` and its interactive flow; the other stacks finish, and the command exits
non-zero at the end. A git error while planning a stack, a refused ref transaction, or a
failed worktree reset marks that stack failed and the command exits non-zero too, never
silently skipping it. Flags match gt: `-f`, `-d/--delete-all` and `--no-restack`; `--all` is
gone because every stack is always synced.

**gh stack treats queued PRs as gone; we put the bases back.** When a branch's PR is sitting
in a merge queue, `gh stack submit` skips it like a merged one and bases the next PR on the
first branch below that is neither merged nor queued, usually trunk. Until the queue drains
that new PR shows the whole stack's diff, which is exactly what you don't want from a stack.
GitHub does let the base of a stacked PR be changed, so after every submit we compare each
open PR's base with its real parent (the nearest branch below whose PR has not actually
merged, queued or not), move it back with `gh pr edit --base` when it differs, and say so in a
notice. The same wrong base also made GitHub reject gh stack's attempt to append the PR to the
stack object (the Stacks API wants an unbroken base to head chain; a queue only stops PRs being
removed, not added), so after a fix we run the backend submit once more and it appends cleanly.
gh stack will grumble that the base isn't what it expected on later submits; that's a warning,
not a failure.

**Merged branches are deleted by default.** `stack.sync.prune` decides: `always` (default)
deletes branches whose PRs merged or closed, `ask` prompts on a terminal and keeps with a
notice otherwise, `never` keeps. `-d` and `-f` delete regardless. A branch checked out in
the current worktree is moved off first.

**Claude Code is driven through `claude -p`**, not an SDK, so it uses whatever login you
already have. The call is `--tools ""`, `--output-format json`, `--json-schema <schema>`,
`--strict-mcp-config`, `--no-session-persistence`, `--permission-prompts none`, with the
diff on stdin and the answer read from `structured_output`. Things learnt the hard way;
`--strict-mcp-config` is mandatory (without it Claude loaded every connector on the machine
and the request failed as too long at ~256k tokens), there is no `--max-turns` in current
versions so we bound the call with a context timeout instead, and `--bare` is rejected
because it disables keychain auth. Default model is `haiku`; a call is about 9 seconds and
roughly $0.02.

**PR state is cached** under `<git-common-dir>/git-stack` with a TTL (default 5m) and used
stale while revalidate; the checkout picker renders from the cache straight away and
refreshes in the background, completion only ever reads it, and the MCP `stack_view`
refreshes when stale unless told not to.

**Completion only runs `git`.** It reads the metadata file and the PR cache, never
`gh` or `claude`, never prompts, and the test suite walks the whole command tree to make
sure every command and every non bool flag is completable. The shell side (how `git ss
<TAB>` gets back to us through bash, zsh and fish's own git completion) is written up in
[`completion.md`](completion.md).

**MCP and roots.** Protocol 2026-07-28 (the SDK default) removed the initialise handshake
and roots altogether, and older protocols forbid `roots/list` while a request is being
served. So roots are fetched asynchronously at session start for old protocol clients only,
and resolution is `repo_path`, then roots, then cwd. See [`mcp.md`](mcp.md) for the tools.

**Version and self update.** `pkg/version` tries three sources; the value goreleaser stamps
in with ldflags, then the module version `go install @vX.Y.Z` records in the build info, then
the VCS details `go build` records for a checkout (so a dev build says `dev (3a9f2c1,
2026-09-30, dirty)` rather than just `dev`). `pkg/update` talks plain HTTPS to the GitHub
releases API and the asset URLs rather than going through `gh`, so updating doesn't need a
login. It verifies the archive against goreleaser's `checksums.txt`, writes the new binary
next to the old one and renames it into place. Dev builds aren't replaced without `--force`.
Releases are cut by tagging; the workflow runs goreleaser for linux and darwin on amd64 and
arm64.

**How the CLI talks is one thing, in one place.** Every command prints through `ui.Reporter` (marks, emoji headlines, spinners on a terminal; `ok:`/`note:`/`error:` when piped) and the rules are written down in [`style.md`](style.md). `pkg/app` decides where a phase starts and ends through the `Progress` hook and only names the phase; the reporter owns the look.

**Terminal niceties live in the CLI, never the MCP server.** Long gh stack commands
(submit, sync) have their stderr relayed to the terminal as it arrives rather than
dumped at the end; `pkg/exec` tees it through `Cmd.Stream`, `pkg/backend/ghstack` only
sets that when `cmd/root.go` asks via `WithOutput`, and the result carries a `Streamed`
flag so nothing is printed twice. PR numbers and URLs are OSC 8 hyperlinks when stdout is a
terminal (Ghostty, iTerm2, WezTerm make them clickable), and long operations set the OSC 9;4
"busy" progress state on stderr so the terminal can show something is happening; terminals
that don't know the sequence ignore it.

**Prompts and bubbletea's inline renderer.** bubbletea v2.0.10 mispositions a final frame
that is shorter than the previous one (the question line showed up twice after answering)
and erases a final frame that has no trailing newline. So the select prompt's final frame
keeps the height of the interactive one and `Prompter.Select` erases the padding after the
program exits, while the confirm prompt renders an empty final frame and prints its own
summary line. Both were verified by driving the real prompt under a pty with expect; if the
renderer gets fixed upstream this can go back to the obvious implementation.

**No task runner, no fuzzy matching dependency.** The whole check is `go build`, `go vet`,
`golangci-lint run`, `go test` (recorded in AGENTS.md and CI), and the picker's filter is a
small native subsequence matcher.

## Testing

The rules are in AGENTS.md; the short version is that tests must never touch the real user
environment (`gittest.Isolate`), `gh`/`claude`/`codex` are faked, nothing in the default
run hits the network, TUIs get teatest goldens at a fixed size and colour profile, MCP tools
are tested over the in memory transport with a guard that fails if anything writes to
stdout, and the shell hooks are tested against real shells (zsh via zpty, pressing Tab for
real) behind the `shellintegration` tag.

## Later

- A native backend (our own metadata and push) would unlock everything in the "don't
  mirror" list above. The ports are shaped for it already; `Children` returns a slice and
  `Scope` has an `Only` even though gh stack can't use them.
- Rebase the bottom branch onto local trunk during `restack` instead of printing a notice.
- `submit --reviewers`/`--team-reviewers` via the forge after submit.
