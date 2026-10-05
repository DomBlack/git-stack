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
