# AGENTS.md (git-stack)

git-stack is a Graphite-style stacked-branch CLI (`git stack <cmd>`) and stdio MCP server, written in Go.
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
- Mirror Graphite (`gt`) names, flags and short aliases where practical. When behaviour differs because of a backend limitation, say so in one line of output rather than silently diverging.
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
- We never write gh-stack's `<git-dir>/gh-stack` file; only `gh stack` commands mutate it.

## Testing
- Tests must never touch the real user environment. Use `gittest.Isolate(t)` (sets `HOME`, `XDG_CONFIG_HOME`, `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_NOSYSTEM`) in any test that runs git or installers.
- `gh`, `claude` and `codex` are faked with `exectest.Fake` in unit tests and with recording executables on `PATH` in integration tests. No network in the default test run; live tests sit behind the `live` build tag.
- Domain logic gets table-driven unit tests; `pkg/git` gets integration tests against temp repos; TUIs get teatest golden snapshots with fixed size and colour profile; MCP tools get in-memory-transport tests.
- Shell completion hooks are verified against real shells with `go test -tags shellintegration ./pkg/shell/` (fish, bash; zsh via zpty). Run it after touching `pkg/shell/hooks`.

## Workflow
- Small, focused commits; each leaves the tree green.
- Update `README.md` (command table) and `docs/` in the same change as behaviour changes.
- Don't push, create remotes, or open PRs.
