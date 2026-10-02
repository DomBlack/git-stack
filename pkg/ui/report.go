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

	mu     sync.Mutex
	active *spinnerHandle // running Step, if any
	gutter gutterWriter
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
	r := &Reporter{in: in, out: out, err: err, rawErr: rawErr, o: o, st: DefaultStyles()}
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
}

// Success prints a result line on stdout: "✔ Created feat-a on main".
func (r *Reporter) Success(format string, args ...any) {
	if r.o.Quiet {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if r.o.OutTTY {
		r.println(r.out, r.st.Success.Render(markOK)+" "+msg)
		return
	}
	r.println(r.out, "ok: "+msg)
}

// Info prints a detail line on stdout, indented under the result line.
func (r *Reporter) Info(format string, args ...any) {
	if r.o.Quiet {
		return
	}
	r.println(r.out, "  "+fmt.Sprintf(format, args...))
}

// Warn prints a notice on stderr: "⚠ gh stack submits the whole stack".
func (r *Reporter) Warn(format string, args ...any) {
	if r.o.Quiet {
		return
	}
	msg := fmt.Sprintf(format, args...)
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
			r.println(r.err, r.st.Error.Render(markErr)+" "+err.Error())
		} else {
			r.println(r.err, "error: "+err.Error())
		}
		return
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
	_, _ = io.WriteString(r.out, block)
}

// Styles returns the palette for stdout: the default styles on a terminal,
// the zero value (no escape codes) when piped.
func (r *Reporter) Styles() Styles {
	if r.o.OutTTY {
		return r.st
	}
	return Styles{}
}

// Links reports whether stdout gets OSC 8 hyperlinks.
func (r *Reporter) Links() bool { return r.o.OutTTY }

// Branch styles a branch name for use inside a message.
func (r *Reporter) Branch(name string) string {
	if !r.o.OutTTY {
		return name
	}
	return r.st.Current.Render(name)
}

// SHA styles a commit id for use inside a message.
func (r *Reporter) SHA(sha string) string {
	if !r.o.OutTTY {
		return sha
	}
	return r.st.Muted.Render(sha)
}

// Ref styles a pull request reference (#123) and links it to url.
func (r *Reporter) Ref(number int, url string) string {
	label := fmt.Sprintf("#%d", number)
	if !r.o.OutTTY {
		return label
	}
	return Hyperlink(true, url, r.st.PROpen.Render(label))
}

// Link makes url clickable on a terminal and returns it unchanged otherwise.
func (r *Reporter) Link(url string) string {
	return Hyperlink(r.o.OutTTY, url, url)
}

// Step runs fn under a headline for the phase: an emoji, the message and a
// spinner on a terminal (the line is replaced by whatever is printed next
// when fn returns), or the message printed once when piped. The terminal's
// busy state is set for the duration. Output written to Stream while the
// step runs is shown above the spinner.
func (r *Reporter) Step(ctx context.Context, phase app.Phase, message string, fn func(ctx context.Context) error) error {
	if r.o.Quiet {
		return fn(ctx)
	}
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

// Stream returns a writer for a subprocess's live output. Each line is shown
// in the "│" gutter on stderr (plain indent when piped), above the spinner
// while a Step is running.
func (r *Reporter) Stream() io.Writer { return &r.gutter }

func (r *Reporter) println(w io.Writer, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	line = strings.TrimRight(line, "\r")
	var out string
	if r.o.ErrTTY {
		out = r.st.Muted.Render(markPipe) + " " + line
	} else {
		out = "  " + line
	}
	if r.active != nil {
		r.active.Println(out)
		return
	}
	fmt.Fprintln(r.err, out)
}
