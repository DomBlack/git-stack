package exec_test

import (
	"bytes"
	"context"
	"errors"
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
