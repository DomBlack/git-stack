package ui

import (
	"hash/fnv"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Every escape sequence we build ends in ST (ESC \), the terminator the
// standards ask for, rather than BEL. Terminals accept both; we never mix.
const (
	esc = "\x1b"
	st  = esc + "\\"
)

// Hyperlink wraps text in an OSC 8 hyperlink to url when enabled, so
// terminals such as Ghostty, iTerm2 and WezTerm make it clickable. With
// enabled false (not a terminal) or an empty url it returns text unchanged.
//
// Every link is underlined, and that is the only thing that marks it out
// (docs/style.md); the text keeps whatever colour it had. The underline is
// part of the link, so it only appears when the link does, and it is
// switched back off (SGR 24, not a full reset) at the end so it never runs
// into the text after it or disturbs a style the link sits inside.
//
// The link carries an id derived from the url. Lines drawn by bubbletea's
// renderer (the picker, lines printed above a spinner) can be written in
// several pieces as the screen updates; the id is what makes the terminal
// treat those pieces as one link, so hovering underlines all of it.
func Hyperlink(enabled bool, url, text string) string {
	if !enabled || url == "" {
		return text
	}
	return hyperlinkOpen(url) + underlineOn + keepUnderline(text) + underlineOff + hyperlinkClose
}

// SGR underline on and off.
const (
	underlineOn  = esc + "[4m"
	underlineOff = esc + "[24m"
)

// keepUnderline turns the underline back on after every SGR sequence in
// text, since styled text resets its attributes (ESC[m) at the end of each
// styled run and would otherwise drop the underline part way through.
func keepUnderline(text string) string {
	if !strings.Contains(text, esc+"[") {
		return text
	}
	var b strings.Builder
	for len(text) > 0 {
		i := strings.IndexByte(text, 0x1b)
		if i < 0 {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:i])
		n, _, _ := scanEscape(text[i:])
		seq := text[i : i+n]
		b.WriteString(seq)
		if strings.HasPrefix(seq, esc+"[") && strings.HasSuffix(seq, "m") {
			b.WriteString(underlineOn)
		}
		text = text[i+n:]
	}
	return b.String()
}

// hyperlinkClose ends the current link: OSC 8 ; ; ST.
const hyperlinkClose = esc + "]8;;" + st

// hyperlinkOpen starts a link: OSC 8 ; id=<id> ; <uri> ST.
func hyperlinkOpen(url string) string {
	return esc + "]8;id=" + linkID(url) + ";" + encodeURI(url) + st
}

// linkID is a short, stable id for url. Cells with the same URI and the same
// id highlight together; one id per URL keeps every piece of a link joined.
func linkID(url string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(url))
	return "gs" + strconv.FormatUint(uint64(h.Sum32()), 36)
}

