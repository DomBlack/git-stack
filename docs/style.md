# CLI style guide

How git-stack talks to you. Every command follows this, and `cmd/style_test.go` fails the
build if one prints around it, so a new command picks the house style up for free.

The short version; every command tells the same small story. A headline while something is
happening, live detail underneath it, one result line at the end. Nothing is silent on
success, nothing says more than it needs to, and the whole thing goes through
`ui.Reporter` so it all looks the same.

## The shape of a command

```
❯ git ss --ai
🤖 Drafting pull request for payments-retry-queue…        spinner; replaced in place by
✔ Drafted pull request for payments-retry-queue
🚀 Submitting stack…                                     spinner headline
│ Checking stack state...                                gh stack's own output, live
│ Pushing to origin...
│ ✓ Created PR #418 for payments-retry-queue
✔ Submitted 1 branch
  payments-retry-queue  #418 created  https://github.com/…
```

```
❯ git modify -a
✔ Amended feat-a  3a9f2c1 Fix the config path
🧱 Restacking 1 branch above feat-a…
✔ Restacked 1 branch above feat-a

❯ git create
✖ feat-c is not the top of its stack (top is feat-d)
  ↳ run git stack top and create the branch there
  ↳ gh stack can only add branches at the top
```

## The marks

One vocabulary, used everywhere, nothing else;

| Mark | Meaning | Where |
|---|---|---|
| `✔` | it worked | result line, stdout, green |
| `✖` | it didn't | error line, stderr, red |
| `⚠` | worked, but you should know something | notice, stderr, yellow; or the result line of a command that finished but left something you have to deal with, stdout |
| `↳` | something you can do about it | under an error, stderr; or under a `⚠` result line, stdout |
| `│` | a line from a subprocess (gh stack) as it happens | under a headline, stderr, faint |
| `●` `○` `■` | current branch, other branch, trunk | the tree picker and `git stack` / `git stack log` |

## Headlines carry the emoji

One per command family, always the same one, only ever on the in progress headline. They
read as section markers rather than confetti because they never show up anywhere else.

