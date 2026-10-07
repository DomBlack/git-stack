package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
)

func TestSetTitle(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "git stack: Restacking 2 branches", "\x1b]2;git stack: Restacking 2 branches\x1b\\"},
		{"empty resets", "", "\x1b]2;\x1b\\"},
		{"escapes and controls dropped", "a\x1b[1mb\x1b[m\x07c\nd\u009be", "\x1b]2;abcde\x1b\\"},
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
			r.Finish()
			r.Finish() // idempotent
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
			r.Finish()
			if strings.Contains(errOut.String(), "\x1b]2;") || strings.Contains(errOut.String(), "\x1b[2") {
				t.Errorf("no title sequences expected: %q", errOut.String())
			}
		})
	}
	// A command with no steps leaves the title alone entirely.
	r, _, errOut := newReporter(ReporterOptions{ErrTTY: true, TerminalStatus: true})
	r.Success("Checked out feat-a")
	r.Finish()
	if strings.Contains(errOut.String(), "\x1b]2;") || strings.Contains(errOut.String(), titlePop) {
		t.Errorf("no step, no title: %q", errOut.String())
	}
}