// encodeURI percent encodes every byte outside the printable ASCII range
// (and the space), as OSC 8 only allows bytes 32 to 126 in a URI. Existing
// escapes are left alone.
func encodeURI(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if c <= ' ' || c >= 0x7f {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// linkable finds what Linkify turns into links: web URLs and #123 style pull
// request references. A URL candidate runs to the next space or quote;
// trimURL then takes off what belongs to the sentence around it.
var linkable = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+|#[0-9]+\b`)

// trimURL drops what ends a URL candidate but belongs to the prose around
// it: sentence punctuation, and a closing ) or ] with no opener inside the
// URL. Balanced ones stay, so .../Foo_(bar) and http://[2001:db8::1] keep
// theirs while "(see https://x.y/z)" leaves its paren outside the link.
func trimURL(u string) string {
	for len(u) > 0 {
		switch c := u[len(u)-1]; {
		case strings.IndexByte(".,;:!?", c) >= 0:
		case c == ')' && strings.Count(u, "(") < strings.Count(u, ")"):
		case c == ']' && strings.Count(u, "[") < strings.Count(u, "]"):
		default:
			return u
		}
		u = u[:len(u)-1]
	}
	return u
}

// Linkify makes the pull request references (#123) and web URLs in a line
// OSC 8 hyperlinks. prURL maps a PR number to its URL ("" leaves it as
// text). Escape sequences in the line are kept, and text that is already
// inside a link is not linked again, so a styled or pre-linked line is safe
// to pass through. Callers decide whether the stream is a terminal.
func Linkify(line string, prURL func(int) string) string {
	if !strings.ContainsAny(line, "#:") {
		return line
	}
	var b strings.Builder
	inLink := false
	// boundary is true when the previous visible byte cannot be part of a
	// word, so "#12" there is a reference rather than "abc#12" or "&#12".
	boundary := true
	for len(line) > 0 {
		if line[0] == 0x1b {
			n, uri, isLink := scanEscape(line)
			if isLink {
				inLink = uri != ""
			}
			b.WriteString(line[:n])
			line = line[n:]
			continue
		}
		end := strings.IndexByte(line, 0x1b)
		if end < 0 {
			end = len(line)
		}
		run := line[:end]
		line = line[end:]
		if inLink {
			b.WriteString(run)
		} else {
			b.WriteString(linkifyRun(run, boundary, prURL))
		}
		if run != "" {
			boundary = !isWordByte(run[len(run)-1])
		}
	}
	return b.String()
}

func linkifyRun(run string, boundary bool, prURL func(int) string) string {
	matches := linkable.FindAllStringIndex(run, -1)
	if matches == nil {
		return run
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		text := run[m[0]:m[1]]
		var url string
		if text[0] != '#' {
			text = trimURL(text)
			m[1] = m[0] + len(text)
			if strings.HasSuffix(text, "://") {
				continue // nothing left after the scheme
			}
		}
		if text[0] == '#' {
			before := boundary
			if m[0] > 0 {
				before = !isWordByte(run[m[0]-1]) && run[m[0]-1] != '&'
			}
			if n, err := strconv.Atoi(text[1:]); err == nil && before && prURL != nil {
				url = prURL(n)
			}
		} else {
			url = text
		}
		if url == "" {
			continue
		}
		b.WriteString(run[last:m[0]])
		b.WriteString(Hyperlink(true, url, text))
		last = m[1]
	}
	b.WriteString(run[last:])
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || c == '/' || c == '#' ||
		('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// scanEscape returns the length of the escape sequence at the start of s
// and, for an OSC 8, its URI (empty for a link close).
func scanEscape(s string) (n int, uri string, isLink bool) {
	if len(s) < 2 {
		return len(s), "", false
	}
	switch s[1] {
	case ']', 'P', '_', '^', 'X':
		// String sequences run to ST (ESC \). Only OSC may also end in BEL;
		// in DCS, APC, PM and SOS a BEL is payload (tmux passthrough
		// carries whole BEL terminated sequences inside a DCS).
		body := len(s)
		end := len(s)
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 && s[1] == ']' {
				body, end = i, i+1
				break
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				body, end = i, i+2
				break
			}
		}
		if s[1] == ']' && strings.HasPrefix(s[2:body], "8;") {
			if _, u, ok := strings.Cut(s[4:body], ";"); ok {
				return end, u, true
			}
		}
		return end, "", false
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1, "", false
			}
		}
		return len(s), "", false
	default:
		return 2, "", false
	}
}

// OSC 9;4 progress states (ConEmu's extension, supported by Ghostty, Windows
// Terminal and others). State 3 is "indeterminate", 0 removes the indicator.
const (
	progressIndeterminate = "\x1b]9;4;3\x1b\\"
	progressClear         = "\x1b]9;4;0\x1b\\"
)

// Busy marks the terminal as busy with an indeterminate progress indicator
// (a pulsing tab or taskbar entry, depending on the terminal) and returns a
// function that clears it. With enabled false (not a terminal) it does
// nothing. Terminals without OSC 9;4 support ignore the sequence.
func Busy(w io.Writer, enabled bool) func() {
	if !enabled || w == nil {
		return func() {}
	}
	_, _ = io.WriteString(w, progressIndeterminate)
	var once sync.Once
	return func() {
		once.Do(func() { _, _ = io.WriteString(w, progressClear) })
	}
}
