# AGENTS.md (git-stack)

git-stack is a CLI (`git stack <cmd>`) and stdio MCP server for GitHub's native stacked PRs, written in Go. It sits on top of `gh stack`.
`docs/architecture.md` holds the design, what we learnt about gh stack and the decisions behind it; read it before large changes.

## Toolchain
- Go 1.27. Never lower the `go` directive. Use modern stdlib (iterators, slices, maps, `encoding/json/v2`, `errors.AsType`) over hand-rolled helpers.
- Before finishing any change, all of these must pass (there is no task runner on purpose):
  `gofmt -l .` (empty), `go vet ./...`, `golangci-lint run`, `go test ./...`.

## Layout rules
- `cmd/`: cobra wiring only, **one command per file** (`cmd/<command>.go`). No business logic: parse flags, decide TTY vs non-TTY, call `pkg/app`, render output.
- `pkg/app`: use cases. Both `cmd/` and `pkg/mcp` call these; never duplicate logic between CLI and MCP.
- Ports live with their domain (`pkg/stack`, `pkg/forge`, `pkg/ai`). Adapters live in subpackages (`pkg/backend/ghstack`, `pkg/forge/github`, `pkg/ai/claudecode`).
- Only `cmd/root.go` (and `cmd/mcp.go` for server wiring) may import adapters. Nothing in `pkg/` imports `cmd/`. Adapters never import each other.
- No vendor-specific types (GitHub, Anthropic, gh-stack) in port signatures.
- All TUI code lives in `pkg/ui` and uses Charm v2 (`charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `charm.land/bubbles/v2`). No v1 imports.
- `cmd/imports_test.go` enforces the layering rules above; don't weaken it.

## Subprocesses
- All subprocesses go through `pkg/exec`. Never call `os/exec` directly elsewhere.
- Default to capturing output. TTY passthrough is opt-in, CLI-only, and forbidden in any code path reachable from `pkg/mcp`.
- Always pass a `context.Context` and honour cancellation.
- A non-zero exit is returned as `*exec.ExitError` (with captured stderr); a missing binary as `*exec.NotFoundError`. Adapters map these to `*stack.Error` with a `Kind` and `NextSteps`.

## CLI rules
- Every command has `ValidArgsFunction`; every non-bool flag has a completion func. The completion-coverage test enforces this; don't weaken it.
- Completion code must not touch the network, prompt, or write non-completion output. It may only run `git`.
- Keep command names, flags and short aliases familiar to people coming from other stacking tools where practical, but describe behaviour in our own words in help text and docs; never present a command as a copy of another tool's. Don't add flags the backend can't support. When behaviour differs because of a backend limitation, say so in one line of output rather than silently diverging.
- Every command works non-interactively (`--no-interactive` or no TTY).
- Every human facing line goes through `ui.Reporter` (results on stdout, notices, progress and errors on stderr) and follows `docs/style.md`; `cmd/style_test.go` fails the build on raw `fmt.Fprint` in `cmd/`.

## MCP rules
- Stdout is reserved for JSON-RPC. Log to stderr. Capture all subprocess stdout.
- MCP tools never prompt, never launch a TUI, and return structured, actionable errors.
- New CLI capabilities should get a matching MCP tool (or a documented reason why not).

## Config & safety
- Read and write git config via `git config`, never by editing files as text.
- Never edit `~/.claude.json` or `~/.codex/config.toml` directly; use `claude mcp` / `codex mcp`.
- `install` must never clobber existing user aliases or config without confirmation, and `--uninstall` removes only what we recorded as managed.
- Only `pkg/backend/ghstack` writes gh-stack's `<git-dir>/gh-stack` file, through its `Update` under `gh-stack.lock`, editing the JSON in place; nothing else writes it, and `gh stack` commands do every other mutation.

## Testing
- Tests must never touch the real user environment. Use `gittest.Isolate(t)` (sets `HOME`, `XDG_CONFIG_HOME`, `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_NOSYSTEM`) in any test that runs git or installers.
- `gh`, `claude` and `codex` are faked with `exectest.Fake` in unit tests and with recording executables on `PATH` in integration tests. No network in the default test run; live tests sit behind the `live` build tag.
- Domain logic gets table-driven unit tests; `pkg/git` gets integration tests against temp repos; TUIs get teatest golden snapshots with fixed size and colour profile; MCP tools get in-memory-transport tests.
- Shell completion hooks are verified against real shells with `go test -tags shellintegration ./pkg/shell/` (fish, bash; zsh via zpty). Run it after touching `pkg/shell/hooks`.

## Workflow
- Small, focused commits; each leaves the tree green.
- Update `README.md` (command table) and `docs/` in the same change as behaviour changes.
- Pushing a stack and opening or updating its PRs (`git stack submit` / `stack_submit`) is fine. Never merge a PR or a stack (`git stack merge` / `stack_merge`, `gh pr merge`) unless Dom has said so for that stack, and don't create remotes.
- Label every PR as soon as it's open (`gh pr edit <number> --add-label <label>`; submit can't set labels). The label picks its section in the release notes (`.github/release.yml`), so pick by what someone using git-stack would notice, one label per PR:
  - `breaking-change`: something that used to work now works differently or not at all (a flag, an alias, config, MCP tool arguments or results).
  - `enhancement`: a new command, flag, tool or behaviour.
  - `bug`: fixes something that was broken.
  - `documentation`: docs only.
  - `internal`: nothing a user would notice (refactors, tests, CI, release tooling).
  - `ignore-for-release`: leave it out of the notes altogether (a revert of something never released, say).

  In a stack, label each PR by its own change; lower PRs that only lay groundwork for a feature on top are `internal`.

## Releasing
- Only when Dom asks for one, and only once CI on main is green.
- Draft the notes from the PRs merged since the last tag (`gh api repos/DomBlack/git-stack/releases/generate-notes -f tag_name=<new> -f previous_tag_name=<last> --jq .body` lists them), in Dom's voice: a one line summary first (`git stack update` shows it in the terminal, so keep it short), then what's new and why it matters to someone using git-stack, then anything to do after upgrading. Show Dom the draft before tagging.
- Tag with the notes as the message: `git tag -a vX.Y.Z --cleanup=verbatim -F notes.md`, then push the tag. Never a lightweight tag; the release workflow refuses one. Without `--cleanup=verbatim` git drops the markdown headings.
