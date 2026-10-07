package ui

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestSetTitle(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "git stack: Restacking 2 branches", "\x1b]2;git stack: Restacking 2 branches\x1b\\"},
		{"empty resets", "", "\x1b]2;\x1b\\"},
		{"escapes and controls dropped", "a\x1b[1mb\x1b[m\x07c\nd\u009be", "\x1b]2;abc de\x1b\\"},
		{"long titles cut on a rune", strings.Repeat("é", 150), "\x1b]2;" + strings.Repeat("é", 100) + "\x1b\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := setTitle(tc.in); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
	if titlePush != "\x1b[22;2t" || titlePop != "\x1b[23;2t" {
		t.Errorf("title stack sequences changed: %q %q", titlePush, titlePop)
	}
}

// One push for the whole process, a title per step, and one restore at the
// end however the command ended.
func TestReporterTitleLifetime(t *testing.T) {
	restore := "\x1b]2;\x1b\\" + titlePop
	step := func(r *Reporter, msg string, err error) error {
		return r.Step(context.Background(), app.PhaseRestack, msg, func(context.Context) error { return err })
	}
	for _, tc := range []struct {
		name string
		run  func(r *Reporter)
	}{
		{"success", func(r *Reporter) {
			_ = step(r, "Fetching origin", nil)
			_ = step(r, "Restacking 2 branches", nil)
		}},
		{"error", func(r *Reporter) {
			_ = step(r, "Fetching origin", nil)
			_ = step(r, "Restacking 2 branches", errors.New("conflict"))
		}},
		{"cancelled", func(r *Reporter) {
			ctx, cancel := context.WithCancel(context.Background())
			_ = r.Step(ctx, app.PhaseSync, "Fetching origin", func(context.Context) error { cancel(); return ctx.Err() })
			_ = step(r, "Restacking 2 branches", nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, errOut := newReporter(ReporterOptions{ErrTTY: true, TerminalStatus: true})
			tc.run(r)
			r.Finish(nil)
			r.Finish(nil) // idempotent
			e := errOut.String()
			if strings.Count(e, titlePush) != 1 || strings.Count(e, titlePop) != 1 {
				t.Errorf("want exactly one push and one pop: %q", e)
			}
			if !strings.Contains(e, setTitle("git stack: Fetching origin")) || !strings.Contains(e, setTitle("git stack: Restacking 2 branches")) {
				t.Errorf("each step should set the title: %q", e)
			}
			if strings.Index(e, titlePush) > strings.Index(e, "\x1b]2;git stack") {
				t.Errorf("the push must come before the first title: %q", e)
			}
			if !strings.HasSuffix(e, restore) {
				t.Errorf("the title must be restored last: %q", e)
			}
			// Nothing after Finish.
			_ = step(r, "late", nil)
			if strings.Contains(errOut.String(), setTitle("git stack: late")) {
				t.Error("no title may be set after Finish")
			}
		})
	}
}

func TestReporterTitleOnlyOnATerminal(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    ReporterOptions
	}{
		{"piped stderr", ReporterOptions{TerminalStatus: true}},
		{"quiet", ReporterOptions{ErrTTY: true, Quiet: true, TerminalStatus: true}},
		{"switched off", ReporterOptions{ErrTTY: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, errOut := newReporter(tc.o)
			_ = r.Step(context.Background(), app.PhaseSync, "Fetching origin", func(context.Context) error { return nil })
			r.Finish(nil)
			if strings.Contains(errOut.String(), "\x1b]2;") || strings.Contains(errOut.String(), "\x1b[2") || strings.Contains(errOut.String(), "7501") {
				t.Errorf("no title sequences expected: %q", errOut.String())
			}
		})
	}
	// A command with no steps leaves the title alone entirely.
	r, _, errOut := newReporter(ReporterOptions{ErrTTY: true, TerminalStatus: true})
	r.Success("Checked out feat-a")
	r.Finish(nil)
	if strings.Contains(errOut.String(), "\x1b]2;") || strings.Contains(errOut.String(), titlePop) {
		t.Errorf("no step, no title: %q", errOut.String())
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestProgramStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     programState
		kind, msg string
		want      string
	}{
		{"working", stateWorking, "", "Restacking 2 branches",
			"\x1b]7501;state=working:app=git-stack:msg=" + b64("Restacking 2 branches") + "\x1b\\"},
		{"blocked question", stateBlocked, "question", "Delete 2 branches?",
			"\x1b]7501;state=blocked:app=git-stack:kind=question:msg=" + b64("Delete 2 branches?") + "\x1b\\"},
		{"kind only goes with blocked", stateDone, "question", "Synced",
			"\x1b]7501;state=done:app=git-stack:msg=" + b64("Synced") + "\x1b\\"},
		{"no msg", stateIdle, "", "", "\x1b]7501;state=idle:app=git-stack\x1b\\"},
		{"clear carries nothing else", stateClear, "", "ignored", "\x1b]7501;state=clear\x1b\\"},
		{"msg is one clean line", stateError, "", "\x1b[31mboom\x1b[m\nsecond line\x07",
			"\x1b]7501;state=error:app=git-stack:msg=" + b64("boom second line") + "\x1b\\"},
		{"msg capped at 2048 bytes", stateWorking, "", strings.Repeat("x", 3000),
			"\x1b]7501;state=working:app=git-stack:msg=" + b64(strings.Repeat("x", 2048)) + "\x1b\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := programStatus(tc.state, tc.kind, tc.msg)
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
			if len(got) > 4096 {
				t.Errorf("report is %d bytes, over the protocol's 4096", len(got))
			}
		})
	}
	if got, want := tmuxPassthrough("\x1b]7501;state=idle\x1b\\"), "\x1bPtmux;\x1b\x1b]7501;state=idle\x1b\x1b\\\x1b\\"; got != want {
		t.Errorf("tmux passthrough = %q, want %q", got, want)
	}
}

