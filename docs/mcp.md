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
| `stack_continue` | Finish an interrupted restack or modify once the conflicts are resolved and git added | `stage_all` | destructive |
| `stack_abort` | Give up an interrupted restack and put every moved branch back | | destructive |
| `stack_restack` | Local rebase of the stack onto its parents, bottom branch onto the local trunk included; only checks out a branch to resolve a conflict through one git rebase | `scope` = `all`/`upstack`/`downstack`/`only`, `branch`, `continue`, `abort`, `stage_all` | destructive |
| `stack_navigate` | `up`/`down`/`top`/`bottom` with `steps`, or `branch` | `direction`, `steps`, `branch` | idempotent |
| `stack_submit` | Push and create/update chained PRs; ready for review by default | `draft`, `publish`, `dry_run`, `pull_requests` `{branch: {title, body}}`, `use_ai`, `force` | destructive, open-world |
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
most often an SSH signing key whose passphrase nobody can type in (the server has no terminal);
nothing was committed and `next_steps` says which key to `ssh-add`, or how to turn signing off.
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
