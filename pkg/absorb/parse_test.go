package absorb

import (
	"slices"
	"strings"
	"testing"
)

// one is a zero context diff as git prints it: a replacement, an
// insertion after line 2 and a deletion of lines 10 and 11.
const one = `diff --git a/f.txt b/f.txt
index 1111111..2222222 100644
--- a/f.txt
+++ b/f.txt
@@ -1 +1 @@
-l1
+L1
@@ -2,0 +3,2 @@ l2
+X
+Y
@@ -10,2 +11,0 @@ l9
-l10
-l11
`

func TestParseZeroContextHunks(t *testing.T) {
	files, err := Parse(one)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].OldPath != "f.txt" || files[0].NewPath != "f.txt" || files[0].IsNew() || files[0].IsDeleted() {
		t.Fatalf("files = %+v", files)
	}
	want := []Hunk{
		{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1, Lines: []string{"-l1", "+L1"}},
		{OldStart: 2, OldCount: 0, NewStart: 3, NewCount: 2, Lines: []string{"+X", "+Y"}},
		{OldStart: 10, OldCount: 2, NewStart: 11, NewCount: 0, Lines: []string{"-l10", "-l11"}},
	}
	got := files[0].Hunks
	if len(got) != len(want) {
		t.Fatalf("hunks = %+v", got)
	}
	for i := range want {
		if got[i].OldStart != want[i].OldStart || got[i].OldCount != want[i].OldCount || got[i].NewStart != want[i].NewStart || got[i].NewCount != want[i].NewCount || !slices.Equal(got[i].Lines, want[i].Lines) {
			t.Errorf("hunk %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if files[0].Hunks[1].Net() != 2 || files[0].Hunks[2].Net() != -2 {
		t.Error("net wrong")
	}
}

// Context lines (a diff made with -U1, say) split a hunk into one run per
// stretch of changed lines, numbered as git would number each on its own.
func TestParseSplitsOnContextLines(t *testing.T) {
	diff := `diff --git a/f.txt b/f.txt
--- a/f.txt
+++ b/f.txt
@@ -1,5 +1,6 @@
 l1
-l2
+L2
 l3
+L3b
 l4
 l5
`
	files, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	h := files[0].Hunks
	if len(h) != 2 {
		t.Fatalf("hunks = %+v", h)
	}
	if h[0].OldStart != 2 || h[0].OldCount != 1 || h[0].NewStart != 2 || h[0].NewCount != 1 {
		t.Errorf("first = %+v", h[0])
	}
	if h[1].OldStart != 3 || h[1].OldCount != 0 || h[1].NewStart != 4 || h[1].NewCount != 1 || !slices.Equal(h[1].Lines, []string{"+L3b"}) {
		t.Errorf("second = %+v", h[1])
	}
}

func TestParseKeepsNoNewlineMarker(t *testing.T) {
	diff := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -5 +5 @@ l4\n-l5\n\\ No newline at end of file\n+l5\n"
	files, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-l5", "\\ No newline at end of file", "+l5"}
	if len(files[0].Hunks) != 1 || !slices.Equal(files[0].Hunks[0].Lines, want) {
		t.Errorf("hunks = %+v", files[0].Hunks)
	}
	// The marker after the very last line of the diff is kept too.
	diff = "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -5 +5 @@ l4\n-l5\n+L5\n\\ No newline at end of file\n"
	files, _ = Parse(diff)
	if got := files[0].Hunks[0].Lines; !slices.Equal(got, []string{"-l5", "+L5", "\\ No newline at end of file"}) {
		t.Errorf("trailing marker lost: %q", got)
	}
}

func TestParseNewDeletedBinaryModeRenameSubmodule(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/new.txt b/new.txt",
		"new file mode 100644",
		"index 0000000..1111111",
		"--- /dev/null",
		"+++ b/new.txt",
		"@@ -0,0 +1 @@",
		"+hello",
		"diff --git a/gone.txt b/gone.txt",
		"deleted file mode 100644",
		"index 1111111..0000000",
		"--- a/gone.txt",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-bye",
		"diff --git a/pic.png b/pic.png",
		"index 1111111..2222222 100644",
		"Binary files a/pic.png and b/pic.png differ",
		"diff --git a/run.sh b/run.sh",
		"old mode 100644",
		"new mode 100755",
		"diff --git a/old.go b/new.go",
		"similarity index 90%",
		"rename from old.go",
		"rename to new.go",
		"index 1111111..2222222 100644",
		"--- a/old.go",
		"+++ b/new.go",
		"@@ -3 +3 @@",
		"-a",
		"+b",
		"diff --git a/moved.go b/elsewhere.go",
		"similarity index 100%",
		"rename from moved.go",
		"rename to elsewhere.go",
		"diff --git a/sub b/sub",
		"index 1111111..2222222 160000",
		"--- a/sub",
		"+++ b/sub",
		"@@ -1 +1 @@",
		"-Subproject commit 1111111",
		"+Subproject commit 2222222",
		"",
	}, "\n")
	files, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 7 {
		t.Fatalf("got %d files: %+v", len(files), files)
	}
	f := files
	if !f[0].IsNew() || f[0].NewPath != "new.txt" || len(f[0].Hunks) != 1 || f[0].Hunks[0].OldStart != 0 || f[0].Hunks[0].NewStart != 1 {
		t.Errorf("new = %+v", f[0])
	}
	if !f[1].IsDeleted() || f[1].OldPath != "gone.txt" || len(f[1].Hunks) != 1 || f[1].Hunks[0].NewStart != 0 {
		t.Errorf("deleted = %+v", f[1])
	}
	if !f[2].Binary || f[2].NewPath != "pic.png" || len(f[2].Hunks) != 0 {
		t.Errorf("binary = %+v", f[2])
	}
	if f[3].NewPath != "run.sh" || f[3].OldPath != "run.sh" || len(f[3].Hunks) != 0 || f[3].Binary {
		t.Errorf("mode only = %+v", f[3])
	}
	if !f[4].Renamed() || f[4].OldPath != "old.go" || f[4].NewPath != "new.go" || len(f[4].Hunks) != 1 {
		t.Errorf("rename with edit = %+v", f[4])
	}
	if !f[5].Renamed() || f[5].OldPath != "moved.go" || f[5].NewPath != "elsewhere.go" || len(f[5].Hunks) != 0 {
		t.Errorf("pure rename = %+v", f[5])
	}
	if !f[6].Submodule || f[6].NewPath != "sub" {
		t.Errorf("submodule = %+v", f[6])
	}
}

