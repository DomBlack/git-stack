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
	if !strings.Contains(out, "\x1b]8;;https://x/5") {
		t.Errorf("PR should be a hyperlink when Links is on:\n%q", out)
	}
}
