package ui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

func newTestReorder(t *testing.T) *teatest.TestModel {
	t.Helper()
	m := NewReorder(ReorderOptions{Trunk: "main", Branches: []string{"a", "b", "c"}, Current: "b", PRs: map[string]int{"a": 12}})
	return teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(80, 24),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)),
	)
}

func finalReorder(t *testing.T, tm *teatest.TestModel) *Reorder {
	t.Helper()
	m := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	r, ok := m.(*Reorder)
	if !ok {
		t.Fatalf("final model is %T", m)
	}
	return r
}

func TestReorderStartsOnCurrentTopFirst(t *testing.T) {
	tm := newTestReorder(t)
	waitForText(t, tm, "main")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	r := finalReorder(t, tm)
	if res := r.Result(); res.Cancelled || !slices.Equal(res.Order, []string{"a", "b", "c"}) {
		t.Errorf("result = %+v", res)
	}
	out := ansi.Strip(r.View().Content)
	// Rows are found by their line, since the letters also occur in the
	// title and help text.
	line := func(prefix string) int {
		for i, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimLeft(l, "❯ "), prefix) {
				return i
			}
		}
		return -1
	}
	c, b, a, trunk := line("c"), line("b"), line("a"), line("main")
	if c < 0 || b < 0 || a < 0 || c >= b || b >= a || a >= trunk || !strings.Contains(out, "#12") || !strings.Contains(out, "❯ b") {
		t.Errorf("screen:\n%s", out)
	}
}

func TestReorderMovesBranchUpAndDown(t *testing.T) {
	tm := newTestReorder(t)
	waitForText(t, tm, "main")
	tm.Send(tea.KeyPressMsg{Code: 'K', Text: "K"}) // b above c
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := finalReorder(t, tm).Result(); !slices.Equal(r.Order, []string{"a", "c", "b"}) {
		t.Errorf("K: %+v", r)
	}
	tm = newTestReorder(t)
	waitForText(t, tm, "main")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}) // b below a
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}) // clamps at the bottom
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := finalReorder(t, tm).Result(); !slices.Equal(r.Order, []string{"b", "a", "c"}) {
		t.Errorf("shift+down: %+v", r)
	}
}

func TestReorderCancelAndInterrupt(t *testing.T) {
	tm := newTestReorder(t)
	waitForText(t, tm, "main")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if r := finalReorder(t, tm).Result(); !r.Cancelled || r.Interrupted {
		t.Errorf("esc: %+v", r)
	}
	_, err := RunReorder(context.Background(), strings.NewReader("\x03"), &strings.Builder{}, ReorderOptions{Trunk: "main", Branches: []string{"a"}})
	if err == nil {
		t.Error("ctrl+c should interrupt")
	}
}

func TestReorderGolden(t *testing.T) {
	tm := newTestReorder(t)
	waitForText(t, tm, "main")
	tm.Send(tea.KeyPressMsg{Code: 'K', Text: "K"})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	r := finalReorder(t, tm)
	teatest.RequireEqualOutput(t, []byte(ansi.Strip(r.View().Content)))
}
