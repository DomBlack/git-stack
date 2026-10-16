package cmd

import (
	"bytes"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func TestRenderRestackNothingInScope(t *testing.T) {
	var out, errOut bytes.Buffer
	rep := ui.NewReporter(nil, &out, &errOut, ui.ReporterOptions{})
	renderRestack(rep, app.RestackResult{}, false)
	if out.String() != "ok: Nothing to restack\n" {
		t.Errorf("out = %q", out.String())
	}
}

func TestRenderRestackAborted(t *testing.T) {
	tests := []struct {
		name string
		res  app.RestackResult
		want string
	}{
		{"nothing", app.RestackResult{}, "ok: Restack aborted\n"},
		{"branches", app.RestackResult{Restored: []string{"a", "b"}}, "ok: Restack aborted; put back a, b\n"},
		{"branches and order", app.RestackResult{Restored: []string{"a", "b"}, OrderRestored: true}, "ok: Restack aborted; put back a, b and the stack order\n"},
		{"order only", app.RestackResult{OrderRestored: true}, "ok: Restack aborted; put back the stack order\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			rep := ui.NewReporter(nil, &out, &errOut, ui.ReporterOptions{})
			renderRestack(rep, tt.res, true)
			if out.String() != tt.want {
				t.Errorf("out = %q, want %q", out.String(), tt.want)
			}
		})
	}
}

func TestRenderRestackFinishedAPop(t *testing.T) {
	var out, errOut bytes.Buffer
	rep := ui.NewReporter(nil, &out, &errOut, ui.ReporterOptions{})
	renderRestack(rep, app.RestackResult{Moved: []app.BranchMove{{Name: "c"}}, Popped: "b", PoppedOnto: "a"}, false)
	if want := "ok: Restacked c\nok: Popped b; its changes are staged on a\n"; out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
}
