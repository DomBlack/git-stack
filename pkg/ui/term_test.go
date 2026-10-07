package ui

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

func TestHyperlink(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enabled       bool
		url, text     string
		want          string
		wantUnchanged bool
	}{
		{name: "disabled", enabled: false, url: "https://example.com/pr/1", text: "#1", wantUnchanged: true},
		{name: "no url", enabled: true, url: "", text: "#1", wantUnchanged: true},
		{
			name: "ST terminated with an id", enabled: true, url: "https://example.com/pr/1", text: "#1",
			want: "\x1b]8;id=" + linkID("https://example.com/pr/1") + ";https://example.com/pr/1\x1b\\\x1b[4m#1\x1b[24m\x1b]8;;\x1b\\",
		},
		{
			name: "bytes outside 32-126 are percent encoded", enabled: true, url: "https://example.com/a b/é", text: "x",
			want: "\x1b]8;id=" + linkID("https://example.com/a b/é") + ";https://example.com/a%20b/%C3%A9\x1b\\\x1b[4mx\x1b[24m\x1b]8;;\x1b\\",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Hyperlink(tc.enabled, tc.url, tc.text)
			if tc.wantUnchanged {
				tc.want = tc.text
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
			if strings.Contains(got, "\a") {
				t.Error("links end in ST, never BEL")
			}
		})
	}
	if linkID("https://a") == linkID("https://b") {
		t.Error("link ids must differ between URLs")
	}
}

