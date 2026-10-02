package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func newReporter(o ReporterOptions) (*Reporter, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return NewReporter(strings.NewReader(""), &out, &errOut, o), &out, &errOut
}

func TestReporterPipedIsPlainASCII(t *testing.T) {
	r, out, errOut := newReporter(ReporterOptions{})
	r.Success("Checked out %s", r.Branch("feat-a"))
	r.Info("%s  %s created", "feat-a", r.Ref(12, "https://x/12"))
	r.Warn("gh stack submits the whole stack")
	r.Error(stack.New(stack.KindNotAtTop, "feat-c is not the top").WithDetail("d1\nd2").WithSteps("run git stack top"))
	r.Error(errors.New("plain"))
	r.Error(context.Canceled)
	if out.String() != "ok: Checked out feat-a\n  feat-a  #12 created\n" {
		t.Errorf("stdout = %q", out.String())
	}
	want := "note: gh stack submits the whole stack\nerror: feat-c is not the top\n  d1\n  d2\n  - run git stack top\nerror: plain\ninterrupted\n"
	if errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	if r.Link("https://x") != "https://x" {
		t.Error("no hyperlinks when piped")
	}
}

func TestReporterTerminalUsesMarks(t *testing.T) {
	r, out, errOut := newReporter(ReporterOptions{OutTTY: true, ErrTTY: true})
	r.Success("Synced")
	r.Warn("careful")
	r.Error(stack.New(stack.KindConflict, "boom").WithSteps("fix it"))
	// Styles add colour; compare the words and marks with the ANSI stripped.
	if o := ansi.Strip(out.String()); !strings.Contains(o, markOK+" Synced") {
		t.Errorf("stdout = %q", o)
	}
	e := ansi.Strip(errOut.String())
	if !strings.Contains(e, markWarn+" careful") || !strings.Contains(e, markErr+" boom") || !strings.Contains(e, markStep+" fix it") {
		t.Errorf("stderr = %q", e)
	}
	if strings.Contains(e, "note:") || strings.Contains(e, "error:") {
		t.Errorf("ASCII prefixes must not appear on a terminal: %q", e)
	}
	if !strings.Contains(r.Link("https://x"), "\x1b]8;;https://x") {
		t.Error("links should be hyperlinks on a terminal")
	}
}

func TestReporterQuietKeepsErrorsOnly(t *testing.T) {
	r, out, errOut := newReporter(ReporterOptions{Quiet: true})
	r.Success("x")
	r.Info("y")
	r.Warn("z")
	err := r.Step(context.Background(), app.PhaseSync, "Syncing", func(context.Context) error { return nil })
	if err != nil || out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("quiet should print nothing: out=%q err=%q", out.String(), errOut.String())
	}
	r.Error(errors.New("still shown"))
	if errOut.String() != "error: still shown\n" {
		t.Errorf("errors must survive --quiet: %q", errOut.String())
	}
}

func TestReporterStepWithoutSpinnerPrintsHeadlineOnce(t *testing.T) {
	r, _, errOut := newReporter(ReporterOptions{})
	calls := 0
	err := r.Step(context.Background(), app.PhaseSubmit, "Submitting stack", func(ctx context.Context) error {
		calls++
		_, _ = r.Stream().Write([]byte("Pushing to origin...\n✓ Created"))
		_, _ = r.Stream().Write([]byte(" PR #1\n"))
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("Step = %v, calls = %d", err, calls)
	}
	want := "Submitting stack...\n  Pushing to origin...\n  ✓ Created PR #1\n"
	if errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	if strings.Contains(errOut.String(), "🚀") {
		t.Error("no emoji when piped")
	}
}

func TestReporterStepOnTerminalWithoutSpinnersHasEmojiAndGutter(t *testing.T) {
	r, _, errOut := newReporter(ReporterOptions{ErrTTY: true})
	_ = r.Step(context.Background(), app.PhaseSubmit, "Submitting stack", func(context.Context) error {
		_, _ = r.Stream().Write([]byte("line without newline"))
		return nil
	})
	e := errOut.String()
	// The busy state is set first, then the emoji headline.
	if !strings.Contains(e, "🚀 Submitting stack...\n") || strings.Index(e, "\x1b]9;4;3") > strings.Index(e, "🚀") {
		t.Errorf("headline = %q", e)
	}
	if !strings.Contains(e, markPipe) || !strings.Contains(e, "line without newline") {
		t.Errorf("partial last line must be flushed in the gutter: %q", e)
	}
	// Busy state wraps the step.
	if !strings.Contains(e, "\x1b]9;4;3\x1b\\") || !strings.HasSuffix(e, "\x1b]9;4;0\x1b\\") {
		t.Errorf("busy state missing or not cleared: %q", e)
	}
}

func TestReporterStepReturnsErrors(t *testing.T) {
	r, _, _ := newReporter(ReporterOptions{})
	want := errors.New("nope")
	if err := r.Step(context.Background(), app.PhaseRestack, "Restacking", func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Errorf("Step should return fn's error, got %v", err)
	}
}
