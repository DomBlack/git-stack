package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/ui"
)

// Whatever way a command ends, the command itself puts the window title
// back, so callers of NewRootCmd get it as well as Execute.
func TestTerminalIsRestoredOnEveryExitPath(t *testing.T) {
	const restore = "\x1b]2;\x1b\\\x1b[23;2t"
	for _, tc := range []struct {
		name string
		body func(ctx context.Context) error
		// panics is true when execute must re-panic after restoring.
		panics bool
		// status is the program status the run ends on.
		status string
	}{
		{"success", func(context.Context) error { return nil }, false, "state=done:app=git-stack\x1b\\"},
		{"error", func(context.Context) error { return errors.New("conflict") }, false, "state=error:app=git-stack:msg=Y29uZmxpY3Q=\x1b\\"},
		{"interrupted", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, false, "state=idle:app=git-stack:msg=SW50ZXJydXB0ZWQ=\x1b\\"},
		{"panic", func(context.Context) error { panic("boom") }, true, "state=error:app=git-stack:msg=cGFuaWM6IGJvb20=\x1b\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errOut bytes.Buffer
			rep := ui.NewReporter(strings.NewReader(""), &bytes.Buffer{}, &errOut, ui.ReporterOptions{ErrTTY: true, TerminalStatus: true})
			c := &cli{streams: testStreams(nil), rt: &Runtime{Report: rep}}
			c.rtOnce.Do(func() {})
			ctx, cancel := context.WithCancel(context.Background())
			root := &cobra.Command{Use: "x", SilenceErrors: true, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
				return rep.Step(cmd.Context(), app.PhaseRestack, "Restacking 2 branches", func(ctx context.Context) error {
					if tc.name == "interrupted" {
						cancel() // what SIGINT does through signal.NotifyContext
					}
					return tc.body(ctx)
				})
			}}
			root.SetArgs(nil)
			c.ownTerminal(root)
			func() {
				defer func() {
					if p := recover(); (p != nil) != tc.panics {
						t.Errorf("panic = %v, want panic %v", p, tc.panics)
					}
				}()
				_ = root.ExecuteContext(ctx)
			}()
			cancel()
			e := errOut.String()
			if !strings.Contains(e, "\x1b]2;git stack: Restacking 2 branches\x1b\\") {
				t.Errorf("the step should have set the title: %q", e)
			}
			if !strings.HasSuffix(e, "\x1b]7501;"+tc.status+restore) || strings.Count(e, "\x1b[23;2t") != 1 {
				t.Errorf("want the status to end %q and the title restored exactly once, last: %q", tc.status, e)
			}
		})
	}
}

// The tree NewRootCmd builds restores the title by itself, with no help
// from Execute.
func TestRootCommandTreeOwnsTerminalCleanup(t *testing.T) {
	var errOut bytes.Buffer
	rep := ui.NewReporter(strings.NewReader(""), &bytes.Buffer{}, &errOut, ui.ReporterOptions{ErrTTY: true, TerminalStatus: true})
	c := &cli{streams: testStreams(nil), rt: &Runtime{Report: rep}}
	c.rtOnce.Do(func() {})
	// A step earlier in the run set the title.
	_ = rep.Step(context.Background(), app.PhaseSync, "Fetching origin", func(context.Context) error { return nil })
	root := newRootCmd(c)
	root.SetArgs([]string{"version"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(errOut.String(), "\x1b]2;\x1b\\\x1b[23;2t") {
		t.Errorf("running a command from NewRootCmd must restore the title: %q", errOut.String())
	}
}

// Ctrl-C in the picker or a prompt exits like every other interrupt.
func TestPickerAndPromptInterruptsExit130(t *testing.T) {
	_, pickErr := ui.RunPicker(context.Background(), strings.NewReader("\x03"), &bytes.Buffer{}, ui.PickerOptions{Height: 3})
	_, promptErr := ui.Prompter{In: strings.NewReader("\x03"), Out: &bytes.Buffer{}}.Confirm("Delete it?", true)
	for _, err := range []error{pickErr, promptErr} {
		if exitCode(err) != 130 {
			t.Errorf("exitCode(%v) = %d, want 130", err, exitCode(err))
		}
	}
	if _, err := (ui.Prompter{In: strings.NewReader("q"), Out: &bytes.Buffer{}}).Confirm("Delete it?", true); exitCode(err) != 1 {
		t.Errorf("dismissing with q is a cancel, not an interrupt: exit %d", exitCode(err))
	}
}
