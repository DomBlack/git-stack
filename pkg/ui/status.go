package ui

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/DomBlack/git-stack/pkg/stack"
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

// Program status (OSC 7501) states and the fields we send. See
// https://www.superlogical.com/rex/docs/build/program-status.
type programState string

const (
	stateWorking programState = "working"
	stateBlocked programState = "blocked"
	stateDone    programState = "done"
	stateError   programState = "error"
	stateIdle    programState = "idle"
	stateClear   programState = "clear"
)

// statusApp is our app id in every report; reports replace the whole
// record, so it is repeated each time.
const statusApp = "git-stack"

// maxStatusMsg is the protocol's cap on a decoded msg, in bytes.
const maxStatusMsg = 2048

// programStatus is OSC 7501 ; state=…:app=git-stack[:kind=…][:msg=…] ST.
// msg is one line of text, base64 encoded; kind only goes with blocked.
func programStatus(state programState, kind, msg string) string {
	var b strings.Builder
	b.WriteString(esc + "]7501;state=" + string(state))
	if state != stateClear {
		b.WriteString(":app=" + statusApp)
	}
	if kind != "" && state == stateBlocked {
		b.WriteString(":kind=" + kind)
	}
	if msg = truncateBytes(sanitize(msg), maxStatusMsg); msg != "" && state != stateClear {
		b.WriteString(":msg=" + base64.StdEncoding.EncodeToString([]byte(msg)))
	}
	b.WriteString(st)
	return b.String()
}

// tmuxPassthrough wraps seq so tmux hands it to the outer terminal as is:
// DCS tmux; <seq with every ESC doubled> ST. tmux needs allow-passthrough
// on; with it off tmux drops the whole thing, which is harmless.
func tmuxPassthrough(seq string) string {
	return esc + "Ptmux;" + strings.ReplaceAll(seq, esc, esc+esc) + st
}

// sanitize strips escape sequences and control characters (line breaks
// become spaces) so text taken from a message can go inside another
// sequence as one line.
func sanitize(s string) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
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
// beyond the text it printed: the window title and the OSC 7501 program
// status record while a step runs or a prompt waits. Nothing is written
// until the first step or prompt, and finish puts everything back once, at
// the end of the process.
type termStatus struct {
	w       io.Writer
	enabled bool
	// tmux wraps OSC 7501 in tmux's passthrough, as tmux doesn't know it
	// and would swallow it. The title is left bare: tmux handles OSC 2.
	tmux bool

	mu      sync.Mutex
	pushed  bool   // the title was pushed and has to be restored
	worked  bool   // a working report went out
	blocked bool   // a blocked report went out
	message string // the current step's message
	done    bool
}

func newTermStatus(w io.Writer, enabled, tmux bool) *termStatus {
	return &termStatus{w: w, enabled: enabled && w != nil, tmux: tmux}
}

// working reports that the process is busy with message: the window title
// becomes "git stack: <message>" and the status record is working. The
// first call saves the title the terminal had so finish can put it back.
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
	b.WriteString(t.status(stateWorking, "", message))
	t.message = message
	t.worked = true
	t.write(b.String())
}

// waiting reports that the process is blocked on the user answering
// question, and returns a function to call once they have; it puts the
// record back to working on the current step, if there is one.
func (t *termStatus) waiting(question string) func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.enabled || t.done {
		return func() {}
	}
	t.blocked = true
	t.write(t.status(stateBlocked, "question", question))
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.done || !t.worked {
				return
			}
			t.write(t.status(stateWorking, "", t.message))
		})
	}
}

// finish leaves the status record in its final state and puts the title
// back. It is safe to call more than once and from any exit path; only the
// first call writes.
//
// The record follows the protocol's advice for a program that exits as
// soon as it finishes: done (with result, the last result line) or error
// (with the error) right before exiting, so the outcome is still there for
// someone who was looking at another tab; idle when the user interrupted
// it. A run that only ever waited on a prompt (the checkout picker) had no
// result the user hasn't seen, so its record is cleared instead.
func (t *termStatus) finish(err error, result string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	var b strings.Builder
	switch {
	case !t.worked && !t.blocked:
	case errors.Is(err, context.Canceled):
		b.WriteString(t.status(stateIdle, "", "Interrupted"))
	case err != nil:
		b.WriteString(t.status(stateError, "", errorLine(err)))
	case t.worked:
		b.WriteString(t.status(stateDone, "", result))
	default:
		b.WriteString(t.status(stateClear, "", ""))
	}
	if t.pushed {
		// An empty title first, then the pop. Where the title stack works
		// (xterm, tmux, kitty, WezTerm) the pop brings the saved title
		// back. Where the pop is a no-op (Ghostty parses it but doesn't
		// implement it) the empty title resets the terminal to its
		// default, which beats leaving "git stack: Restacking…" up after
		// we've gone.
		b.WriteString(setTitle("") + titlePop)
	}
	if b.Len() > 0 {
		t.write(b.String())
	}
}

// status builds a program status report, wrapped for tmux when needed.
func (t *termStatus) status(state programState, kind, msg string) string {
	seq := programStatus(state, kind, msg)
	if t.tmux {
		return tmuxPassthrough(seq)
	}
	return seq
}

func (t *termStatus) write(s string) { _, _ = io.WriteString(t.w, s) }

// errorLine is the one line an error is summed up by: a stack.Error's
// message without its detail, otherwise the error text.
func errorLine(err error) string {
	if se, ok := errors.AsType[*stack.Error](err); ok {
		return se.Msg
	}
	return err.Error()
}