| Phase | Emoji | Headline |
|---|---|---|
| create | 🌱 | Creating branch feat-a… |
| modify | ✏️ | (modify's own work is instant; its restack uses 🧱) |
| restack | 🧱 | Restacking 2 branches above feat-a… |
| submit | 🚀 | Submitting stack… |
| sync | 🔄 | Syncing with origin… |
| Claude | 🤖 | Drafting pull request for feat-a… |
| update | 📦 | Checking for updates… |
| merge | 🔀 | Merging 3 pull requests into main… |
| install | 🔧 | (install prints a report, no spinner) |

## Spinners

Anything that can take longer than a blink gets a spinner; gh stack init and add, restack,
submit, sync, Claude, the release download. The spinner line is replaced in place by the
`✔` line when it finishes, so what's left on screen is a clean transcript (and the tab
shows it too; see [Terminal integration](#terminal-integration)). Output from gh stack
arriving while a spinner runs is printed above it in the `│` gutter, i.e. the spinner stays on
the bottom line and the detail scrolls up as it comes in.

## Words

- Headlines are present participles ending in an ellipsis; "Restacking…", "Submitting
  stack…".
- Result lines are past tense with an object; "Restacked 2 branches above feat-a", never a
  bare "Done." or "Synced.".
- Errors say what's wrong in one line, then the detail (faint), then `↳` next steps that
  are things you can actually run.
- Notices are for "it worked but"; a branch left alone because its checkout in another worktree is dirty, a dry run.
- The result line never claims more than happened. A command that finished but left something
  undone for a reason that's yours to sort out (uncommitted changes in the way, a diverged
  trunk you said no to) ends on a `⚠` result line instead of `✔` ("Synced, but main was not
  updated"); one that couldn't do part of its job for any other reason (a conflict, another
  git process holding a lock) ends on an error and exits non-zero, after showing what it did
  manage. Never "nothing to do" when something was left.
- A `⚠` result line is followed straight away by why, one idea per line: which branch, which
  checkout (`~` shortened), which files (three, then "and N more", relative to that
  checkout), then a `↳` with a command that works from wherever the user is (`git -C
  ~/src/app stash`). Never git's own stderr. Every path or file name in a command is quoted for
  the shell (`shell.Arg` / `shell.Path`); one with a control character can't be pasted safely,
  so it gets directions ("stash them in the checkout at …") instead of a command.
- Shas are short (seven characters) and faint; `Reporter.SHA` does both.
- A list of commits (a branch's own commits in `git stack log`) is one per line, newest first: a
  faint `•`, the faint short sha, ` - ` and the subject, plain and escaped (`• 7ac32ef - Reject
  expired refresh tokens`). Ten at most, then a muted `• and N more`. On a terminal a subject, like
  a PR title, is cut short with `…` to fit the width rather than wrapping under the tree.
- Names and text from outside (file names, git's stderr, gh stack's output) never reach the
  terminal raw. A file name with anything odd in it is shown C quoted (`ui.QuoteName`), and
  the Reporter escapes control characters in notices, errors and gutter lines. Structured
  output (MCP) keeps the raw names.
- Branch names are bold cyan, PR refs take the colour of their state (green open, faint
  draft, magenta merged, red closed) when it's known and the line's own colour when it isn't,
  shas faint, subjects plain. Semantic colour only; nothing is coloured for decoration.
- Anything clickable is underlined and nothing else is; see links below.

## Terminal integration

Besides the text, git-stack tells the terminal a few things with escape sequences. The same
rules hold for every one of them;

- Only to a terminal, and only to the stream that is one. A sequence on a stdout line needs
  stdout to be a terminal, one on stderr needs stderr to be; never decide one from the other.
- Never from the MCP server (stdout is JSON-RPC and the agent reports its own status) and
  never from completion. Neither has a Reporter, which is how that's kept true.
- Every sequence we build ends in ST (`ESC \`), never BEL. (bubbletea's renderer rewrites the
  links it draws with BEL; that's fine, it's the renderer's output, not ours.)
- Terminals ignore sequences they don't know, so we don't probe for support first.
- No question while a step is running. A spinner would draw over the prompt and the status
  would say `working` and `blocked` at once. A use case that needs the user's say so ends its
  step, asks through the `Prompter` port, then starts a new step for whatever the answer
  unlocks (sync's diverged trunk: "Updating main", the question, then "Resetting main to
  origin/main").
- `GIT_STACK_NO_TERMINAL_STATUS=1` turns off everything that describes what the command is
  doing (the title and the program status); links and the progress state stay.
  `--quiet` turns all of that off too.

**Links (OSC 8).** Every pull request number (`#123`) and every URL shown to a person is a
link to it; result lines, notices, errors, the lines relayed from gh stack, the log tree and
the picker. Don't link at the call site; print through the Reporter, which links each line
as it's written (`ui.Linkify`), and give `Ref` the URL (and the PR's state if you know it)
so it can be found. Blocks rendered elsewhere (the log tree, the picker) take `Reporter.Links()`
and `Reporter.PRURL`, the same lookup Linkify uses, so a PR the snapshot only has a number for
(offline, no URL in gh stack's metadata) is linked there too. PR URLs come from local state through the forge port, never a hard coded
host. A link is underlined, always, and that's its only cue; its colour stays whatever the
text already was, so a PR keeps its state colour and a URL the line's colour. `Hyperlink`
does the underline, so nothing that emits a link can forget it, and it only appears with the
link, so piped output stays plain ASCII. It keeps the underline across the text's own style
resets and turns it off with SGR 24 rather than a full reset, so the style around the link
(a selected row's reverse, say) carries on. Under `NO_COLOR` the underline stays, like bold
and the marks; it isn't colour. Each link has an `id=` derived from its URL, so a link
bubbletea redraws in pieces is still one link, and its URI is percent encoded outside bytes
32 to 126.

**Progress (OSC 9;4).** A step sets the indeterminate "busy" state (a pulsing tab in Ghostty,
the taskbar in Windows Terminal) for exactly as long as its spinner runs, and clears it when
the step ends, error or not.

**Window title (OSC 2).** Each step sets the title to `git stack: <headline>`, the headline
without its emoji or ellipsis. The first step pushes the title the terminal had (XTWINOPS
`CSI 22;2 t`) and later steps only set it; never push or pop per step. `Reporter.Finish` puts
it back, once, at the end of the process, by writing an empty title and then popping
(`CSI 23;2 t`); the pop restores it where the title stack works, the empty title resets it
where the pop is a no-op (Ghostty). Finish runs on every way out, an error, Ctrl-C, SIGTERM
or a panic, because every command in the tree is wrapped to call it when its `RunE` returns
(`ownTerminal` in `cmd/root.go`); a new command gets that by being added to the tree. A
command with no steps doesn't touch the title.

**Program status (OSC 7501).** Sent alongside the title, with `app=git-stack` on every report
(a report replaces the whole record) and `msg` as one clean line of text, base64 encoded.

| When | State |
|---|---|
| a step starts | `working`, the headline as `msg` |
| a prompt or picker is waiting on the user | `blocked`, `kind=question`, the question as `msg` |
| the question is answered | `working` again |
| the command succeeded | `done`, the last result line as `msg` |
| it failed (a restack conflict included) | `error`, the error's one line message |
| Ctrl-C (in a step, a prompt or the picker) or SIGTERM | `idle`, exit 130 |
| it only ever waited on the picker, closed with Escape or a choice | `clear` |

The last four come from `Reporter.Finish`, on the same exit paths as the title. Anything that
waits on the user goes through `Reporter.Waiting` (the `Prompter` does this for you) so it
shows as `blocked`.

**tmux.** Only OSC 7501 is wrapped in tmux's passthrough (`ESC P tmux; … ESC \` with every
`ESC` inside doubled), because tmux doesn't know it and would drop it. Links, the progress
state and the title go out bare; tmux handles each of those itself (its `hyperlinks`
feature, its progress bar and the pane title), and wrapping them would skip that.

## When it's not a terminal

Piped, in CI or under `--no-interactive`, the words are the same but the dressing goes;

```
ok: Checked out feat-a
Submitting stack...
ok: Submitted 2 branches
  feat-a  #12 updated  https://github.com/…
note: gh stack submits the whole stack
error: feat-c is not the top of its stack
  - run git stack top and create the branch there
```

No colour, no spinner (the headline prints once as a plain line), no emoji, no marks;
`ok:`, `note:`, `error:` and `  - ` instead, so scripts and logs see plain ASCII. `NO_COLOR`
drops colour but keeps the marks. `--quiet` prints errors only.

The MCP server never sees any of this; it returns structured results and the agent does its
own talking.

## How it's wired

`pkg/ui.Reporter` owns every line; `Success`, `Info`, `Warn`, `Error`, `Step` (headline
plus spinner plus busy state around a function) and `Stream` (the `│` gutter writer that the
gh stack backend writes to). `cmd/root.go` builds one per invocation from the process
streams and passes it to the commands and, as the `Progress` hook, to `pkg/app`. `pkg/app`
decides where the phases start and stop (it knows when the prompt has finished and the push
is about to begin) but only ever hands over a phase name and a message; the emoji, marks and
colours are the reporter's business.
