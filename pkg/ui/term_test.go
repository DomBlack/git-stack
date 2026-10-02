package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestHyperlink(t *testing.T) {
	plain := Hyperlink(false, "https://example.com/pr/1", "#1")
	if plain != "#1" {
		t.Errorf("disabled hyperlink should be the bare text, got %q", plain)
	}
	// OSC 8 is "ESC ] 8 ; params ; URL ST text ESC ] 8 ; ; ST"; the ansi
	// package terminates with BEL, which terminals accept as ST.
	linked := Hyperlink(true, "https://example.com/pr/1", "#1")
	if !strings.HasPrefix(linked, "\x1b]8;;https://example.com/pr/1\a") || !strings.Contains(linked, "\a#1\x1b]8;;\a") {
		t.Errorf("OSC 8 hyperlink malformed: %q", linked)
	}
	if got := Hyperlink(true, "", "#1"); got != "#1" {
		t.Errorf("no URL means no link, got %q", got)
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
