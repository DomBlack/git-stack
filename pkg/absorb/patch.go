package absorb

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Patch renders hunks for one file as a zero context patch for git apply,
// the path on both sides. The hunks are sorted by old line (an insertion
// after a line goes after an edit of that line) and their new side
// numbers are recomputed so each one accounts for the ones before it,
// which is what git apply uses to place an insertion.
func Patch(path string, hunks []Hunk) string {
	sorted := slices.Clone(hunks)
	slices.SortStableFunc(sorted, func(a, b Hunk) int {
		if a.OldStart != b.OldStart {
			return a.OldStart - b.OldStart
		}
		// Same anchor: the edit of line k before the insertion after k.
		return boolInt(a.OldCount == 0) - boolInt(b.OldCount == 0)
	})
	var b strings.Builder
	q := quotePath(path)
	fmt.Fprintf(&b, "diff --git %s %s\n--- %s\n+++ %s\n", side("a/", path, q), side("b/", path, q), side("a/", path, q), side("b/", path, q))
	delta := 0
	for _, h := range sorted {
		newStart := h.OldStart + delta
		switch {
		case h.OldCount == 0:
			newStart++
		case h.NewCount == 0:
			newStart--
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldCount, newStart, h.NewCount)
		for _, l := range h.Lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		delta += h.Net()
	}
	return b.String()
}

// PatchFiles renders every file's hunks, files in path order, as one patch.
func PatchFiles(files map[string][]Hunk) string {
	var b strings.Builder
	for _, path := range slices.Sorted(maps.Keys(files)) {
		b.WriteString(Patch(path, files[path]))
	}
	return b.String()
}

// side is the a/ or b/ form of a path for a header, quoted as a whole when
// the path needs quoting (git quotes the prefix inside the quotes).
func side(prefix, path, quoted string) string {
	if quoted != path {
		return quotePath(prefix + path)
	}
	return prefix + path
}

// quotePath quotes a path the way git does when it has to: C style with
// \t, \n, \" and \\ as escapes and three digit octal for every other byte
// outside printable ASCII. A path that needs no quoting is returned as is.
func quotePath(p string) string {
	needs := false
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			needs = true
			break
		}
	}
	if !needs {
		return p
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c >= 0x7f {
				fmt.Fprintf(&b, `\%03o`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
