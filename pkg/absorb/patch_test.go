package absorb

import (
	"strings"
	"testing"
)

func TestPatchRenumbersCumulatively(t *testing.T) {
	// An insertion of two lines after line 1, then an insertion after line
	// 3 and an edit of line 5, all given as if each were alone (new side
	// numbers stale or wrong on purpose).
	hunks := []Hunk{
		{OldStart: 5, OldCount: 1, NewStart: 5, NewCount: 1, Lines: []string{"-l5", "+L5"}},
		{OldStart: 1, OldCount: 0, NewStart: 99, NewCount: 2, Lines: []string{"+P", "+Q"}},
		{OldStart: 3, OldCount: 0, NewStart: 4, NewCount: 1, Lines: []string{"+R"}},
		{OldStart: 8, OldCount: 2, NewStart: 7, NewCount: 0, Lines: []string{"-l8", "-l9"}},
	}
	want := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n" +
		"@@ -1,0 +2,2 @@\n+P\n+Q\n" +
		"@@ -3,0 +6,1 @@\n+R\n" +
		"@@ -5,1 +8,1 @@\n-l5\n+L5\n" +
		"@@ -8,2 +11,0 @@\n-l8\n-l9\n"
	if got := Patch("f.txt", hunks); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// An insertion after line k sorts after an edit of line k, so the inserted
// lines land after the edited one.
func TestPatchOrdersInsertionAfterEditOfSameLine(t *testing.T) {
	hunks := []Hunk{
		{OldStart: 4, OldCount: 0, NewCount: 1, Lines: []string{"+after4"}},
		{OldStart: 4, OldCount: 1, NewCount: 1, Lines: []string{"-l4", "+L4"}},
	}
	want := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n" +
		"@@ -4,1 +4,1 @@\n-l4\n+L4\n" +
		"@@ -4,0 +5,1 @@\n+after4\n"
	if got := Patch("f.txt", hunks); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// An insertion before the first line has an old start of 0.
func TestPatchInsertionAtTopOfFile(t *testing.T) {
	h := Hunk{OldStart: 0, OldCount: 0, NewCount: 2, Lines: []string{"+a", "+b"}}
	want := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -0,0 +1,2 @@\n+a\n+b\n"
	if got := Patch("f.txt", []Hunk{h}); got != want {
		t.Errorf("got:\n%s", got)
	}
}

func TestPatchKeepsMarker(t *testing.T) {
	h := Hunk{OldStart: 5, OldCount: 1, NewCount: 1, Lines: []string{"-l5", "\\ No newline at end of file", "+l5"}}
	want := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -5,1 +5,1 @@\n-l5\n\\ No newline at end of file\n+l5\n"
	if got := Patch("f.txt", []Hunk{h}); got != want {
		t.Errorf("got:\n%s", got)
	}
}

func TestPatchQuotesPaths(t *testing.T) {
	cases := map[string]string{
		"plain.txt":    "plain.txt",
		"has space.go": "has space.go",
		"tab\there":    `"tab\there"`,
		"say \"hi\"":   `"say \"hi\""`,
		"café.txt":     `"caf\303\251.txt"`,
		"new\nline":    `"new\nline"`,
		`a\b.txt`:      `"a\\b.txt"`,
		"x":            "x",
	}
	for in, want := range cases {
		if got := quotePath(in); got != want {
			t.Errorf("quotePath(%q) = %s, want %s", in, got, want)
		}
		// What we write, we can read back.
		if back := unquotePath(quotePath(in)); back != in {
			t.Errorf("round trip of %q gave %q", in, back)
		}
	}
	got := Patch("café.txt", []Hunk{{OldStart: 1, OldCount: 1, NewCount: 1, Lines: []string{"-a", "+b"}}})
	if !strings.HasPrefix(got, "diff --git \"a/caf\\303\\251.txt\" \"b/caf\\303\\251.txt\"\n--- \"a/caf\\303\\251.txt\"\n+++ \"b/caf\\303\\251.txt\"\n") {
		t.Errorf("header:\n%s", got)
	}
}

func TestPatchFilesSortsByPath(t *testing.T) {
	files := map[string][]Hunk{
		"b.go": {{OldStart: 1, OldCount: 1, NewCount: 1, Lines: []string{"-x", "+y"}}},
		"a.go": {{OldStart: 1, OldCount: 1, NewCount: 1, Lines: []string{"-x", "+y"}}},
	}
	got := PatchFiles(files)
	if strings.Index(got, "a/a.go") > strings.Index(got, "a/b.go") || strings.Count(got, "diff --git") != 2 {
		t.Errorf("got:\n%s", got)
	}
	if PatchFiles(nil) != "" {
		t.Error("no files should give an empty patch")
	}
}
