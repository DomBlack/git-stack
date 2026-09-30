// Package exec runs subprocesses. Every subprocess git-stack starts goes
// through a Runner so that output capture, TTY hand-off, cancellation and
// debug logging are handled in exactly one place.
//
// Capture mode is the default: stdout and stderr are buffered and stdin is
// whatever the caller supplies (never the parent's stdin). Passthrough mode
// attaches the real terminal and is only available to runners built with
// WithTTY; the MCP server never constructs such a runner.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"slices"
	"strings"
	"time"
)

// Mode selects how a subprocess's standard streams are wired.
type Mode int

const (
	// Capture buffers stdout and stderr (default).
	Capture Mode = iota
	// Passthrough attaches the runner's TTY to stdin, stdout and stderr.
	Passthrough
)

func (m Mode) String() string {
	switch m {
	case Capture:
		return "capture"
	case Passthrough:
		return "passthrough"
	default:
		return fmt.Sprintf("Mode(%d)", int(m))
	}
}

// Cmd describes a subprocess to run.
type Cmd struct {
	Name string
	Args []string
	// Dir is the working directory; empty means the current directory.
	Dir string
	// Env holds KEY=VALUE pairs appended to the parent environment.
	Env []string
	// Unset lists environment keys removed before Env is applied.
	Unset []string
	// Stdin is fed to the process in Capture mode. Nil means no input.
	Stdin io.Reader
	Mode  Mode
}

// String renders the command line for logs and error messages.
func (c Cmd) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

// Result is the outcome of a finished subprocess.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
}

// Out returns stdout with surrounding whitespace trimmed.
func (r Result) Out() string { return strings.TrimSpace(string(r.Stdout)) }

// Err returns stderr with surrounding whitespace trimmed.
func (r Result) Err() string { return strings.TrimSpace(string(r.Stderr)) }

// Runner runs subprocesses.
type Runner interface {
	// Run executes c and waits for it. A non-zero exit status is returned as
	// *ExitError (the Result is still populated). A missing executable is
	// returned as *NotFoundError. Context cancellation is returned as the
	// context's error.
	Run(ctx context.Context, c Cmd) (Result, error)
}

// ErrTTYUnavailable is returned when Passthrough mode is requested from a
// runner that has no terminal attached.
var ErrTTYUnavailable = errors.New("exec: passthrough mode requested but no terminal is attached")

// ExitError reports a non-zero exit status.
type ExitError struct {
	Cmd    Cmd
	Result Result
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s exited with code %d", e.Cmd.Name, e.Result.ExitCode)
	if s := e.Result.Err(); s != "" {
		msg += ": " + lastLine(s)
	}
	return msg
}

// NotFoundError reports that the executable is not on PATH.
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s: executable not found in PATH", e.Name)
}

// TTY holds the terminal files used for Passthrough mode.
type TTY struct {
	In, Out, Err *os.File
}

// Option configures a Runner.
type Option func(*runner)

// WithDebug logs every command (name, arguments, directory, mode, exit code
// and duration) to w. Stdin contents are never logged.
func WithDebug(w io.Writer) Option {
	return func(r *runner) { r.debug = w }
}

// WithTTY enables Passthrough mode using the given terminal files.
func WithTTY(t TTY) Option {
	return func(r *runner) { r.tty = &t }
}

// waitDelay bounds how long Run waits for a process to exit after its
// context is cancelled before killing it.
const waitDelay = 2 * time.Second

type runner struct {
	debug io.Writer
	tty   *TTY
}

// New returns a Runner backed by os/exec.
func New(opts ...Option) Runner {
	r := &runner{}
	for _, o := range opts {
		o(r)
	}
	return r
}

func (r *runner) Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Mode == Passthrough && r.tty == nil {
		return Result{}, ErrTTYUnavailable
	}

	cmd := osexec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = BuildEnv(os.Environ(), c.Env, c.Unset)
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error {
		// Give the child a chance to clean up (e.g. git rebase state) before
		// WaitDelay forces a kill.
		return cmd.Process.Signal(os.Interrupt)
	}

	var stdout, stderr bytes.Buffer
	switch c.Mode {
	case Passthrough:
		cmd.Stdin, cmd.Stdout, cmd.Stderr = r.tty.In, r.tty.Out, r.tty.Err
	default:
		cmd.Stdin = c.Stdin
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	start := time.Now()
	runErr := cmd.Run()
	res := Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: time.Since(start),
	}

	var err error
	switch {
	case runErr == nil:
	case ctx.Err() != nil:
		res.ExitCode = -1
		err = ctx.Err()
	default:
		var exitErr *osexec.ExitError
		switch {
		case errors.As(runErr, &exitErr):
			res.ExitCode = exitErr.ExitCode()
			err = &ExitError{Cmd: c, Result: res}
		case errors.Is(runErr, osexec.ErrNotFound):
			res.ExitCode = -1
			err = &NotFoundError{Name: c.Name}
		default:
			res.ExitCode = -1
			err = fmt.Errorf("exec %s: %w", c.Name, runErr)
		}
	}

	r.log(c, res, err)
	return res, err
}

func (r *runner) log(c Cmd, res Result, err error) {
	if r.debug == nil {
		return
	}
	status := fmt.Sprintf("exit=%d", res.ExitCode)
	if err != nil && res.ExitCode == -1 {
		status = "error=" + err.Error()
	}
	dir := c.Dir
	if dir == "" {
		dir = "."
	}
	fmt.Fprintf(r.debug, "[exec] %s (dir=%s mode=%s) %s in %s\n",
		c, dir, c.Mode, status, res.Duration.Round(time.Millisecond))
}

// BuildEnv returns base with the keys in unset removed and extra appended.
// Later entries win when git or the OS reads the environment, so extra
// effectively overrides base.
func BuildEnv(base, extra, unset []string) []string {
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if slices.Contains(unset, key) {
			continue
		}
		if slices.ContainsFunc(extra, func(e string) bool { k, _, _ := strings.Cut(e, "="); return k == key }) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, extra...)
}

func lastLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}
