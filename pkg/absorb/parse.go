package absorb

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// Parse reads a diff as git prints it with zero lines of context and
// rename detection (diff -U0 -M, a/ and b/ prefixes) into one FileDiff per
// file. Should a hunk carry context lines anyway, it is split into one
// Hunk per stretch of changed lines, numbered as git would number each on
// its own, so every Hunk is exactly one contiguous change.
func Parse(diff string) ([]FileDiff, error) {
	var files []FileDiff
	var cur *FileDiff
	lines := strings.Split(diff, "\n")
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files = append(files, FileDiff{})
			cur = &files[len(files)-1]
			if old, nu, ok := splitHeader(strings.TrimPrefix(line, "diff --git ")); ok {
				cur.OldPath, cur.NewPath = old, nu
			}
		case cur == nil:
			// Anything before the first file header is not ours to read.
		case strings.HasPrefix(line, "--- "):
			cur.OldPath = sidePath(line[4:], "a/")
		case strings.HasPrefix(line, "+++ "):
			cur.NewPath = sidePath(line[4:], "b/")
		case strings.HasPrefix(line, "rename from "):
			cur.OldPath = unquotePath(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			cur.NewPath = unquotePath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "new file mode "):
			cur.OldPath = ""
			cur.Submodule = cur.Submodule || strings.HasSuffix(line, " 160000")
		case strings.HasPrefix(line, "deleted file mode "):
			cur.NewPath = ""
			cur.Submodule = cur.Submodule || strings.HasSuffix(line, " 160000")
		case strings.HasPrefix(line, "index ") && strings.HasSuffix(line, " 160000"):
			cur.Submodule = true
		case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
			cur.Binary = true
		case strings.HasPrefix(line, "@@ "):
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("bad hunk header: %q", line)
			}
			next, err := hunkBody(cur, lines, i+1, atoiDefault(m[1], 0), atoiDefault(m[2], 1), atoiDefault(m[3], 0), atoiDefault(m[4], 1))
			if err != nil {
				return nil, err
			}
			i = next
			continue
		}
		i++
	}
	return files, nil
}

// hunkBody reads one hunk's lines starting at lines[i], splitting on
// context lines, and returns the index of the first line after it. The
// counts from the header say how many lines belong to the hunk.
func hunkBody(f *FileDiff, lines []string, i, oldStart, oldCount, newStart, newCount int) (int, error) {
	// oldNext and newNext are the numbers of the next old and new line the
	// body will mention; a side with no lines starts just after its anchor.
	oldNext, newNext := oldStart, newStart
	if oldCount == 0 {
		oldNext++
	}
	if newCount == 0 {
		newNext++
	}
	oldLeft, newLeft := oldCount, newCount
	var run *Hunk
	runOld, runNew := 0, 0
	open := func() {
		if run == nil {
			f.Hunks = append(f.Hunks, Hunk{})
			run = &f.Hunks[len(f.Hunks)-1]
			runOld, runNew = oldNext, newNext
		}
	}
	closeRun := func() {
		if run == nil {
			return
		}
		run.OldStart, run.NewStart = runOld, runNew
		if run.OldCount == 0 {
			run.OldStart = runOld - 1
		}
		if run.NewCount == 0 {
			run.NewStart = runNew - 1
		}
		run = nil
	}
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, `\`) {
			// "\ No newline at end of file" belongs to the line before it
			// and counts for neither side.
			if run != nil {
				run.Lines = append(run.Lines, line)
			}
			i++
			continue
		}
		if oldLeft == 0 && newLeft == 0 {
			break
		}
		switch {
		case strings.HasPrefix(line, "-"):
			open()
			run.Lines = append(run.Lines, line)
			run.OldCount++
			oldNext++
			oldLeft--
		case strings.HasPrefix(line, "+"):
			open()
			run.Lines = append(run.Lines, line)
			run.NewCount++
			newNext++
			newLeft--
		case line == "" || strings.HasPrefix(line, " "):
			// A context line ("" when diff.suppressBlankEmpty is set).
			closeRun()
			oldNext++
			newNext++
			oldLeft--
			newLeft--
		default:
			return i, fmt.Errorf("unexpected line in hunk: %q", line)
		}
		i++
	}
	closeRun()
	return i, nil
}

// splitHeader reads the two paths off a "diff --git a/x b/y" header. A
// quoted first path is read as such; otherwise the line is split in the
// middle, which is right whenever both sides are the same path (every
// change but a rename, and renames have their own lines).
func splitHeader(rest string) (old, nu string, ok bool) {
	if strings.HasPrefix(rest, `"`) {
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return "", "", false
		}
		first := unquotePath(q)
		second := unquotePath(strings.TrimPrefix(rest[len(q):], " "))
		return strings.TrimPrefix(first, "a/"), strings.TrimPrefix(second, "b/"), true
	}
	if n := len(rest); n%2 == 1 {
		a, b := rest[:n/2], rest[n/2+1:]
		if strings.HasPrefix(a, "a/") && strings.HasPrefix(b, "b/") && a[2:] == b[2:] && rest[n/2] == ' ' {
			return a[2:], b[2:], true
		}
	}
	return "", "", false
}

// sidePath reads the path off a --- or +++ line: "" for /dev/null,
// otherwise unquoted and without its a/ or b/ prefix.
func sidePath(s, prefix string) string {
	// git follows a name containing a space with a tab on these lines.
	s = strings.TrimSuffix(s, "\t")
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(unquotePath(s), prefix)
}

// unquotePath undoes git's C style quoting ("a\303\251.txt", "tab\t");
// a path that isn't quoted is returned as is.
func unquotePath(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// atoiDefault parses a hunk header number, giving def when it is absent.
// The header regexp only lets digits through, so the error is ignored.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, _ := strconv.Atoi(s)
	return n
}
