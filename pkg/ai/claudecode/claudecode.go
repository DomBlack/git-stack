// Package claudecode implements ai.Drafter with the local Claude Code CLI in
// headless mode (`claude -p`). We compute all context ourselves and pipe it
// on stdin; tools are disabled so the model cannot touch the repository.
package claudecode

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/DomBlack/git-stack/pkg/ai"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Config selects the binary, model and limits.
type Config struct {
	// Command is the Claude Code binary (git config stack.ai.command).
	Command string
	// Model is passed to --model (git config stack.ai.model).
	Model string
	// ExtraPrompt is appended to every instruction.
	ExtraPrompt string
	// Timeout bounds one call.
	Timeout time.Duration
}

// Adapter runs claude.
type Adapter struct {
	run exec.Runner
	cfg Config
	log *slog.Logger
}

// New returns an Adapter. Zero config fields take defaults (claude, haiku, 60s).
func New(r exec.Runner, cfg Config, log *slog.Logger) *Adapter {
	if cfg.Command == "" {
		cfg.Command = "claude"
	}
	if cfg.Model == "" {
		cfg.Model = "haiku"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Adapter{run: r, cfg: cfg, log: log}
}

var _ ai.Drafter = (*Adapter)(nil)

// NestedSessionEnv is set by Claude Code inside a session.
const NestedSessionEnv = "CLAUDECODE"

// response is the subset of `--output-format json` we read.
type response struct {
	Type             string         `json:"type"`
	IsError          bool           `json:"is_error"`
	Result           string         `json:"result"`
	StructuredOutput jsontext.Value `json:"structured_output"`
	Subtype          string         `json:"subtype"`
	TerminalReason   string         `json:"terminal_reason"`
}

var reAuth = regexp.MustCompile(`(?i)not logged in|please (?:log ?in|run /login)|invalid api key|authentication|unauthori[sz]ed|OAuth token`)

// DraftCommit implements ai.Drafter.
func (a *Adapter) DraftCommit(ctx context.Context, in ai.CommitInput) (ai.Commit, error) {
	in.ExtraPrompt = joinExtra(in.ExtraPrompt, a.cfg.ExtraPrompt)
	instruction, stdin := ai.CommitPrompt(in)
	var out ai.Commit
	err := a.draft(ctx, instruction, stdin, ai.CommitSchema, &out, func() error { return ai.ValidateCommit(out) })
	return out, err
}

// DraftPR implements ai.Drafter.
func (a *Adapter) DraftPR(ctx context.Context, in ai.PRInput) (ai.PullRequest, error) {
	in.ExtraPrompt = joinExtra(in.ExtraPrompt, a.cfg.ExtraPrompt)
	instruction, stdin := ai.PRPrompt(in)
	var out ai.PullRequest
	err := a.draft(ctx, instruction, stdin, ai.PRSchema, &out, func() error { return ai.ValidatePR(out) })
	return out, err
}

func joinExtra(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(p))
		}
	}
	return strings.Join(nonEmpty, "\n")
}

// draft calls claude once, retrying a single time on malformed output.
func (a *Adapter) draft(ctx context.Context, instruction, stdin, schema string, out any, validate func() error) error {
	if os.Getenv(NestedSessionEnv) != "" {
		a.log.Debug("running inside a Claude Code session; calling claude -p anyway")
	}
	var lastErr error
	for attempt := range 2 {
		raw, err := a.call(ctx, instruction, stdin, schema)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, out); err != nil {
			lastErr = fmt.Errorf("claude returned malformed JSON: %w", err)
		} else if err := validate(); err != nil {
			lastErr = fmt.Errorf("claude returned an invalid draft: %w", err)
		} else {
			return nil
		}
		a.log.Debug("ai draft rejected", "attempt", attempt+1, "err", lastErr)
	}
	return stack.New(stack.KindAPIFailure, "Claude Code did not return a usable draft after two attempts").
		WithSteps("retry, or pass the text yourself (-m / --no-ai)").WithCause(lastErr)
}

func (a *Adapter) call(ctx context.Context, instruction, stdin, schema string) (jsontext.Value, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()

	args := []string{
		"-p", instruction,
		"--model", a.cfg.Model,
		"--tools", "",
		"--output-format", "json",
		"--json-schema", schema,
		"--strict-mcp-config",
		"--no-session-persistence",
		"--permission-prompts", "none",
	}
	res, err := a.run.Run(ctx, exec.Cmd{
		Name:  a.cfg.Command,
		Args:  args,
		Stdin: strings.NewReader(stdin),
		// A nested session must not inherit the outer session's identity.
		Unset: []string{NestedSessionEnv, "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_ENTRYPOINT"},
	})
	if err != nil {
		return nil, a.mapError(err, res)
	}
	var resp response
	if err := json.Unmarshal(res.Stdout, &resp); err != nil {
		return nil, stack.New(stack.KindAPIFailure, "could not parse Claude Code's JSON output").
			WithDetail(tail(res.Out())).WithCause(err)
	}
	if resp.IsError {
		if reAuth.MatchString(resp.Result) {
			return nil, stack.New(stack.KindAuthRequired, "Claude Code is not logged in").
				WithSteps("run `claude` once and complete the login").WithDetail(resp.Result)
		}
		return nil, stack.New(stack.KindAPIFailure, "Claude Code reported an error").
			WithDetail(tail(resp.Result))
	}
	if len(resp.StructuredOutput) == 0 {
		// Older versions only fill "result" with the JSON text.
		return jsontext.Value(resp.Result), nil
	}
	return resp.StructuredOutput, nil
}

func (a *Adapter) mapError(err error, res exec.Result) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return stack.Newf(stack.KindAPIFailure, "Claude Code did not answer within %s", a.cfg.Timeout).
			WithSteps("raise the limit with `git config stack.ai.timeout 2m`, or pass the text yourself (-m / --no-ai)")
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if _, ok := errors.AsType[*exec.NotFoundError](err); ok {
		return stack.Newf(stack.KindNotInstalled, "Claude Code (%s) is not installed", a.cfg.Command).
			WithSteps("install it from https://claude.com/claude-code, or set `git config stack.ai.command <path>`",
				"or drop --ai and pass the text yourself").WithCause(err)
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		detail := ee.Result.Err()
		if detail == "" {
			detail = ee.Result.Out()
		}
		if reAuth.MatchString(detail) {
			return stack.New(stack.KindAuthRequired, "Claude Code is not logged in").
				WithSteps("run `claude` once and complete the login").WithDetail(tail(detail)).WithCause(err)
		}
		return stack.Newf(stack.KindAPIFailure, "%s exited with code %d", a.cfg.Command, ee.Result.ExitCode).
			WithDetail(tail(detail)).WithCause(err)
	}
	_ = res
	return err
}

// detailLimit bounds diagnostic output kept in errors.
const detailLimit = 500

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= detailLimit {
		return s
	}
	return "…" + s[len(s)-detailLimit:]
}
