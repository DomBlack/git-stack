package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/colorprofile"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Reporter prints every human facing line the CLI produces, so all commands
// share one look (see docs/style.md). On a terminal it uses marks, colour,
// emoji headlines and spinners; piped it falls back to plain ASCII prefixes
// (ok:, note:, error:) with the same words.
type Reporter struct {
	in  io.Reader
	out io.Writer
	err io.Writer
	// rawErr is stderr before colour profile wrapping. bubbletea programs
	// (the spinner) need the real file to size the terminal.
	rawErr io.Writer
	o      ReporterOptions
	st     Styles
	term   *termStatus

	mu     sync.Mutex
	active *spinnerHandle // running Step, if any
	gutter gutterWriter
	// result is the last result line, the outcome Finish reports.
	result string

	// prs maps pull request numbers to URLs for Linkify: the ones passed
	// to Ref, then whatever the resolver knows.
	prs     map[int]string
	resolve func(int) string
}

// ReporterOptions describe the streams the Reporter writes to.
type ReporterOptions struct {
	// OutTTY / ErrTTY: stdout / stderr are terminals (marks and colour).
	OutTTY, ErrTTY bool
	// Spinners enables animated headlines; needs a terminal on stdin and
	// stderr. Without it Step prints the headline once as a plain line.
	Spinners bool
	// Quiet drops everything but errors.
	Quiet bool
	// TerminalStatus lets steps and prompts set the window title and the
	// OSC 7501 program status (put back or finished at Finish). It only
	// takes effect on a terminal stderr without Quiet.
	TerminalStatus bool
	// Tmux says stderr is a tmux pane, so program status reports go
	// through tmux's passthrough.
	Tmux bool
}

// NewReporter builds a Reporter over the given streams. Terminal writers
// are wrapped so colour is downsampled to what the terminal supports and
// dropped under NO_COLOR; hyperlinks and the busy state pass through.
func NewReporter(in io.Reader, out, err io.Writer, o ReporterOptions) *Reporter {
	rawErr := err
	if o.OutTTY {
		out = profileWriter(out)
	}
	if o.ErrTTY {
		err = profileWriter(err)
	}
	r := &Reporter{in: in, out: out, err: err, rawErr: rawErr, o: o, st: DefaultStyles(),
		term: newTermStatus(rawErr, o.TerminalStatus && o.ErrTTY && !o.Quiet, o.Tmux)}
	r.gutter.r = r
	return r
}

// profileWriter wraps a real terminal file in a colour profile writer. Other
// writers (test buffers) are left alone so tests see the raw sequences.
func profileWriter(w io.Writer) io.Writer {
	if f, ok := w.(*os.File); ok {
		return colorprofile.NewWriter(f, os.Environ())
	}
	return w
}

// Quiet reports whether non-error output is suppressed.
func (r *Reporter) Quiet() bool { return r.o.Quiet }

// Marks used on a terminal.
const (
	markOK   = "✔"
	markErr  = "✖"
	markWarn = "⚠"
	markStep = "↳"
	markPipe = "│"
)

// phaseEmoji is the one emoji each command family gets, on its headline only.
var phaseEmoji = map[app.Phase]string{
	app.PhaseCreate:  "🌱",
	app.PhaseModify:  "✏️",
	app.PhaseRestack: "🧱",
	app.PhaseSubmit:  "🚀",
	app.PhaseSync:    "🔄",
	app.PhaseAI:      "🤖",
	app.PhaseUpdate:  "📦",
	app.PhaseInstall: "🔧",
	app.PhaseMerge:   "🔀",
}

// Success prints a result line on stdout: "✔ Created feat-a on main".
func (r *Reporter) Success(format string, args ...any) {
	if r.o.Quiet {
		return
	}
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.result = msg
	r.mu.Unlock()
	if r.o.OutTTY {
		r.println(r.out, r.st.Success.Render(markOK)+" "+msg)
		return
	}
	r.println(r.out, "ok: "+msg)
}

// Partial prints a result line on stdout for a command that finished but
// left something undone that the user has to deal with: "⚠ Synced, but main
// was not updated" (note: when piped). Use it instead of Success whenever
// claiming success would be untrue; a real failure is an error instead.
func (r *Reporter) Partial(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.result = msg
	r.mu.Unlock()
	if r.o.Quiet {
		return
	}
	if r.o.OutTTY {
		r.println(r.out, r.st.Warning.Render(markWarn)+" "+msg)
		return
	}
	r.println(r.out, "note: "+msg)
}

