package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

func TestSpinnerModelStopsAndCancels(t *testing.T) {
	cancelled := false
	m := &spinnerModel{sp: spinner.New(spinner.WithSpinner(spinner.Dot)), message: "working", cancel: func() { cancelled = true }, styles: DefaultStyles()}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24), teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)))
	tm.Send(spinnerStopMsg{})
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*spinnerModel)
	if !fm.done || cancelled {
		t.Errorf("stop should finish without cancelling: %+v cancelled=%v", fm, cancelled)
	}
	if fm.View().Content != "" {
		t.Errorf("final frame must be empty so the next line replaces the spinner, got %q", fm.View().Content)
	}

	cancelled = false
	m = &spinnerModel{sp: spinner.New(spinner.WithSpinner(spinner.Dot)), message: "working", cancel: func() { cancelled = true }, styles: DefaultStyles()}
	tm = teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(spinnerStopMsg{})
	tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	if !cancelled {
		t.Error("ctrl+c should cancel the work")
	}
}

func TestConfirmModel(t *testing.T) {
	cases := []struct {
		key       tea.KeyPressMsg
		def       bool
		want      bool
		cancelled bool
	}{
		{tea.KeyPressMsg{Code: 'y', Text: "y"}, false, true, false},
		{tea.KeyPressMsg{Code: 'n', Text: "n"}, true, false, false},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, true, true, false},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, false, false, false},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, true, true, true},
	}
	for _, tc := range cases {
		m := &confirmModel{question: "Stage all?", value: tc.def, styles: DefaultStyles()}
		tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
		tm.Send(tc.key)
		fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*confirmModel)
		if fm.value != tc.want || fm.cancelled != tc.cancelled {
			t.Errorf("%v (default %v): value=%v cancelled=%v", tc.key, tc.def, fm.value, fm.cancelled)
		}
	}
}

func TestSelectModel(t *testing.T) {
	opts := []string{"draft", "publish"}
	m := &selectModel{question: "Create PRs as", options: opts, styles: DefaultStyles()}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.KeyPressMsg{Code: 'j', Text: "j"})
	tm.Send(tea.KeyPressMsg{Code: 'j', Text: "j"})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*selectModel); fm.cursor != 1 || fm.cancelled {
		t.Errorf("select = %+v", fm)
	}

	m = &selectModel{question: "q", options: opts, styles: DefaultStyles()}
	tm = teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.KeyPressMsg{Code: '2', Text: "2"})
	if fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*selectModel); fm.cursor != 1 {
		t.Errorf("number key = %+v", fm)
	}

	m = &selectModel{question: "q", options: opts, styles: DefaultStyles()}
	tm = teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*selectModel); !fm.cancelled {
		t.Errorf("q should cancel = %+v", fm)
	}
}
