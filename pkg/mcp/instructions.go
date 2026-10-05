package mcp

// Instructions is sent to clients at initialisation. Codex weighs the start
// of it most, so the key guidance comes first.
const Instructions = `git-stack manages stacked branches and stacked pull requests on top of GitHub's gh stack. Prefer these tools over raw git whenever the user works in a stack: stack_view shows every stack (branches, parents, PR state, needs_restack); stack_create starts a new branch on top of the current one (you write the commit message); stack_modify amends the current branch (or adds a commit) and restacks everything above it; stack_restack rebases the stack locally; stack_navigate moves up/down/top/bottom or checks out a branch; stack_submit pushes and creates or updates PRs (you write titles and bodies; drafts by default); stack_sync fetches, fast forwards trunk, deletes branches whose pull requests merged or closed, fast forwards branches that moved on the remote and restacks every stack without checking anything out; it never pushes.

Never run git rebase or git commit --amend by hand inside a stack: descendants would not be restacked. Use stack_modify / stack_restack instead.

Errors are JSON objects {code, message, next_steps, files?, branch?}. On code "conflict": resolve the listed files, run git add on them, then call the same tool again with continue: true (or abort: true to give up). Code "not_at_top": gh stack can only add branches at the top of a stack; call stack_navigate {direction: "top"} first. Code "interaction_required": the operation would need a terminal; pass the required text (message, pull_requests) instead.

Every tool accepts repo_path (an absolute path inside the repository); otherwise the client's first root, then the server's working directory, is used. stack_view is read-only; stack_submit pushes to the remote and stack_sync deletes local branches.`
