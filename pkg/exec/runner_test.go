package exec_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/exec"
)

func TestRunCapturesOutputAndStdin(t *testing.T) {
	r := exec.New()
	res, err := r.Run(context.Background(), exec.Cmd{
		Name:  "sh",
		Args:  []string{"-c", "cat; echo out; echo err >&2"},
		Stdin: strings.NewReader("in\n"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := res.Out(), "in\nout"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := res.Err(), "err"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestRunNonZeroExitIsExitError(t *testing.T) {
	r := exec.New()
	res, err := r.Run(context.Background(), exec.Cmd{
		Name: "sh",
		Args: []string{"-c", "echo boom >&2; exit 3"},
	})
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		t.Fatalf("err = %v (%T), want *exec.ExitError", err, err)
	}
	if ee.Result.ExitCode != 3 || res.ExitCode != 3 {
		t.Errorf("exit code = %d/%d, want 3", ee.Result.ExitCode, res.ExitCode)
	}
	if !strings.Contains(ee.Error(), "code 3") || !strings.Contains(ee.Error(), "boom") {
		t.Errorf("Error() = %q, want code and stderr tail", ee.Error())
	}
}

func TestRunMissingExecutable(t *testing.T) {
	_, err := exec.New().Run(context.Background(), exec.Cmd{Name: "git-stack-definitely-missing-binary"})
	if _, ok := errors.AsType[*exec.NotFoundError](err); !ok {
		t.Fatalf("err = %v (%T), want *exec.NotFoundError", err, err)
	}
}

func TestRunHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := exec.New().Run(ctx, exec.Cmd{Name: "sh", Args: []string{"-c", "sleep 5"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("Run did not return promptly after cancellation")
	}
}

func TestPassthroughRequiresTTY(t *testing.T) {
	_, err := exec.New().Run(context.Background(), exec.Cmd{Name: "true", Mode: exec.Passthrough})
	if !errors.Is(err, exec.ErrTTYUnavailable) {
		t.Fatalf("err = %v, want ErrTTYUnavailable", err)
	}
}

func TestDebugLogNeverIncludesStdin(t *testing.T) {
	var log bytes.Buffer
	r := exec.New(exec.WithDebug(&log))
	_, err := r.Run(context.Background(), exec.Cmd{
		Name:  "sh",
		Args:  []string{"-c", "cat >/dev/null"},
		Dir:   t.TempDir(),
		Stdin: strings.NewReader("SECRET-DIFF"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := log.String()
	if !strings.HasPrefix(s, "[exec] sh -c ") || !strings.Contains(s, "mode=capture") || !strings.Contains(s, "exit=0") {
		t.Errorf("unexpected debug line: %q", s)
	}
	if strings.Contains(s, "SECRET-DIFF") {
		t.Errorf("debug log leaked stdin: %q", s)
	}
}

func TestEnvOverridesAndUnset(t *testing.T) {
	res, err := exec.New().Run(context.Background(), exec.Cmd{
		Name:  "sh",
		Args:  []string{"-c", `echo "$GIT_STACK_A|$GIT_STACK_B|${HOME:-unset}"`},
		Env:   []string{"GIT_STACK_A=1", "GIT_STACK_B=2"},
		Unset: []string{"HOME"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := res.Out(), "1|2|unset"; got != want {
		t.Errorf("env = %q, want %q", got, want)
	}
}

func TestBuildEnv(t *testing.T) {
	got := exec.BuildEnv([]string{"A=1", "B=2", "C=3"}, []string{"B=9"}, []string{"C"})
	want := []string{"A=1", "B=9"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("BuildEnv = %v, want %v", got, want)
	}
}

// hasControllingTerminal reports whether this test process can open
// /dev/tty, which is what ssh-keygen and gpg do to prompt for a passphrase.
func hasControllingTerminal(t *testing.T) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/tty on windows")
	}
	_, err := exec.New(exec.WithTTY(exec.TTY{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})).
		Run(context.Background(), exec.Cmd{Name: "sh", Args: []string{"-c", "exec 3</dev/tty"}})
	return err == nil
}

func TestCaptureWithoutTTYDetachesFromTerminal(t *testing.T) {
	hadTTY := hasControllingTerminal(t)
	// A child of a runner with no TTY must not reach the terminal the
	// process was started from, even when there is one: the MCP server's
	// git would otherwise block on a passphrase prompt nobody can answer.
	res, err := exec.New().Run(context.Background(), exec.Cmd{Name: "sh", Args: []string{"-c", "exec 3</dev/tty"}})
	if err == nil {
		t.Errorf("child of a TTY-less runner opened /dev/tty (test has terminal: %v)", hadTTY)
	} else if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Errorf("err = %v (%T), want *exec.ExitError from the failed open; stderr %q", err, err, res.Err())
	}
	// That check is vacuous where the test itself has no terminal (CI), so
	// also check the mechanism: a child started in its own session leads
	// its own process group, so its pgid is its pid. One that merely
	// inherited ours is in our group instead.
	pgid := func(r exec.Runner) string {
		t.Helper()
		res, err := r.Run(context.Background(), exec.Cmd{Name: "sh", Args: []string{"-c", `[ "$(ps -o pgid= -p $$ | tr -d ' ')" = "$$" ] && echo leader || echo inherited`}})
		if err != nil {
			t.Fatalf("ps: %v: %s", err, res.Err())
		}
		return res.Out()
	}
	if got := pgid(exec.New()); got != "leader" {
		t.Errorf("child of a TTY-less runner is %s, want its own session leader", got)
	}
	if got := pgid(exec.New(exec.WithTTY(exec.TTY{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))); got != "inherited" {
		t.Errorf("child of a TTY runner is %s, want to inherit our process group", got)
	}
}

func TestCaptureWithoutTTYDisablesPrompts(t *testing.T) {
	t.Setenv("GPG_TTY", "/dev/ttys000")
	script := `echo "${GIT_TERMINAL_PROMPT:-unset}|${GPG_TTY:-unset}"`
	res, err := exec.New().Run(context.Background(), exec.Cmd{Name: "sh", Args: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := res.Out(), "0|unset"; got != want {
		t.Errorf("TTY-less env = %q, want %q", got, want)
	}
	// A runner with a terminal leaves prompts alone: the CLI user can answer them.
	r := exec.New(exec.WithTTY(exec.TTY{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
	res, err = r.Run(context.Background(), exec.Cmd{Name: "sh", Args: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := res.Out(), "unset|/dev/ttys000"; got != want {
		t.Errorf("TTY env = %q, want %q", got, want)
	}
}