// The underline is the link's only cue: it covers all of the link text,
// survives the resets inside styled text, and is switched off (not reset)
// at the end so the style around the link carries on.
func TestHyperlinkUnderline(t *testing.T) {
	url := "https://f/pull/12"
	open, closeLink := hyperlinkOpen(url), hyperlinkClose
	for _, tc := range []struct{ name, text, want string }{
		{"plain", "#12", open + "\x1b[4m#12\x1b[24m" + closeLink},
		{"state colour kept", "\x1b[32m#12 open\x1b[m", open + "\x1b[4m\x1b[32m\x1b[4m#12 open\x1b[m\x1b[4m\x1b[24m" + closeLink},
		{"several styled runs", "\x1b[2m#\x1b[m\x1b[1m12\x1b[m", open + "\x1b[4m\x1b[2m\x1b[4m#\x1b[m\x1b[4m\x1b[1m\x1b[4m12\x1b[m\x1b[4m\x1b[24m" + closeLink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Hyperlink(true, url, tc.text); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
	// Inside a reversed (selected) row the reverse carries on after the link.
	row := "\x1b[7mfeat-a " + Hyperlink(true, url, "#12") + " 2d ago\x1b[m"
	if !strings.Contains(row, "\x1b[24m"+closeLink+" 2d ago") || strings.Contains(row, closeLink+"\x1b[m 2d") {
		t.Errorf("the link must only switch off its own underline: %q", row)
	}
	if Hyperlink(false, url, "#12") != "#12" {
		t.Error("no link, no underline")
	}
}

func TestLinkify(t *testing.T) {
	urls := map[int]string{12: "https://f/pull/12", 7: "https://f/pull/7"}
	resolve := func(n int) string { return urls[n] }
	l := func(url, text string) string { return Hyperlink(true, url, text) }
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain text untouched", "nothing here", "nothing here"},
		{"pr reference", "Created PR #12 for a", "Created PR " + l(urls[12], "#12") + " for a"},
		{"two references", "#7 and #12", l(urls[7], "#7") + " and " + l(urls[12], "#12")},
		{"unknown number stays text", "#99 gone", "#99 gone"},
		{"not inside a word", "abc#12 x/#12 &#12; ##12", "abc#12 x/#12 &#12; ##12"},
		{"not a prefix of a word", "#12abc", "#12abc"},
		{"bare url", "see https://f/pull/7.", "see " + l("https://f/pull/7", "https://f/pull/7") + "."},
		{"url in parens", "(https://f/x)", "(" + l("https://f/x", "https://f/x") + ")"},
		{"url in parens in prose", "(see https://x.y/z)", "(see " + l("https://x.y/z", "https://x.y/z") + ")"},
		{"balanced parens are part of the url", "https://example.com/wiki/Foo_(bar) is it",
			l("https://example.com/wiki/Foo_(bar)", "https://example.com/wiki/Foo_(bar)") + " is it"},
		{"balanced parens then a full stop", "see https://example.com/wiki/Foo_(bar).",
			"see " + l("https://example.com/wiki/Foo_(bar)", "https://example.com/wiki/Foo_(bar)") + "."},
		{"wiki url inside parens", "(https://example.com/wiki/Foo_(bar))",
			"(" + l("https://example.com/wiki/Foo_(bar)", "https://example.com/wiki/Foo_(bar)") + ")"},
		{"ipv6 host keeps its brackets", "at http://[2001:db8::1]:8080/x and http://[2001:db8::1]",
			"at " + l("http://[2001:db8::1]:8080/x", "http://[2001:db8::1]:8080/x") + " and " + l("http://[2001:db8::1]", "http://[2001:db8::1]")},
		{"trailing full stop", "Opened https://f/pull/7.", "Opened " + l("https://f/pull/7", "https://f/pull/7") + "."},
		{"trailing sentence punctuation", "really https://f/x?!", "really " + l("https://f/x", "https://f/x") + "?!"},
		{"nothing after the scheme", "just https:// here", "just https:// here"},
		{
			"styled reference",
			"\x1b[32m#12\x1b[m open",
			"\x1b[32m" + l(urls[12], "#12") + "\x1b[m open",
		},
		{
			"already linked text is left alone",
			l("https://f/pull/12", "\x1b[32m#12 open\x1b[m") + " and #7",
			l("https://f/pull/12", "\x1b[32m#12 open\x1b[m") + " and " + l(urls[7], "#7"),
		},
		{
			"a BEL inside a DCS payload doesn't end it",
			"\x1bPtmux;\x1b\x1b]2;title\a#12\x1b\\ then #7",
			"\x1bPtmux;\x1b\x1b]2;title\a#12\x1b\\ then " + l(urls[7], "#7"),
		},
		{
			"a BEL inside an APC payload doesn't end it",
			"\x1b_x\a#12\x1b\\#7",
			"\x1b_x\a#12\x1b\\" + l(urls[7], "#7"),
		},
		{
			"BEL terminated link from elsewhere",
			"\x1b]8;;https://f/pull/12\a#12\x1b]8;;\a",
			"\x1b]8;;https://f/pull/12\a#12\x1b]8;;\a",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Linkify(tc.in, resolve); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// The escape bytes of a link must not count towards width, or the tree and
// picker columns would drift.
func TestLinksDoNotChangeWidth(t *testing.T) {
	row := TreeRow{Row: app.Row{Name: "feat-a", Depth: 1, Tracked: true,
		PR: &forge.PullRequest{Number: 418, State: forge.StateOpen, URL: "https://github.com/o/r/pull/418"}}, Prefix: "└─ "}
	st := DefaultStyles()
	plain := RenderRow(row, RenderOptions{Styles: st})
	linked := RenderRow(row, RenderOptions{Styles: st, Links: true})
	if !strings.Contains(linked, hyperlinkOpen("https://github.com/o/r/pull/418")) {
		t.Fatalf("row should link its PR: %q", linked)
	}
	if lipgloss.Width(plain) != lipgloss.Width(linked) {
		t.Errorf("width changed with links: %d vs %d", lipgloss.Width(plain), lipgloss.Width(linked))
	}
	if w := lipgloss.Width(Linkify("PR #418 here", func(int) string { return "https://x/418" })); w != len("PR #418 here") {
		t.Errorf("Linkify changed the width to %d", w)
	}
}

func TestBusy(t *testing.T) {
	var w bytes.Buffer
	done := Busy(&w, true)
	if w.String() != "\x1b]9;4;3\x1b\\" {
		t.Errorf("Busy should set an indeterminate progress state, got %q", w.String())
	}
	done()
	if !strings.HasSuffix(w.String(), "\x1b]9;4;0\x1b\\") {
		t.Errorf("done should clear the progress state, got %q", w.String())
	}
	done() // idempotent
	if strings.Count(w.String(), "\x1b]9;4;0\x1b\\") != 1 {
		t.Errorf("clearing twice should write once, got %q", w.String())
	}

	var off bytes.Buffer
	Busy(&off, false)()
	if off.Len() != 0 {
		t.Errorf("disabled Busy must write nothing, got %q", off.String())
	}
}
