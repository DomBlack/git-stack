package ui

import (
	"io"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// Hyperlink wraps text in an OSC 8 hyperlink to url when enabled, so
// terminals such as Ghostty, iTerm2 and WezTerm make it clickable. With
// enabled false (not a terminal) or an empty url it returns text unchanged.
func Hyperlink(enabled bool, url, text string) string {
	if !enabled || url == "" {
		return text
	}
	return ansi.SetHyperlink(url) + text + ansi.ResetHyperlink()
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
