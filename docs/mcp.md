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
| `stack_view` | Tree of every stack: branches, parents, PR number/state/URL, `needs_restack`, current branch, untracked branches | `include_untracked`, `fresh` (default true) | read-only, open-world |
| `stack_create` | New branch on top of the current one (from trunk: new stack), commit staged changes | `message` (required), `branch`, `staging` = `all`/`update`/`none`, `use_ai` | non-destructive |
| `stack_modify` | Amend the current branch (or `mode: commit`) and restack everything above | `mode`, `staging`, `message`, `continue`, `abort` | destructive |
| `stack_restack` | Local rebase of the stack | `scope` = `all`/`upstack`/`downstack`, `continue`, `abort` | destructive |
| `stack_navigate` | `up`/`down`/`top`/`bottom` with `steps`, or `branch` | `direction`, `steps`, `branch` | idempotent |
| `stack_submit` | Push and create/update chained PRs; drafts by default | `publish`, `dry_run`, `pull_requests` `{branch: {title, body}}`, `use_ai` | destructive, open-world |
| `stack_sync` | Fetch, update trunk, restack and push every stack (checked out or not, all worktrees); merged branches deleted per `stack.sync.prune` | `prune` (force deletion) | destructive, open-world |
| `stack_merge` | Merge the stack's PRs up to a branch into trunk, all or nothing, then sync | `branch` (default current), `method` = `merge`/`squash`/`rebase`, `no_sync` | destructive, open-world |

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

Codes: `not_repo`, `not_in_stack`, `not_at_top`, `conflict`, `rebase_active`, `locked`,
`stacks_unavailable`, `auth_required`, `not_installed`, `unsupported`, `invalid_args`,
`interaction_required`, `api_failure`, `disambiguate`, `modify_recovery`, `unknown`.
Input-schema violations (a missing required field, a wrong type) are rejected by the SDK
before the tool runs and come back as plain text.

## Testing

`pkg/mcp` is tested over the SDK's in-memory transport against a temporary repository with
an in-memory backend, and a test replaces `os.Stdout` with a pipe to prove no tool path
writes to it. Another test checks that `pkg/mcp` has no transitive dependency on `pkg/ui`
or any Charm package.
