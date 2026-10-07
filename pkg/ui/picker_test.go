package ui

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

func newTestPicker(t *testing.T, o PickerOptions) *teatest.TestModel {
	t.Helper()
	if o.Rows == nil {
		o.Rows = sampleRows()
	}
	if o.Now.IsZero() {
		o.Now = now
	}
	if o.Height == 0 {
		o.Height = 20
	}
	m := NewPicker(context.Background(), o)
	return teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(80, 24),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)),
	)
}

// waitForText waits until the rendered output contains text. The output is
// stripped of ANSI sequences first: bubbletea's renderer updates the screen
// incrementally, so a word can arrive split around a cursor or insert-mode
// sequence and a raw byte search would miss it.
func waitForText(t *testing.T, tm *teatest.TestModel, text string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains([]byte(ansi.Strip(string(b))), []byte(text))
	}, teatest.WithDuration(5*time.Second))
}

func finalPicker(t *testing.T, tm *teatest.TestModel) *Picker {
	t.Helper()
	m := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	p, ok := m.(*Picker)
	if !ok {
		t.Fatalf("final model is %T", m)
	}
	return p
}

func TestPickerStartsOnCurrentAndSelectsWithEnter(t *testing.T) {
	tm := newTestPicker(t, PickerOptions{})
	waitForText(t, tm, "feat/ui-tests")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := finalPicker(t, tm).Result(); r.Cancelled || r.Branch != "feat/ui" {
		t.Errorf("result = %+v, want current branch feat/ui", r)
	}
}

func TestPickerNavigationKeys(t *testing.T) {
	tm := newTestPicker(t, PickerOptions{})
	tm.Send(tea.KeyPressMsg{Code: 'j', Text: "j"})
	tm.Send(tea.KeyPressMsg{Code: 'j', Text: "j"})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyUp})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := finalPicker(t, tm).Result(); r.Branch != "feat/ui-tests" {
		t.Errorf("result = %+v, want feat/ui-tests", r)
	}

	tm = newTestPicker(t, PickerOptions{})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnd})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := finalPicker(t, tm).Result(); r.Branch != "scratch" {
		t.Errorf("end: %+v", r)
	}

	tm = newTestPicker(t, PickerOptions{})
	tm.Send(tea.KeyPressMsg{Code: 'k', Text: "k"})
	tm.Send(tea.KeyPressMsg{Code: 'k', Text: "k"})
	tm.Send(tea.KeyPressMsg{Code: 'k', Text: "k"})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := finalPicker(t, tm).Result(); r.Branch != "main" {
		t.Errorf("k clamps at top: %+v", r)
	}
}

func TestPickerFilterKeepsTreeContext(t *testing.T) {
	tm := newTestPicker(t, PickerOptions{})
	tm.Type("hot")
	waitForText(t, tm, "> hot")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	p := finalPicker(t, tm)
	if r := p.Result(); r.Branch != "hotfix" {
		t.Errorf("filtered enter = %+v, want hotfix (cursor jumps to the first real match)", r)
	}
	names := make([]string, 0, len(p.visible))
	for _, r := range p.visible {
		names = append(names, r.Row.Name)
	}
	if len(names) != 2 || names[0] != "release" || names[1] != "hotfix" {
		t.Errorf("visible rows after filter = %v, want the match plus its trunk", names)
	}

	// While a filter is active, j/k are typed into the filter, not navigation.
	tm = newTestPicker(t, PickerOptions{})
	tm.Type("fj")
	waitForText(t, tm, "no branches match")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := finalPicker(t, tm).Result(); !r.Cancelled {
		t.Errorf("enter with no matches should cancel: %+v", r)
	}
}

func TestPickerCancel(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		tm := newTestPicker(t, PickerOptions{})
		tm.Send(key)
		if r := finalPicker(t, tm).Result(); !r.Cancelled {
			t.Errorf("%v should cancel: %+v", key, r)
		}
	}
}

func TestPickerBackgroundRefreshUpdatesRows(t *testing.T) {
	refreshed := make(chan struct{})
	refresh := func(context.Context) ([]forge.PullRequest, error) {
		<-refreshed
		return []forge.PullRequest{{Number: 99, Head: "feat/ui-tests", State: forge.StateOpen}}, nil
	}
	tm := newTestPicker(t, PickerOptions{Refresh: refresh})
	waitForText(t, tm, "refreshing pull requests")
	close(refreshed)
	waitForText(t, tm, "#99 open")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	finalPicker(t, tm)

	tm = newTestPicker(t, PickerOptions{Refresh: func(context.Context) ([]forge.PullRequest, error) { return nil, errors.New("offline") }})
	waitForText(t, tm, "could not refresh")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	finalPicker(t, tm)
}