func TestParseQuotedPaths(t *testing.T) {
	diff := "diff --git \"a/sp ace\\t.txt\" \"b/sp ace\\t.txt\"\n--- \"a/sp ace\\t.txt\"\n+++ \"b/sp ace\\t.txt\"\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/caf\\303\\251.txt b/caf\\303\\251.txt\n--- \"a/caf\\303\\251.txt\"\n+++ \"b/caf\\303\\251.txt\"\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/has space.txt b/has space.txt\nold mode 100644\nnew mode 100755\n"
	files, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].OldPath != "sp ace\t.txt" || files[0].NewPath != "sp ace\t.txt" {
		t.Errorf("tab path = %q %q", files[0].OldPath, files[0].NewPath)
	}
	if files[1].NewPath != "café.txt" {
		t.Errorf("octal path = %q", files[1].NewPath)
	}
	// No --- / +++ lines for a mode only change: the header is the only
	// source of the path, and both sides are the same.
	if files[2].OldPath != "has space.txt" || files[2].NewPath != "has space.txt" {
		t.Errorf("header path = %q %q", files[2].OldPath, files[2].NewPath)
	}
}

func TestParseEmptyAndBad(t *testing.T) {
	files, err := Parse("")
	if err != nil || len(files) != 0 {
		t.Errorf("empty: %+v %v", files, err)
	}
	if _, err := Parse("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ nonsense @@\n"); err == nil {
		t.Error("bad hunk header should fail")
	}
	if _, err := Parse("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n?what\n"); err == nil {
		t.Error("bad hunk line should fail")
	}
}

// Git follows a file name on the --- and +++ lines with a tab when the name
// contains a space, quoted or not. The tab is not part of the path.
func TestParseTrailingTabOnSpacedPaths(t *testing.T) {
	diff := "diff --git a/has space.txt b/has space.txt\n--- a/has space.txt\t\n+++ b/has space.txt\t\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git \"a/sp ace\\t.txt\" \"b/sp ace\\t.txt\"\n--- \"a/sp ace\\t.txt\"\t\n+++ \"b/sp ace\\t.txt\"\t\n@@ -1 +1 @@\n-a\n+b\n"
	files, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].OldPath != "has space.txt" || files[0].NewPath != "has space.txt" {
		t.Errorf("unquoted = %q %q", files[0].OldPath, files[0].NewPath)
	}
	if files[1].OldPath != "sp ace\t.txt" || files[1].NewPath != "sp ace\t.txt" {
		t.Errorf("quoted = %q %q", files[1].OldPath, files[1].NewPath)
	}
}
