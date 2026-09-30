package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

var now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func sampleRows() []app.Row {
	h := func(d time.Duration) time.Time { return now.Add(-d) }
	return []app.Row{
		{Name: "main", Depth: 0, IsTrunk: true, Tracked: true, LastCommit: h(30 * time.Minute)},
		{Name: "feat/api", Depth: 1, Parent: "main", Tracked: true, LastCommit: h(3 * time.Hour), PR: &forge.PullRequest{Number: 12, State: forge.StateOpen}},
		{Name: "feat/ui", Depth: 2, Parent: "feat/api", Tracked: true, IsCurrent: true, LastCommit: h(2 * 24 * time.Hour), PR: &forge.PullRequest{Number: 13, State: forge.StateDraft}},
		{Name: "feat/ui-tests", Depth: 3, Parent: "feat/ui", Tracked: true, NeedsRestack: true, LastCommit: h(10 * 24 * time.Hour)},
		{Name: "fix/typo", Depth: 1, Parent: "main", Tracked: true, LastCommit: h(40 * 24 * time.Hour), PR: &forge.PullRequest{Number: 3, State: forge.StateMerged}, Worktree: "/tmp/wt"},
		{Name: "release", Depth: 0, IsTrunk: true, Tracked: true},
		{Name: "hotfix", Depth: 1, Parent: "release", Tracked: true, PR: &forge.PullRequest{Number: 7, State: forge.StateClosed}},
		{Name: "scratch", Depth: 0, Tracked: false, LastCommit: h(400 * 24 * time.Hour)},
	}
}

func TestBuildTreePrefixes(t *testing.T) {
	rows := BuildTree(sampleRows())
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.Prefix + r.Row.Name
	}
	want := []string{
		"main",
		"├─ feat/api",
		"│  └─ feat/ui",
		"│     └─ feat/ui-tests",
		"└─ fix/typo",
		"release",
		"└─ hotfix",
		"scratch",
	}
	if !slices.Equal(got, want) {
		t.Errorf("prefixes =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFuzzyMatch(t *testing.T) {
	ok, idx := FuzzyMatch("feat/ui-tests", "fut")
	if !ok || !slices.Equal(idx, []int{0, 5, 8}) {
		t.Errorf("fut: %v %v", ok, idx)
	}
	if ok, _ := FuzzyMatch("feat/api", "xyz"); ok {
		t.Error("xyz should not match")
	}
	if ok, _ := FuzzyMatch("Feat/API", "api"); !ok {
		t.Error("match should be case-insensitive")
	}
	if ok, idx := FuzzyMatch("main", ""); !ok || idx != nil {
		t.Error("empty query matches everything")
	}
}

func TestFilterTreeKeepsAncestors(t *testing.T) {
	rows := BuildTree(sampleRows())
	got := FilterTree(rows, "tests")
	names := make([]string, len(got))
	for i, r := range got {
		names[i] = r.Row.Name
	}
	if !slices.Equal(names, []string{"main", "feat/api", "feat/ui", "feat/ui-tests"}) {
		t.Errorf("filter tests = %v", names)
	}
	if got[3].Matched == nil || !got[3].IsMatch || got[0].IsMatch || got[0].Matched != nil {
		t.Errorf("match flags wrong: %+v", got)
	}

	got = FilterTree(rows, "hot")
	if len(got) != 2 || got[0].Row.Name != "release" || got[1].Row.Name != "hotfix" {
		t.Errorf("filter hot = %+v", got)
	}
	got = FilterTree(rows, "scratch")
	if len(got) != 1 || got[0].Row.Name != "scratch" {
		t.Errorf("untracked rows have no ancestors: %+v", got)
	}
	if len(FilterTree(rows, "")) != len(rows) {
		t.Error("empty filter keeps everything")
	}
	if len(FilterTree(rows, "zzz")) != 0 {
		t.Error("no match yields nothing")
	}
}

func TestRelativeTime(t *testing.T) {
	cases := map[time.Duration]string{
		10 * time.Second:      "just now",
		5 * time.Minute:       "5m ago",
		3 * time.Hour:         "3h ago",
		2 * 24 * time.Hour:    "2d ago",
		10 * 24 * time.Hour:   "1w ago",
		40 * 24 * time.Hour:   "1mo ago",
		400 * 24 * time.Hour:  "1y ago",
		1000 * 24 * time.Hour: "2y ago",
	}
	for d, want := range cases {
		if got := RelativeTime(now.Add(-d), now); got != want {
			t.Errorf("%v: got %q want %q", d, got, want)
		}
	}
	if RelativeTime(time.Time{}, now) != "" {
		t.Error("zero time renders empty")
	}
}

func TestRenderTreePlain(t *testing.T) {
	// Styles zero value renders without escape codes.
	out := RenderTree(BuildTree(sampleRows()), RenderOptions{Now: now})
	want := strings.Join([]string{
		"  main  30m ago",
		"  ├─ feat/api  #12 open · 3h ago",
		"* │  └─ feat/ui  #13 draft · 2d ago",
		"  │     └─ feat/ui-tests  needs restack · 1w ago",
		"  └─ fix/typo  #3 merged · 1mo ago · in /tmp/wt",
		"  release",
		"  └─ hotfix  #7 closed",
		"  scratch  untracked · 1y ago",
	}, "\n")
	if out != want {
		t.Errorf("RenderTree =\n%s\nwant\n%s", out, want)
	}
}
