package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ShortPath shows a path under $HOME as ~/...; other paths are unchanged.
func ShortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return p
}

// RelativeTime renders t relative to now ("3h ago").
func RelativeTime(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}

// QuoteName shows a file name or path as it is when it's plain, and C
// quoted ("a\nb.txt", "say \"hi\"") when it holds anything that could break
// the line or reach the terminal as a control: control characters, quotes,
// backslashes, unprintable or invalid UTF-8. Spaces and printable
// non-ASCII stay as they are. Display only; structured output (MCP) keeps
// the raw name.
func QuoteName(s string) string {
	if !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool {
		return r == '"' || r == '\\' || (r != ' ' && !unicode.IsPrint(r))
	}) {
		return strconv.Quote(s)
	}
	return s
}

// Printable makes text we didn't write (git's stderr, gh stack's output,
// messages that carry file or branch names) safe to print as one line:
// every control character but tab, and every invalid byte, is shown as an
// escape (\n, \x1b) instead of being sent to the terminal.
func Printable(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, isControl) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case isControl(r):
			q := strconv.QuoteRune(r) // '\n', '\x1b', '\u0085'
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

func isControl(r rune) bool {
	return r != '\t' && (r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f))
}
