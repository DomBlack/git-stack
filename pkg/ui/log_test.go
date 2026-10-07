package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

// logRows is sampleRows with the titles and commits the log shows.
func logRows() []app.Row {
	rows := sampleRows()
	for i := range rows {
		r := &rows[i]
		switch r.Name {
		case "feat/api":
			r.PR.Title = "Add the API"
			r.Commits = []app.Commit{{SHA: "aaaaaaa1111111", Subject: "Add the endpoint"}, {SHA: "bbbbbbb2222222", Subject: "Add the client"}}
		case "feat/ui":
			r.PR.Title = "Build the UI"
			r.Commits = []app.Commit{{SHA: "ccccccc3333333", Subject: "Build the UI"}}
		case "fix/typo":
			r.PR.Title = "Fix a typo"
			r.Commits = []app.Commit{{SHA: "ddddddd4444444", Subject: "Fix a typo"}}
		}
	}
	return rows
}

func TestRenderLogPlain(t *testing.T) {
	// Zero Styles render without escape codes; Links off.
	out := RenderLog(&app.View{Rows: logRows()}, LogOptions{Now: now})
	want := strings.Join([]string{
		"○ feat/ui-tests",
		"│  no PR",
		"│  needs restack · 1w ago",
		"│",
		"● feat/ui",
		"│  #13 Build the UI",
		"│  draft · 2d ago",
		"│",
		"│  • ccccccc - Build the UI",
		"│",
		"○ feat/api",
		"│  #12 Add the API",
		"│  open · 3h ago",
		"│",
		"│  • aaaaaaa - Add the endpoint",
		"│  • bbbbbbb - Add the client",
		"│",
		"│ ○ fix/typo",
		"│ │  #3 Fix a typo",
		"│ │  merged · 1mo ago · in /tmp/wt",
		"│ │",
		"│ │  • ddddddd - Fix a typo",
		"│ │",
		"├─┘",
		"■ main  30m ago",
		"",
		"○ hotfix",
		"│  #7",
		"│  closed",
		"│",
		"■ release",
		"",
	}, "\n")
	if out != want {
		t.Errorf("RenderLog =\n%s\nwant\n%s", out, want)
	}
}

