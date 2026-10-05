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
| `⚠` | worked, but you should know something | notice, stderr, yellow |
| `↳` | something you can do about it | under an error, stderr |
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
| install | 🔧 | (install prints a report, no spinner) |

## Spinners

Anything that can take longer than a blink gets a spinner; gh stack init and add, restack,
submit, sync, Claude, the release download. The spinner line is replaced in place by the
`✔` line when it finishes, so what's left on screen is a clean transcript. The terminal's
OSC 9;4 "busy" state (Ghostty, Windows Terminal) starts and stops with the spinner, so the
tab shows something is happening too. Output from gh stack arriving while a spinner runs is
printed above it in the `│` gutter, i.e. the spinner stays on the bottom line and the detail
scrolls up as it comes in.

## Words

- Headlines are present participles ending in an ellipsis; "Restacking…", "Submitting
  stack…".
- Result lines are past tense with an object; "Restacked 2 branches above feat-a", never a
  bare "Done." or "Synced.".
- Errors say what's wrong in one line, then the detail (faint), then `↳` next steps that
  are things you can actually run.
- Notices are for "it worked but"; the bottom branch being behind trunk, a dry run.
- Branch names are bold cyan, PR refs green and clickable (OSC 8 hyperlinks), shas faint,
  subjects plain. Semantic colour only; nothing is coloured for decoration.

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
