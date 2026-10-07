package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

func TestRenderLogPlain(t *testing.T) {
	// Zero Styles render without escape codes; Links off.
	out := RenderLog(&app.View{Rows: sampleRows()}, LogOptions{Now: now})
	want := strings.Join([]string{
		"○ feat/ui-tests",
		"│  no PR · needs restack · 1w ago",
		"│",
		"● feat/ui",
		"│  #13 draft · 2d ago",
		"│",
		"○ feat/api",
		"│  #12 open · 3h ago",
		"│",
		"│ ○ fix/typo",
		"│ │  #3 merged · 1mo ago · in /tmp/wt",
		"│ │",
		"├─┘",
		"■ main  30m ago",
		"",
		"○ hotfix",
		"│  #7 closed",
		"│",
		"■ release",
		"",
	}, "\n")
	if out != want {
		t.Errorf("RenderLog =\n%s\nwant\n%s", out, want)
	}
}

func TestRenderLogThreeStacksJoin(t *testing.T) {
	rows := []app.Row{
		{Name: "main", IsTrunk: true, Tracked: true},
		{Name: "a", Depth: 1, Tracked: true},
		{Name: "b", Depth: 1, Tracked: true},
		{Name: "c", Depth: 1, Tracked: true, IsCurrent: true},
	}
	out := RenderLog(&app.View{Rows: rows}, LogOptions{})
	if !strings.Contains(out, "├─┴─┘\n■ main") {
		t.Errorf("three stacks should join with ├─┴─┘:\n%s", out)
	}
	if !strings.Contains(out, "│ │ ● c") {
		t.Errorf("third stack is two columns in and current:\n%s", out)
	}
}

func TestRenderLogTrunkOnly(t *testing.T) {
	rows := []app.Row{{Name: "main", IsTrunk: true, Tracked: true, IsCurrent: true, LastCommit: now.Add(-2 * time.Hour)}}
	if got, want := RenderLog(&app.View{Rows: rows}, LogOptions{Now: now}), "● main  2h ago\n"; got != want {
		t.Errorf("a trunk with no stacks renders just the trunk:\n%q\nwant\n%q", got, want)
	}
	rows = []app.Row{
		{Name: "main", IsTrunk: true, Tracked: true},
		{Name: "release", IsTrunk: true, Tracked: true},
		{Name: "a", Depth: 1, Tracked: true},
	}
	if got, want := RenderLog(&app.View{Rows: rows}, LogOptions{}), "■ main\n\n○ a\n│  no PR\n│\n■ release\n"; got != want {
		t.Errorf("an empty trunk next to a stacked one:\n%q\nwant\n%q", got, want)
	}
}

func TestRenderLogEmptyAndLinks(t *testing.T) {
	if RenderLog(&app.View{}, LogOptions{}) != "" {
		t.Error("a view with no rows renders nothing")
	}
	if RenderLog(nil, LogOptions{}) != "" {
		t.Error("nil view renders nothing")
	}
	rows := []app.Row{
		{Name: "main", IsTrunk: true, Tracked: true},
		{Name: "a", Depth: 1, Tracked: true, PR: &forge.PullRequest{Number: 5, State: forge.StateOpen, URL: "https://x/5"}},
	}
	out := RenderLog(&app.View{Rows: rows}, LogOptions{Links: true})
	if !strings.Contains(out, hyperlinkOpen("https://x/5")+underlineOn+"#5 open") || !strings.Contains(out, hyperlinkClose) {
		t.Errorf("PR should be a hyperlink when Links is on:\n%q", out)
	}
}

func TestRenderLogNeedsPush(t *testing.T) {
	rows := sampleRows()
	for i := range rows {
		rows[i].NeedsPush = rows[i].Name == "feat/api"
	}
	out := RenderLog(&app.View{Rows: rows}, LogOptions{Now: now})
	if !strings.Contains(out, "#12 open · needs push") {
		t.Errorf("RenderLog =\n%s", out)
	}
}

// A PR the snapshot only has a number for (offline, and gh stack's
// metadata without a URL) is still linked, through the shared resolver,
// in the log, the tree and the picker alike.
func TestPRWithoutURLIsLinkedEverywhere(t *testing.T) {
	resolve := func(n int) string {
		if n == 5 {
			return "https://forge.test/o/r/pull/5"
		}
		return ""
	}
	link := hyperlinkOpen("https://forge.test/o/r/pull/5")
	rows := []app.Row{
		{Name: "main", IsTrunk: true, Tracked: true},
		{Name: "a", Depth: 1, Tracked: true, IsCurrent: true, PR: &forge.PullRequest{Number: 5}},
		{Name: "b", Depth: 2, Tracked: true, PR: &forge.PullRequest{Number: 9}},
	}
	st := DefaultStyles()
	for name, out := range map[string]string{
		"log":    RenderLog(&app.View{Rows: rows}, LogOptions{Styles: st, Links: true, PRURL: resolve}),
		"tree":   RenderTree(BuildTree(rows), RenderOptions{Styles: st, Links: true, PRURL: resolve}),
		"picker": NewPicker(t.Context(), PickerOptions{Rows: rows, Links: true, PRURL: resolve, Height: 5}).View().Content,
	} {
		if !strings.Contains(out, link) {
			t.Errorf("%s: #5 should be linked through the resolver:\n%q", name, out)
		}
		if strings.Count(out, "]8;id=") != 1 {
			t.Errorf("%s: #9 has no URL anywhere and must stay text:\n%q", name, out)
		}
	}
	// Without Links, the resolver isn't even asked.
	if out := RenderLog(&app.View{Rows: rows}, LogOptions{PRURL: func(int) string { t.Error("asked"); return "" }}); strings.Contains(out, "]8;") {
		t.Errorf("no links when Links is off: %q", out)
	}
}