// NextStep prints something the user can do about the line above it, on
// stdout under a ⚠ result: "  ↳ git -C ~/src/app stash, then git stack sync
// again" ("  - " when piped). Errors carry their own next steps.
func (r *Reporter) NextStep(format string, args ...any) {
	if r.o.Quiet {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if r.o.OutTTY {
		r.println(r.out, "  "+r.st.Muted.Render(markStep)+" "+msg)
		return
	}
	r.println(r.out, "  - "+msg)
}

// Info prints a detail line on stdout, indented under the result line.
func (r *Reporter) Info(format string, args ...any) {
	if r.o.Quiet {
		return
	}
	r.println(r.out, "  "+fmt.Sprintf(format, args...))
}

// Warn prints a notice on stderr: "⚠ gh stack submits the whole stack".
// Notices carry names and git's words, so control characters in them are
// shown escaped rather than sent to the terminal.
func (r *Reporter) Warn(format string, args ...any) {
	if r.o.Quiet {
		return
	}
	msg := Printable(fmt.Sprintf(format, args...))
	if r.o.ErrTTY {
		r.println(r.err, r.st.Warning.Render(markWarn)+" "+msg)
		return
	}
	r.println(r.err, "note: "+msg)
}

// Error prints an error on stderr: the message, any detail (faint) and the
// next steps. It renders stack.Error specially and anything else as one line.
func (r *Reporter) Error(err error) {
	if errors.Is(err, context.Canceled) {
		if r.o.ErrTTY {
			r.println(r.err, r.st.Muted.Render("interrupted"))
		} else {
			r.println(r.err, "interrupted")
		}
		return
	}
	se, ok := errors.AsType[*stack.Error](err)
	if !ok {
		if r.o.ErrTTY {
			r.println(r.err, r.st.Error.Render(markErr)+" "+Printable(err.Error()))
		} else {
			r.println(r.err, "error: "+Printable(err.Error()))
		}
		return
	}
	// Errors quote git and name files and branches; none of it may reach
	// the terminal as a control character.
	se = &stack.Error{Kind: se.Kind, Msg: Printable(se.Msg), Detail: se.Detail, NextSteps: printableAll(se.NextSteps)}
	if se.Detail != "" {
		lines := strings.Split(se.Detail, "\n")
		for i, l := range lines {
			lines[i] = Printable(l)
		}
		se.Detail = strings.Join(lines, "\n")
	}
	if r.o.ErrTTY {
		r.println(r.err, r.st.Error.Render(markErr)+" "+se.Msg)
		if se.Detail != "" {
			for line := range strings.SplitSeq(se.Detail, "\n") {
				r.println(r.err, "  "+r.st.Muted.Render(line))
			}
		}
		for _, step := range se.NextSteps {
			r.println(r.err, "  "+r.st.Muted.Render(markStep)+" "+step)
		}
		return
	}
	r.println(r.err, "error: "+se.Msg)
	if se.Detail != "" {
		for line := range strings.SplitSeq(se.Detail, "\n") {
			r.println(r.err, "  "+line)
		}
	}
	for _, step := range se.NextSteps {
		r.println(r.err, "  - "+step)
	}
}

// Print writes an already rendered block (a tree) to stdout as is.
func (r *Reporter) Print(block string) {
	if r.o.Quiet || block == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = io.WriteString(r.out, r.linkify(r.out, block))
}

// Styles returns the palette for stdout: the default styles on a terminal,
// the zero value (no escape codes) when piped.
func (r *Reporter) Styles() Styles {
	if r.o.OutTTY {
		return r.st
	}
	return Styles{}
}

// Links reports whether stdout gets OSC 8 hyperlinks. It is for blocks
// rendered elsewhere (the log tree, the picker) that go to stdout; lines
// printed through the Reporter are linked per stream on their own.
func (r *Reporter) Links() bool { return r.o.OutTTY }

// SetPRResolver sets how a pull request number found in a line (#123) is
// turned into a URL; it should only use local state, as it runs on the
// output path. Numbers it doesn't know ("") are left as text.
func (r *Reporter) SetPRResolver(fn func(number int) string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolve = fn
}

// PRURL is the URL of pull request number, or "" if nothing local knows
// it: the URLs passed to Ref, then the resolver. It's the one lookup every
// link uses, the log, the tree and the picker included (their options take
// it), so a PR is linked the same way wherever it's shown.
func (r *Reporter) PRURL(number int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prURL(number)
}

// prURL looks a PR number up for Linkify. Callers hold r.mu.
func (r *Reporter) prURL(n int) string {
	if u := r.prs[n]; u != "" {
		return u
	}
	if r.resolve != nil {
		return r.resolve(n)
	}
	return ""
}

// linkify makes the PR references and URLs in a line clickable, but only
// when the stream it is going to is a terminal. Callers hold r.mu.
func (r *Reporter) linkify(w io.Writer, line string) string {
	tty := (w == r.out && r.o.OutTTY) || (w == r.err && r.o.ErrTTY)
	if !tty {
		return line
	}
	return Linkify(line, r.prURL)
}

// Branch styles a branch name for use inside a message.
func (r *Reporter) Branch(name string) string {
	if !r.o.OutTTY {
		return name
	}
	return r.st.Current.Render(name)
}

// SHA styles a commit id for use inside a message: abbreviated to seven
// characters and faint.
func (r *Reporter) SHA(sha string) string {
	if len(sha) > shortSHA {
		sha = sha[:shortSHA]
	}
	if !r.o.OutTTY {
		return sha
	}
	return r.st.Muted.Render(sha)
}

// shortSHA is how many characters of a commit id we show.
const shortSHA = 7

// Ref styles a pull request reference (#123) for a stdout line in the
// colour of its state (the line's own colour when state is unknown) and
// remembers url, so the reference is a link, underlined, wherever it is
// printed to a terminal.
func (r *Reporter) Ref(number int, url string, state forge.State) string {
	label := fmt.Sprintf("#%d", number)
	if url != "" {
		r.mu.Lock()
		if r.prs == nil {
			r.prs = map[int]string{}
		}
		r.prs[number] = url
		r.mu.Unlock()
	}
	if !r.o.OutTTY || state == forge.StateUnknown {
		return label
	}
	return prStyle(r.st, state).Render(label)
}

// Link returns url for use in a line. Every URL printed through the
// Reporter becomes a link when its stream is a terminal; this only marks
// the intent at the call site.
func (r *Reporter) Link(url string) string { return url }

// Step runs fn under a headline for the phase: an emoji, the message and a
// spinner on a terminal (the line is replaced by whatever is printed next
// when fn returns), or the message printed once when piped. The terminal's
// busy state is set for the duration, and the window title shows the
// message until the next step or Finish, as does the program status. Output written to Stream while
// the step runs is shown above the spinner.
func (r *Reporter) Step(ctx context.Context, phase app.Phase, message string, fn func(ctx context.Context) error) error {
	if r.o.Quiet {
		return fn(ctx)
	}
	r.mu.Lock()
	r.term.working(message)
	r.mu.Unlock()
	defer Busy(r.err, r.o.ErrTTY)()
	headline := message
	if r.o.ErrTTY {
		if e, ok := phaseEmoji[phase]; ok {
			headline = e + " " + message
		}
	}
	if !r.o.Spinners || !r.o.ErrTTY {
		r.println(r.err, headline+"...")
		err := fn(ctx)
		r.gutter.flush()
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sp := startSpinner(ctx, cancel, r.in, r.rawErr, headline+"…")
	r.mu.Lock()
	r.active = sp
	r.mu.Unlock()
	err := fn(ctx)
	r.mu.Lock()
	r.gutter.flushLocked()
	r.active = nil
	r.mu.Unlock()
	sp.Stop()
	if err == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Waiting tells the terminal the command is blocked on the user answering
// question (a prompt or picker) and returns a function to call once they
// have.
func (r *Reporter) Waiting(question string) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.term.waiting(question)
}

// Finish puts back what steps and prompts changed about the terminal: it
// leaves the program status as done, error (err) or idle (interrupted) and
// restores the window title. Call it once the command is over, on every
// exit path; it is safe to call more than once and does nothing if no step
// or prompt ran.
func (r *Reporter) Finish(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.term.finish(err, r.result)
}

// Stream returns a writer for a subprocess's live output. Each line is shown
// in the "│" gutter on stderr (plain indent when piped), above the spinner
// while a Step is running.
func (r *Reporter) Stream() io.Writer { return &r.gutter }

func (r *Reporter) println(w io.Writer, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line = r.linkify(w, line)
	if r.active != nil && w == r.err {
		// A spinner owns the last line of stderr; print above it.
		r.active.Println(line)
		return
	}
	fmt.Fprintln(w, line)
}

// gutterWriter buffers partial lines and emits whole ones with the gutter
// mark, routing them above the spinner while one runs.
type gutterWriter struct {
	r   *Reporter
	buf bytes.Buffer
}

func (g *gutterWriter) Write(p []byte) (int, error) {
	g.r.mu.Lock()
	defer g.r.mu.Unlock()
	g.buf.Write(p)
	for {
		line, rest, ok := bytes.Cut(g.buf.Bytes(), []byte("\n"))
		if !ok {
			break
		}
		g.emit(string(line))
		g.buf.Reset()
		g.buf.Write(rest)
	}
	return len(p), nil
}

func (g *gutterWriter) flush() {
	g.r.mu.Lock()
	defer g.r.mu.Unlock()
	g.flushLocked()
}

func (g *gutterWriter) flushLocked() {
	if g.buf.Len() > 0 {
		g.emit(g.buf.String())
		g.buf.Reset()
	}
}

func (g *gutterWriter) emit(line string) {
	r := g.r
	if r.o.Quiet {
		return
	}
	// gh stack's output is relayed, not trusted: no controls reach the terminal.
	line = Printable(strings.TrimRight(line, "\r"))
	var out string
	if r.o.ErrTTY {
		out = r.st.Muted.Render(markPipe) + " " + line
	} else {
		out = "  " + line
	}
	out = r.linkify(r.err, out)
	if r.active != nil {
		r.active.Println(out)
		return
	}
	fmt.Fprintln(r.err, out)
}

func printableAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = Printable(s)
	}
	return out
}
