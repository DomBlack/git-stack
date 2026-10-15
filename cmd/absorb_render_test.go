package cmd

import (
	"bytes"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/ui"
)

func TestRenderAbsorbPlan(t *testing.T) {
	var out, errOut bytes.Buffer
	rep := ui.NewReporter(nil, &out, &errOut, ui.ReporterOptions{})
	renderAbsorbPlan(rep, &app.AbsorbPlan{
		Hunks: []app.AbsorbHunk{
			{Path: "pkg/x.go", OldStart: 120, OldCount: 6, NewCount: 6, Target: &app.AbsorbTarget{SHA: "7ac32ef0123456789", Subject: "Give up after five", Branch: "retries"}},
			{Path: "a\nb.txt", OldStart: 12, OldCount: 1, NewCount: 1},
			{Path: "pkg/x.go", OldStart: 300, OldCount: 0, NewCount: 2, Target: &app.AbsorbTarget{SHA: "4f21abc0123456789", Subject: "Version it", Branch: "schema"}},
		},
		Skipped: []app.AbsorbSkipped{{Path: "docs/new.md", Reason: "new file"}},
	})
	want := "  pkg/x.go:120-125  into 7ac32ef Give up after five (retries)\n" +
		"  \"a\\nb.txt\":12  not absorbed; no commit in this stack changed those lines\n" +
		"  pkg/x.go:after 300  into 4f21abc Version it (schema)\n" +
		"  docs/new.md  skipped; new file\n"
	if out.String() != want {
		t.Errorf("out = %q\nwant %q", out.String(), want)
	}
}

func TestRenderAbsorbResult(t *testing.T) {
	var out, errOut bytes.Buffer
	rep := ui.NewReporter(nil, &out, &errOut, ui.ReporterOptions{})
	renderAbsorbResult(rep, app.AbsorbResult{
		Branch:  "retries",
		Applied: true,
		Plan:    app.AbsorbPlan{Absorbed: 2, Unabsorbed: 1, Commits: 2, Skipped: []app.AbsorbSkipped{{Path: "n", Reason: "new file"}}},
		Rewritten: []app.BranchMove{
			{Name: "schema", From: "9b1d2e00000000000", To: "1"},
			{Name: "retries", From: "7ac32ef0000000000", To: "2"},
		},
		Restacked: []string{"ui"},
	})
	wantOut := "ok: Absorbed 2 hunks into 2 commits\n  schema  rewritten, was 9b1d2e0\n  retries  rewritten, was 7ac32ef\nok: Restacked 1 branch above retries\n"
	if out.String() != wantOut {
		t.Errorf("out = %q\nwant %q", out.String(), wantOut)
	}
	if errOut.String() != "note: Still staged: 1 hunk, 1 file\n" {
		t.Errorf("err = %q", errOut.String())
	}
}
