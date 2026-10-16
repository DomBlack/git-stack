---
name: git-stack
description: Work with stacked branches and stacked pull requests using git-stack (git commands for GitHub's native stacked PRs, on top of gh stack). Use when the repository uses stacks, when the user mentions git stack, gt, stacked PRs, restacking, or when a change should be split into dependent pull requests.
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
| Start a branch on top of the current one (from trunk: a new stack) | `stack_create`; you write `message` |
| Add a branch in the middle of a stack, right above the current one | `stack_create` with `insert: true` (restacks the branches above onto it) |
| Change the current branch's commit and rebase everything above | `stack_modify` |
| Fold staged fixes into whichever commits of the stack last touched those lines | `stack_absorb` (`dry_run: true` to see the plan) |
| Rebase the stack after edits lower down | `stack_restack` |
| Change the order of a stack's branches | `stack_reorder` (`order` bottom to top) |
| Move a branch and what's above it onto another stack or onto trunk | `stack_move` (`only: true` to leave the rest behind) |
| Merge the current branch into the one below it (one branch, one PR) | `stack_fold` (`keep: true` keeps the current branch's name and PR) |
| Track branches made by hand as a stack | `stack_init` |
| Take the current branch out of its stack, keeping its changes staged on its parent | `stack_pop` |
| Move around | `stack_navigate` (`up`/`down`/`top`/`bottom` or `branch`) |
| Push and open/update PRs | `stack_submit`; you write `pull_requests` `{branch: {title, body}}`; ready for review unless `draft: true` |
| Pull trunk, restack, prune merged branches | `stack_sync` |

Write commit messages and PR text yourself; `use_ai` exists but is not needed when you are
the one calling.

## Rules

- Never `git rebase`, `git commit --amend` or `git push --force` by hand on a stacked branch:
  the branches above would be left behind. Use `stack_modify` / `stack_restack`.
- New work goes on a **new branch on top** (`stack_create`), or with `insert: true` right above
  the current branch when it belongs lower down. To change something lower down, `stack_navigate` there, edit, then
  `stack_modify` (it restacks the rest).
- On `code: "conflict"`: resolve the listed files, `git add` them, then call the same tool
  again with `continue: true` (or `abort: true`); after `stack_absorb`, `stack_reorder` or
  `stack_move` that is `stack_continue` or `stack_abort` (abort also puts the old order back).
- A branch checked out with uncommitted changes in another worktree makes `stack_reorder` and
  `stack_move` refuse with nothing changed. After either, `stack_submit` dissolves every
  stale GitHub stack, lets gh stack create the one it submits and creates the others again itself
  (its notices name each), so a move's source stack is grouped again by the same submit. A stack
  with fewer than two pull requests has nothing to group and the notice says so; a branch with
  no open pull request, or a failed create, gets a notice saying to submit from that stack.
- Plain git is fine for reading (`git status`, `git diff`, `git log`) and for staging files.
- Run `stack_view` before `stack_submit` so you know which branches get new PRs and need
  titles and bodies.

## CLI equivalents

`git stack create -m "…"`, `git stack modify -a`, `git stack absorb`, `git stack reorder a b c`,
`git stack move --onto <branch>`, `git stack fold`, `git stack init a b`, `git stack pop`, `git stack restack`,
`git stack up/down/top/bottom`, `git stack checkout`, `git stack submit --no-edit`,
`git stack sync -f`. Aliases: `git c`, `git m`, `git ab`, `git reorder`, `git move`, `git fold`, `git track`, `git pop`,
`git rs`, `git u`, `git d`, `git t`, `git b`, `git co`, `git ss`.