func TestPickerGolden(t *testing.T) {
	tm := newTestPicker(t, PickerOptions{Height: 6})
	for range 4 {
		tm.Send(tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	// Send handles messages in order, so FinalModel below waits for all
	// four moves. Do not wait for "7/8" in raw output: the renderer can
	// leave "8" on screen and only emit "7/".
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	p := finalPicker(t, tm)
	// Snapshot the last frame as plain text: the inline program clears its
	// view on exit, so the raw output stream is not a useful golden.
	teatest.RequireEqualOutput(t, []byte(ansi.Strip(p.View().Content)))
}

// The picker's PR references are links, and they survive bubbletea's cell
// renderer with their id, so the terminal sees one link per PR however
// the frame is redrawn.
func TestPickerLinksSurviveTheRenderer(t *testing.T) {
	url := "https://github.com/o/r/pull/12"
	rows := []app.Row{{Name: "main", IsTrunk: true, Tracked: true}, {Name: "feat-a", Depth: 1, Tracked: true, IsCurrent: true,
		PR: &forge.PullRequest{Number: 12, URL: url}}}
	for _, links := range []bool{true, false} {
		m := NewPicker(context.Background(), PickerOptions{Rows: rows, Links: links, Now: now, Height: 5})
		if got := strings.Contains(m.View().Content, hyperlinkOpen(url)); got != links {
			t.Errorf("Links %v: view linked = %v", links, got)
		}
		var out bytes.Buffer
		p := tea.NewProgram(m, tea.WithInput(strings.NewReader("")), tea.WithOutput(&out),
			tea.WithColorProfile(colorprofile.TrueColor), tea.WithWindowSize(100, 24))
		go func() { time.Sleep(200 * time.Millisecond); p.Quit() }()
		if _, err := p.Run(); err != nil {
			t.Fatal(err)
		}
		// The renderer re-emits the link itself, BEL terminated, keeping
		// our id.
		emitted := ansi.SetHyperlink(url, "id="+linkID(url)) + "#12"
		if got := strings.Contains(out.String(), emitted); got != links {
			t.Errorf("Links %v: rendered linked = %v: %q", links, got, out.String())
		}
		// The link cells keep their underline, and it stops with the link.
		if links && !regexp.MustCompile(`\x1b\[[0-9;]*\b4m`+regexp.QuoteMeta(emitted)+`\x1b\[(m|24m)`).MatchString(out.String()) {
			t.Errorf("rendered link should be underlined and nothing after it: %q", out.String())
		}
	}
}

// Ctrl-C in the picker interrupts the command (context.Canceled, so exit
// 130 and an idle status); Escape or q only closes the picker.
func TestPickerCtrlCIsAnInterrupt(t *testing.T) {
	for _, tc := range []struct {
		name, keys      string
		wantInterrupted bool
	}{
		{"ctrl+c", "\x03", true},
		{"q", "q", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := RunPicker(context.Background(), strings.NewReader(tc.keys), &bytes.Buffer{}, PickerOptions{Rows: sampleRows(), Now: now, Height: 5})
			if !res.Cancelled || res.Interrupted != tc.wantInterrupted {
				t.Errorf("result = %+v", res)
			}
			if got := errors.Is(err, context.Canceled); got != tc.wantInterrupted {
				t.Errorf("err = %v, want an interrupt %v", err, tc.wantInterrupted)
			}
		})
	}
}

func TestPromptCtrlCIsAnInterrupt(t *testing.T) {
	for _, tc := range []struct {
		name, keys      string
		wantInterrupted bool
	}{
		{"ctrl+c", "\x03", true},
		{"q", "q", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Prompter{In: strings.NewReader(tc.keys), Out: &bytes.Buffer{}}
			_, err := p.Confirm("Delete it?", true)
			if !errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) != tc.wantInterrupted {
				t.Errorf("Confirm err = %v", err)
			}
			p.In = strings.NewReader(tc.keys)
			_, err = p.Select("Which?", []string{"a", "b"})
			if !errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) != tc.wantInterrupted {
				t.Errorf("Select err = %v", err)
			}
		})
	}
}