// TestRenderLogBranch covers the block under one branch: the PR and title
// line, the state line and the commit list.
func TestRenderLogBranch(t *testing.T) {
	h := func(d time.Duration) time.Time { return now.Add(-d) }
	commits := func(n int) []app.Commit {
		var cs []app.Commit
		for i := range n {
			cs = append(cs, app.Commit{SHA: fmt.Sprintf("%07d0000", i+1), Subject: fmt.Sprintf("Commit %d", i+1)})
		}
		return cs
	}
	pr := func(title string) *forge.PullRequest {
		return &forge.PullRequest{Number: 5, State: forge.StateOpen, Title: title}
	}
	long := "Teach the parser about the new refresh token format"
	tests := []struct {
		name  string
		row   app.Row
		width int
		want  []string // the lines between the branch name and the trunk
	}{
		{
			name: "no PR",
			row:  app.Row{LastCommit: h(3 * time.Hour)},
			want: []string{"│  no PR", "│  3h ago", "│"},
		},
		{
			name: "no PR and nothing else to say",
			row:  app.Row{},
			want: []string{"│  no PR", "│"},
		},
		{
			name: "PR with a title",
			row:  app.Row{PR: pr("Add refresh tokens"), NeedsRestack: true, NeedsPush: true, LastCommit: h(2 * time.Hour), Worktree: "/tmp/wt"},
			want: []string{"│  #5 Add refresh tokens", "│  open · needs restack · needs push · 2h ago · in /tmp/wt", "│"},
		},
		{
			name: "PR with an empty title shows just the number",
			row:  app.Row{PR: pr(""), LastCommit: h(2 * time.Hour)},
			want: []string{"│  #5", "│  open · 2h ago", "│"},
		},
		{
			name: "PR state unknown has no state word",
			row:  app.Row{PR: &forge.PullRequest{Number: 5, Title: "Offline"}, LastCommit: h(2 * time.Hour)},
			want: []string{"│  #5 Offline", "│  2h ago", "│"},
		},
		{
			name:  "long title cut to the width",
			row:   app.Row{PR: pr(long)},
			width: 30,
			want:  []string{"│  #5 Teach the parser about …", "│  open", "│"},
		},
		{
			name:  "title that fits is left alone",
			row:   app.Row{PR: pr("Short")},
			width: 30,
			want:  []string{"│  #5 Short", "│  open", "│"},
		},
		{
			name: "not a terminal never cuts",
			row:  app.Row{PR: pr(long)},
			want: []string{"│  #5 " + long, "│  open", "│"},
		},
		{
			name: "one commit",
			row:  app.Row{PR: pr("T"), Commits: commits(1)},
			want: []string{"│  #5 T", "│  open", "│", "│  • 0000001 - Commit 1", "│"},
		},
		{
			name: "twelve commits show ten and a count",
			row:  app.Row{PR: pr("T"), Commits: commits(app.MaxCommits), MoreCommits: 2},
			want: append(append([]string{"│  #5 T", "│  open", "│"}, func() []string {
				var ls []string
				for i := range app.MaxCommits {
					ls = append(ls, fmt.Sprintf("│  • %07d - Commit %d", i+1, i+1))
				}
				return ls
			}()...), "│  • and 2 more", "│"),
		},
		{
			name:  "long subject cut to the width",
			row:   app.Row{PR: pr("T"), Commits: []app.Commit{{SHA: "1234567890", Subject: long}}},
			width: 30,
			want:  []string{"│  #5 T", "│  open", "│", "│  • 1234567 - Teach the pars…", "│"},
		},
		{
			name: "control characters are escaped",
			row: app.Row{
				PR:      pr("Title \x1b]8;;https://evil\x1b\\here\x1b]8;;\x1b\\"),
				Commits: []app.Commit{{SHA: "1234567890", Subject: "Evil \x1b]0;pwned\x07 \x1b[2J\tdone\x9b"}},
			},
			want: []string{
				`│  #5 Title \x1b]8;;https://evil\x1b\here\x1b]8;;\x1b\`,
				"│  open",
				"│",
				`│  • 1234567 - Evil \x1b]0;pwned\a \x1b[2J done\x9b`,
				"│",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.row
			r.Name, r.Depth, r.Tracked = "a", 1, true
			rows := []app.Row{{Name: "main", IsTrunk: true, Tracked: true}, r}
			out := RenderLog(&app.View{Rows: rows}, LogOptions{Now: now, Width: tt.width})
			want := "○ a\n" + strings.Join(tt.want, "\n") + "\n■ main\n"
			if out != want {
				t.Errorf("RenderLog =\n%s\nwant\n%s", out, want)
			}
			if strings.ContainsAny(out, "\x1b\x07\t") {
				t.Errorf("raw control characters reached the output: %q", out)
			}
		})
	}
}

// With styles and links on, the width is measured in cells: escapes take
// none and wide characters two.
func TestRenderLogWidthCountsCells(t *testing.T) {
	rows := []app.Row{
		{Name: "main", IsTrunk: true, Tracked: true},
		{Name: "a", Depth: 1, Tracked: true, PR: &forge.PullRequest{Number: 5, State: forge.StateOpen, URL: "https://x/5", Title: strings.Repeat("漢字", 20)},
			Commits: []app.Commit{{SHA: "1234567890", Subject: strings.Repeat("漢字", 20)}}},
		{Name: "b", Depth: 1, Tracked: true, PR: &forge.PullRequest{Number: 6, State: forge.StateDraft, Title: strings.Repeat("x", 40)}},
	}
	const width = 25
	out := RenderLog(&app.View{Rows: rows}, LogOptions{Styles: DefaultStyles(), Links: true, Width: width})
	cut := 0
	for line := range strings.Lines(out) {
		line = strings.TrimSuffix(line, "\n")
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line is %d cells, wider than %d: %q", w, width, line)
		}
		if strings.HasSuffix(line, "…") {
			cut++
		}
	}
	if cut != 3 {
		t.Errorf("both titles and the subject should be cut, %d were:\n%s", cut, out)
	}
	if !strings.Contains(out, hyperlinkOpen("https://x/5")+underlineOn) {
		t.Errorf("the number is still a link:\n%q", out)
	}
}

func TestRenderLogMultipleStacks(t *testing.T) {
	rows := []app.Row{
		{Name: "main", IsTrunk: true, Tracked: true},
		{Name: "a", Depth: 1, Tracked: true, Commits: []app.Commit{{SHA: "aaaaaaaa", Subject: "On a"}}},
		{Name: "b", Depth: 1, Tracked: true, Commits: []app.Commit{{SHA: "bbbbbbbb", Subject: "On b"}}, MoreCommits: 3},
	}
	out := RenderLog(&app.View{Rows: rows}, LogOptions{})
	want := strings.Join([]string{
		"○ a",
		"│  no PR",
		"│",
		"│  • aaaaaaa - On a",
		"│",
		"│ ○ b",
		"│ │  no PR",
		"│ │",
		"│ │  • bbbbbbb - On b",
		"│ │  • and 3 more",
		"│ │",
		"├─┘",
		"■ main",
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
		{Name: "a", Depth: 1, Tracked: true, PR: &forge.PullRequest{Number: 5, State: forge.StateOpen, URL: "https://x/5", Title: "Title"}},
	}
	out := RenderLog(&app.View{Rows: rows}, LogOptions{Links: true})
	if !strings.Contains(out, hyperlinkOpen("https://x/5")+underlineOn+"#5"+underlineOff+hyperlinkClose+" Title\n") {
		t.Errorf("the PR number, and only the number, should be a hyperlink when Links is on:\n%q", out)
	}
}

func TestRenderLogNeedsPush(t *testing.T) {
	rows := sampleRows()
	for i := range rows {
		rows[i].NeedsPush = rows[i].Name == "feat/api"
	}
	out := RenderLog(&app.View{Rows: rows}, LogOptions{Now: now})
	if !strings.Contains(out, "│  open · needs push · 3h ago\n") {
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
