---
name: git-stack
description: Work with stacked branches and stacked pull requests using git-stack (a Graphite-style CLI on top of gh stack). Use when the repository uses stacks, when the user mentions git stack, gt, stacked PRs, restacking, or when a change should be split into dependent pull requests.
---

# git-stack

git-stack keeps a *stack*: a chain of branches, each based on the one below, rooted at the
trunk (usually `main`). Every branch becomes one pull request whose base is its parent, so
reviewers see small, dependent PRs.

## Prefer the MCP tools

When the `git-stack` MCP server is available, use its tools instead of raw git for anything
that touches a stack:

| Task | Tool |
|---|---|
| See the stacks, PR state, what needs a restack | `stack_view` |
| Start a branch on top of the current one (from trunk: a new stack) | `stack_create` — you write `message` |
| Change the current branch's commit and rebase everything above | `stack_modify` |
| Rebase the stack after edits lower down | `stack_restack` |
| Move around | `stack_navigate` (`up`/`down`/`top`/`bottom` or `branch`) |
| Push and open/update PRs | `stack_submit` — you write `pull_requests` `{branch: {title, body}}`; drafts unless `publish: true` |
| Pull trunk, restack, prune merged branches | `stack_sync` |

Write commit messages and PR text yourself; `use_ai` exists but is not needed when you are
the one calling.

## Rules

- Never `git rebase`, `git commit --amend` or `git push --force` by hand on a stacked branch:
  the branches above would be left behind. Use `stack_modify` / `stack_restack`.
- New work goes on a **new branch on top** (`stack_create`); gh stack cannot insert a branch
  in the middle. To change something lower down, `stack_navigate` there, edit, then
  `stack_modify` (it restacks the rest).
- On `code: "conflict"`: resolve the listed files, `git add` them, then call the same tool
  again with `continue: true` (or `abort: true`).
- Plain git is fine for reading (`git status`, `git diff`, `git log`) and for staging files.
- Run `stack_view` before `stack_submit` so you know which branches get new PRs and need
  titles and bodies.

## CLI equivalents

`git stack create -m "…"`, `git stack modify -a`, `git stack restack`, `git stack up/down/top/bottom`,
`git stack checkout`, `git stack submit --no-edit`, `git stack sync -f`. Aliases: `git c`, `git m`,
`git rs`, `git u`, `git d`, `git t`, `git b`, `git co`, `git ss`.
