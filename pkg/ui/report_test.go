package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func newReporter(o ReporterOptions) (*Reporter, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return NewReporter(strings.NewReader(""), &out, &errOut, o), &out, &errOut
}

func TestReporterPipedIsPlainASCII(t *testing.T) {
	r, out, errOut := newReporter(ReporterOptions{})
	r.Success("Checked out %s", r.Branch("feat-a"))
	r.Info("%s  %s created", "feat-a", r.Ref(12, "https://x/12", forge.StateUnknown))
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

func TestReporterErrorListsChecks(t *testing.T) {
	failing := stack.New(stack.KindChecksFailing, "2 pull requests have failing checks; nothing was merged").
		WithSteps("fix them and run git stack submit, or git stack merge --force to merge anyway")
	failing.Checks = []stack.PRChecks{
		{Number: 201, Branch: "auth/api", Failing: []string{"lint", "test (ubuntu-latest)"}},
		{Number: 202, Branch: "auth/core", Pending: []string{"build"}},
		{Number: 203, Branch: "auth/ui", Failing: []string{"test (macos-latest)"}, Pending: []string{"build", "e2e"}},
		{Number: 204, Branch: "auth/\x1bdocs", Failing: []string{"a", "b", "c", "d", "e"}},
	}
	pending := stack.New(stack.KindChecksPending, "1 pull request still has checks running; nothing was merged").
		WithSteps("wait for them to finish and run git stack merge again, or git stack merge --force to merge anyway")
	pending.Checks = []stack.PRChecks{{Number: 204, Branch: "auth/docs", Pending: []string{"build", "test (macos-latest)"}}}

	r, _, errOut := newReporter(ReporterOptions{})
	r.Error(failing)
	r.Error(pending)
	want := `error: 2 pull requests have failing checks; nothing was merged
  #201 auth/api: lint, test (ubuntu-latest)
  #202 auth/core: build still running
  #203 auth/ui: test (macos-latest); build, e2e still running
  #204 auth/\x1bdocs: a, b, c and 2 more
  - fix them and run git stack submit, or git stack merge --force to merge anyway
error: 1 pull request still has checks running; nothing was merged
  #204 auth/docs: build, test (macos-latest)
  - wait for them to finish and run git stack merge again, or git stack merge --force to merge anyway
`
	if errOut.String() != want {
		t.Errorf("stderr =\n%s\nwant\n%s", errOut.String(), want)
	}

	r, _, errOut = newReporter(ReporterOptions{OutTTY: true, ErrTTY: true})
	r.Error(failing)
	r.Error(pending)
	e := ansi.Strip(errOut.String())
	for _, line := range []string{
		markErr + " 2 pull requests have failing checks; nothing was merged\n  #201 auth/api: lint, test (ubuntu-latest)\n",
		"  " + markStep + " fix them and run git stack submit, or git stack merge --force to merge anyway\n",
		markErr + " 1 pull request still has checks running; nothing was merged\n  #204 auth/docs: build, test (macos-latest)\n",
	} {
		if !strings.Contains(e, line) {
			t.Errorf("stderr lacks %q:\n%s", line, e)
		}
	}
	t.Logf("terminal rendering:\n%s", e)
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
	r.Info("see %s", r.Link("https://x"))
	if !strings.Contains(out.String(), Hyperlink(true, "https://x", "https://x")) {
		t.Errorf("links should be hyperlinks on a terminal: %q", out.String())
	}
}

// Links are decided per stream: a PR on a stdout line is linked when stdout
// is a terminal, one in a notice on stderr when stderr is, independently.
func TestReporterLinksFollowEachStreamsTerminal(t *testing.T) {
	resolve := func(n int) string {
		if n == 7 {
			return "https://forge.test/o/r/pull/7"
		}
		return ""
	}
	link7 := Hyperlink(true, "https://forge.test/o/r/pull/7", "#7")
	link12 := hyperlinkOpen("https://x/12")
	for _, tc := range []struct {
		name           string
		outTTY, errTTY bool
	}{
		{"both terminals", true, true},
		{"stdout piped", false, true},
		{"stderr piped", true, false},
		{"both piped", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, out, errOut := newReporter(ReporterOptions{OutTTY: tc.outTTY, ErrTTY: tc.errTTY})
			r.SetPRResolver(resolve)
			r.Info("feat-a  %s created %s", r.Ref(12, "https://x/12", forge.StateUnknown), r.Link("https://x/12"))
			r.Warn("moved the base of #7 and #12; #99 is unknown")
			r.Error(stack.New(stack.KindInvalidArgs, "feat-b's pull request #7 is still a draft"))
			_, _ = r.Stream().Write([]byte("Created PR #7\n"))

			if got := strings.Contains(out.String(), link12); got != tc.outTTY {
				t.Errorf("stdout linked = %v, want %v: %q", got, tc.outTTY, out.String())
			}
			e := errOut.String()
			if got := strings.Count(e, link7); got != map[bool]int{true: 3, false: 0}[tc.errTTY] {
				t.Errorf("stderr has %d links to #7, errTTY %v: %q", got, tc.errTTY, e)
			}
			// #12 was registered by Ref, so the stderr notice links it too.
			if got := strings.Contains(e, link12); got != tc.errTTY {
				t.Errorf("stderr #12 linked = %v, want %v: %q", got, tc.errTTY, e)
			}
			if strings.Contains(e, "]8;id=gs") && !tc.errTTY || strings.Contains(out.String(), "]8;") && !tc.outTTY {
				t.Error("no OSC 8 may reach a stream that isn't a terminal")
			}
			if strings.Contains(e, "#99"+hyperlinkClose) {
				t.Error("a number the resolver doesn't know stays text")
			}
		})
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

// A Ref is coloured by its state when the caller knows it, takes the
// line's colour otherwise, and is underlined only as a link.
func TestReporterRefLooksLikeEveryOtherLink(t *testing.T) {
	url := "https://x/12"
	r, out, _ := newReporter(ReporterOptions{OutTTY: true, ErrTTY: true})
	st := DefaultStyles()
	for _, tc := range []struct {
		state forge.State
		label string
	}{
		{forge.StateUnknown, "#12"},
		{forge.StateMerged, st.PRMerged.Render("#12")},
		{forge.StateOpen, st.PROpen.Render("#12")},
	} {
		out.Reset()
		r.Info("%s", r.Ref(12, url, tc.state))
		want := "  " + Linkify(tc.label, func(int) string { return url }) + "\n"
		if !strings.Contains(want, Hyperlink(true, url, "#12")) || out.String() != want {
			t.Errorf("%q: got %q, want %q", tc.state, out.String(), want)
		}
	}
	p, pout, _ := newReporter(ReporterOptions{})
	p.Info("%s", p.Ref(12, url, forge.StateMerged))
	if pout.String() != "  #12\n" {
		t.Errorf("piped refs are plain: %q", pout.String())
	}
}

func TestReporterPartialAndSHA(t *testing.T) {
	r, out, _ := newReporter(ReporterOptions{})
	r.Partial("Synced, but main was not updated")
	r.Info("main at %s", r.SHA("14ba05688e8bb83932f27db282928a4faf7d7d6a"))
	if want := "note: Synced, but main was not updated\n  main at 14ba056\n"; out.String() != want {
		t.Errorf("piped = %q, want %q", out.String(), want)
	}
	tr, tout, _ := newReporter(ReporterOptions{OutTTY: true, ErrTTY: true, TerminalStatus: true})
	_ = tr.Step(context.Background(), app.PhaseSync, "Updating main", func(context.Context) error { return nil })
	tr.Partial("Synced, but main was not updated")
	if s := ansi.Strip(tout.String()); s != markWarn+" Synced, but main was not updated\n" {
		t.Errorf("terminal = %q", s)
	}
	// The terminal status ends on the partial line, not a stale success.
	var e bytes.Buffer
	tr.err, tr.rawErr = &e, &e
	tr.term.w = &e
	tr.Finish(nil)
	if !strings.Contains(e.String(), programStatus(stateDone, "", "Synced, but main was not updated")) {
		t.Errorf("status = %q", e.String())
	}
}
