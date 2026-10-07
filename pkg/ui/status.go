package ui

import (
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Window title sequences. OSC 2 sets the title; XTWINOPS 22;2 / 23;2 push
// the current title onto the terminal's title stack and pop it back.
const (
	titlePush = esc + "[22;2t"
	titlePop  = esc + "[23;2t"
)

// maxTitle bounds the title in bytes. Ghostty ignores a title of 256 bytes
// or more outright, so stay well under that.
const maxTitle = 200

// setTitle is OSC 2 ; title ST with title made safe to send.
func setTitle(title string) string {
	return esc + "]2;" + truncateBytes(sanitize(title), maxTitle) + st
}

// sanitize strips escape sequences and control characters so text taken
// from a message can be embedded in another sequence.
func sanitize(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// termStatus is what this process has told the terminal about itself
// beyond the text it printed: the window title while a step runs. It is
// set once per step and put back once, at the end of the process, by
// finish; nothing is written unless a step ran.
type termStatus struct {
	w       io.Writer
	enabled bool

	mu     sync.Mutex
	pushed bool // the title was pushed and has to be restored
	done   bool
}

func newTermStatus(w io.Writer, enabled bool) *termStatus {
	return &termStatus{w: w, enabled: enabled && w != nil}
}

// working reports that the process is busy with message: the window title
// becomes "git stack: <message>". The first call saves the title the
// terminal had so finish can put it back.
func (t *termStatus) working(message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.enabled || t.done {
		return
	}
	var b strings.Builder
	if !t.pushed {
		b.WriteString(titlePush)
		t.pushed = true
	}
	b.WriteString(setTitle("git stack: " + message))
	t.write(b.String())
}

// finish puts the title back. It is safe to call more than once and from
// any exit path; only the first call writes.
func (t *termStatus) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	if !t.pushed {
		return
	}
	// An empty title first, then the pop. Where the title stack works
	// (xterm, tmux, kitty, WezTerm) the pop brings the saved title back.
	// Where the pop is a no-op (Ghostty parses it but doesn't implement
	// it) the empty title resets the terminal to its default, which beats
	// leaving "git stack: Restacking…" up after we've gone.
	t.write(setTitle("") + titlePop)
}

func (t *termStatus) write(s string) { _, _ = io.WriteString(t.w, s) }