// The program status record follows the command: working per step,
// blocked while a prompt waits, and done, error or idle at the end.
func TestReporterProgramStatusLifetime(t *testing.T) {
	ps := programStatus
	ok := func(context.Context) error { return nil }
	for _, tc := range []struct {
		name string
		run  func(r *Reporter) error
		want []string // reports in order
	}{
		{"success leaves done with the result", func(r *Reporter) error {
			_ = r.Step(context.Background(), app.PhaseSync, "Fetching origin", ok)
			r.Success("Synced: 1 restacked")
			return nil
		}, []string{ps(stateWorking, "", "Fetching origin"), ps(stateDone, "", "Synced: 1 restacked")}},
		{"a conflict leaves error", func(r *Reporter) error {
			err := stack.New(stack.KindConflict, "restack stopped on feat-b").WithDetail("CONFLICT a.go")
			return r.Step(context.Background(), app.PhaseRestack, "Restacking 2 branches", func(context.Context) error { return err })
		}, []string{ps(stateWorking, "", "Restacking 2 branches"), ps(stateError, "", "restack stopped on feat-b")}},
		{"interrupted leaves idle", func(r *Reporter) error {
			ctx, cancel := context.WithCancel(context.Background())
			return r.Step(ctx, app.PhaseSync, "Fetching origin", func(ctx context.Context) error { cancel(); return ctx.Err() })
		}, []string{ps(stateWorking, "", "Fetching origin"), ps(stateIdle, "", "Interrupted")}},
		{"a prompt in a step blocks then goes back to working", func(r *Reporter) error {
			return r.Step(context.Background(), app.PhaseSync, "Cleaning up merged branches", func(context.Context) error {
				r.Waiting("Delete 2 branches?")()
				return nil
			})
		}, []string{
			ps(stateWorking, "", "Cleaning up merged branches"),
			ps(stateBlocked, "question", "Delete 2 branches?"),
			ps(stateWorking, "", "Cleaning up merged branches"),
			ps(stateDone, "", ""),
		}},
		{"a picker interrupted with ctrl+c is idle", func(r *Reporter) error {
			r.Waiting("Select a branch")()
			return context.Canceled
		}, []string{ps(stateBlocked, "question", "Select a branch"), ps(stateIdle, "", "Interrupted")}},
		{"only a picker is cleared", func(r *Reporter) error {
			r.Waiting("Select a branch")()
			r.Success("Checked out feat-a")
			return nil
		}, []string{ps(stateBlocked, "question", "Select a branch"), ps(stateClear, "", "")}},
		{"nothing slow, nothing sent", func(r *Reporter) error {
			r.Success("Checked out feat-a")
			return nil
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, errOut := newReporter(ReporterOptions{ErrTTY: true, TerminalStatus: true})
			r.Finish(tc.run(r))
			r.Finish(errors.New("again")) // only the first Finish counts
			var got []string
			for _, part := range strings.Split(errOut.String(), "\x1b]7501;")[1:] {
				body, _, _ := strings.Cut(part, "\x1b\\")
				got = append(got, "\x1b]7501;"+body+"\x1b\\")
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("reports:\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// Under tmux the status goes through passthrough; the title and the
// progress pulse don't, tmux understands those itself.
func TestReporterProgramStatusUnderTmux(t *testing.T) {
	r, _, errOut := newReporter(ReporterOptions{ErrTTY: true, TerminalStatus: true, Tmux: true})
	_ = r.Step(context.Background(), app.PhaseSync, "Fetching origin", func(context.Context) error { return nil })
	r.Finish(nil)
	e := errOut.String()
	if !strings.Contains(e, tmuxPassthrough(programStatus(stateWorking, "", "Fetching origin"))) ||
		!strings.Contains(e, tmuxPassthrough(programStatus(stateDone, "", ""))) {
		t.Errorf("status should be wrapped for tmux: %q", e)
	}
	for _, bare := range []string{setTitle("git stack: Fetching origin"), progressIndeterminate, setTitle("") + titlePop} {
		if !strings.Contains(e, bare) || strings.Contains(e, tmuxPassthrough(bare)) {
			t.Errorf("%q should be sent bare: %q", bare, e)
		}
	}
	if strings.Count(e, "\x1b]7501;") != strings.Count(e, "\x1b\x1b]7501;") {
		t.Errorf("no unwrapped 7501 under tmux: %q", e)
	}
}
