# MCP server

`git stack mcp` runs a stdio [Model Context Protocol](https://modelcontextprotocol.io) server
that exposes the stack operations to coding agents. `git stack install --agents` registers it
with Claude Code (`claude mcp add -s user git-stack -- <path>/git-stack mcp`) and Codex
(`codex mcp add git-stack -- <path>/git-stack mcp`).

The tools call the same `pkg/app` use cases as the CLI. The server is always
non-interactive: no prompts, no TUI, no terminal hand-off. Stdout carries JSON-RPC only;
logs go to stderr and every subprocess is captured.

## Repository

Every tool accepts `repo_path` (an absolute path inside the repository). Without it the
server uses the client's first `roots/list` entry, then its own working directory. The
path is resolved with `git rev-parse`; anything outside a repository returns `not_repo`.

## Tools

| Tool | Purpose | Key inputs | Annotations |
|---|---|---|---|
| `stack_view` | Tree of every stack: branches, parents, PR number/state/URL/title, each branch's own `commits`, `needs_restack`, `needs_push`, current branch, untracked branches | `include_untracked`, `fresh` (default true) | read-only, open-world |
| `stack_create` | New branch on top of the current one (from trunk: new stack), commit staged changes | `message` (required), `branch`, `staging` = `all`/`update`/`none`, `use_ai` | non-destructive |
| `stack_modify` | Amend the current branch (or `mode: commit`) and restack everything above | `mode`, `staging`, `message`, `continue`, `abort` | destructive |
| `stack_absorb` | Fold each staged hunk into the commit of the current stack that last changed those lines, rewrite it and everything above, then restack what sits above the current branch; the result's `plan` lists every hunk's destination and what stays staged | `staging` = `all`/`update`/`none`, `dry_run` | destructive |
| `stack_reorder` | Change the order of the current stack's branches and restack them, checking nothing out; the next `stack_submit` dissolves the stale stack on GitHub and creates it again in the new order; a branch checked out with changes in another worktree refuses the call with nothing changed | `order` (required: every branch of the stack once, bottom to top) | destructive |
| `stack_move` | Move a branch, and unless `only` is set everything above it, onto the top of another stack or onto a trunk as a new stack, then restack; moves within one stack are `stack_reorder`'s job | `onto` (required: a trunk or another stack's top branch), `source` (default current), `only` | destructive |
| `stack_init` | Track branches that already exist as a new stack on a trunk; only the metadata changes, and `needsRestack` lists the branches that aren't on their parent | `branches` (bottom to top, default current), `base` | destructive (it writes the metadata, though no branch moves) |
| `stack_continue` | Finish an interrupted restack, modify, reorder or move once the conflicts are resolved and git added | `stage_all` | destructive |
| `stack_abort` | Give up an interrupted restack and put every moved branch back (and, after a reorder or move, the old stack order) | | destructive |
| `stack_restack` | Local rebase of the stack onto its parents, bottom branch onto the local trunk included; only checks out a branch to resolve a conflict through one git rebase | `scope` = `all`/`upstack`/`downstack`/`only`, `branch`, `continue`, `abort`, `stage_all` | destructive |
| `stack_navigate` | `up`/`down`/`top`/`bottom` with `steps`, or `branch` | `direction`, `steps`, `branch` | idempotent |
| `stack_submit` | Push and create/update chained PRs (with `down`, only up to the current branch); ready for review by default | `down`, `branch`, `draft`, `publish`, `dry_run`, `pull_requests` `{branch: {title, body}}`, `use_ai`, `force` | destructive, open-world |
| `stack_sync` | Fetch, update trunk and restack every stack (checked out or not, all worktrees), never pushing; merged branches deleted per `stack.sync.prune` | `prune` (force deletion) | destructive, open-world |
| `stack_merge` | Merge the stack's PRs up to a branch into trunk, all or nothing, then sync; refuses while a check on any of them failed or is still running | `branch` (default current), `method` = `merge`/`squash`/`rebase`, `no_sync`, `force` (skip the checks check; only with the user's say so) | destructive, open-world |

In `stack_view` each branch's `pr` carries its `title` (from the PR cache; empty when the cache
predates titles), and `commits` lists the branch's own commits as `{sha, subject}`: those not on
the branch below it (trunk for the bottom branch), newest first, full ids and subjects exactly as
git has them. There are at most 10; `more_commits` counts the rest. Both come from local state, so
they cost no network calls.

Agents write commit messages and PR text themselves; `use_ai` opts into git-stack's own
Claude Code drafter.

`git stack version` and `git stack update` have no MCP tools on purpose; they're about the
binary, not the stack, and an agent replacing the server it's talking to mid session is not
something we want to make easy. The server does report its version in the MCP handshake.

## Errors

Tool errors (`isError: true`) carry a JSON object in the text content:

```json
{"code": "conflict", "message": "rebase stopped on conflicts",
 "files": ["pkg/x.go"],
 "next_steps": ["resolve the conflicts in: pkg/x.go", "git add pkg/x.go",
                "call stack_modify with continue: true", "or give up with call stack_modify with abort: true"]}
```

Codes: `not_repo`, `not_in_stack`, `not_at_top`, `conflict`, `partial`, `rebase_active`, `locked`,
`stacks_unavailable`, `auth_required`, `not_installed`, `unsupported`, `invalid_args`,
`interaction_required`, `api_failure`, `disambiguate`, `modify_recovery`, `checks_failing`,
`checks_pending`, `signing_failed`, `unknown`.
`signing_failed` comes from `stack_create` and `stack_modify` when git could not sign the commit,
and from `stack_restack`, `stack_modify` and `stack_continue` when the git rebase a conflict
needs could not sign a replayed commit; most often an SSH signing key whose passphrase nobody
can type in (the server has no terminal). For a commit nothing was committed; for a rebase it is
left in progress with the commit rescheduled, so `stack_continue` carries on once signing works
and `stack_abort` still puts everything back. `next_steps` says which key to `ssh-add`, or how to
turn signing off.
`conflict` from `stack_absorb` means the commits were rewritten (the structured result says which
branches moved) and the restack of the branches above then stopped; `stack_continue` and
`stack_abort` apply to that restack only, the absorb itself stays. `invalid_args` from it names the
branch that needs restacking first, the branch with a merge commit it won't rewrite, or the checkout elsewhere
with uncommitted changes in the way; nothing has changed in those cases.
`conflict` from `stack_reorder` or `stack_move` means the new order is already recorded and the
restack stopped part way; the structured result comes back alongside the error, so you can see the
new order (or what moved and what was left behind). `stack_continue` finishes it, both halves of a
`move` with `only: true` included, and `stack_abort` puts the branches back and the stack order
too. A reorder or move that stops without a conflict after some branches moved is saved the same
way, so `stack_continue` finishes it and `stack_abort` undoes all of it. A branch checked out
with uncommitted changes in another worktree, or whose move would overwrite untracked files
there (or a conflict on a branch checked out elsewhere), makes `stack_reorder` and `stack_move`
fail with `invalid_args` and nothing changed.
Because GitHub can only create a stack or add to the top of one, the next `stack_submit`
dissolves every stale GitHub stack first and then creates again only the stack it submits. The
notice says which stack was dissolved and, when it isn't the one being submitted, which stack
to submit from ("dissolved stack #7 on GitHub because its pull requests were regrouped by a
reorder or move; submit from <bottom> to group them again"), so call
`stack_submit` from that other stack's bottom branch to group its pull requests. If dissolving
fails, a notice says so and the next submit tries again.
`checks_failing` and `checks_pending` come from `stack_merge` when a pull request it would land
has a check that failed, or (with none failed) one still queued or running, on its head commit.
Nothing is merged. The error's `checks` lists each held up pull request:

```json
{"code": "checks_failing", "message": "1 pull request has failing checks; nothing was merged",
 "checks": [{"number": 201, "branch": "auth/api", "failing": ["lint"], "pending": ["build"]}],
 "next_steps": ["fix them and run stack_submit, or stack_merge with force: true (only with the user's say so) to merge anyway"]}
```

Fix the failures and push, or wait for running checks and call again; `force: true` skips the
check, but GitHub's required checks still apply. If the checks can't be read at all the merge
goes ahead and the result carries a notice saying so.
`partial` comes from `stack_sync` when everything else was done but some branches could not
be updated; the structured result still comes back, and its `notUpdated` lists each one with
a `reason`: `locked` (another git process held a lock the update needed) or `refused` (git
would not move the checkout for any other reason, e.g. a merge in progress there) fail the
sync; `dirty` (uncommitted changes in the way) and `untracked` (untracked files in the way)
are listed too but are only warnings. Each entry has the `worktree` it is checked out in
(empty for a branch with no checkout). A `locked` entry has the `lock` file that was held and
its `lockKind`: `index` is that checkout's index lock, `ref` is the branch's own ref lock,
which can happen to a branch with no checkout at all, and after git has already moved a
checkout (so it can show the incoming changes as staged until a later sync finishes). Act on
the `lock` path and `lockKind` given; don't assume it's a checkout's `.git/index.lock`. A
`dirty` / `untracked` entry has the files in the way (`changed`, `untracked`, at most 20
each, with `moreChanged` / `moreUntracked` counting the rest).
Input-schema violations (a missing required field, a wrong type) are rejected by the SDK
before the tool runs and come back as plain text.

## Testing

`pkg/mcp` is tested over the SDK's in-memory transport against a temporary repository with
an in-memory backend, and a test replaces `os.Stdout` with a pipe to prove no tool path
writes to it. Another test checks that `pkg/mcp` has no transitive dependency on `pkg/ui`
or any Charm package.
