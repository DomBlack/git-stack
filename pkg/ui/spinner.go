package ui

import (
	"context"
	"io"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// spinnerModel shows a spinner and a message until told to stop.
type spinnerModel struct {
	sp      spinner.Model
	message string
	cancel  context.CancelFunc
	done    bool
	styles  Styles
	// ready is closed on the first View call, i.e. the renderer is up and
	// lines printed above the spinner land in the right place.
	ready chan struct{}
}

type (
	spinnerStopMsg struct{}
	spinnerQuitMsg struct{}
)

// stopGrace is how long the spinner waits between being told to stop and
// quitting, so lines queued with Println just before are flushed by the
// renderer rather than dropped on exit.
const stopGrace = 50 * time.Millisecond

func (m *spinnerModel) Init() tea.Cmd { return m.sp.Tick }

func (m *spinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinnerStopMsg:
		m.done = true
		return m, tea.Tick(stopGrace, func(time.Time) tea.Msg { return spinnerQuitMsg{} })
	case spinnerQuitMsg:
		return m, tea.Quit
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			// Cancel the work; Step notices the context and stops us.
			m.cancel()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.sp, cmd = m.sp.Update(msg)
	return m, cmd
}

func (m *spinnerModel) View() tea.View {
	if m.ready != nil {
		// First render: the renderer is live, Println lines will be placed above us.
		close(m.ready)
		m.ready = nil
	}
	if m.done {
		// Empty final frame: the inline renderer erases the spinner line and
		// whatever the caller prints next takes its place.
		return tea.NewView("")
	}
	return tea.NewView(m.sp.View() + " " + m.message)
}

// spinnerHandle is a running spinner program.
type spinnerHandle struct {
	prog *tea.Program
	done chan struct{}
}

// startSpinner runs a spinner on out until Stop is called. cancel is invoked
// on ctrl+c so the work being waited on can be interrupted.
func startSpinner(ctx context.Context, cancel context.CancelFunc, in io.Reader, out io.Writer, message string) *spinnerHandle {
	ready := make(chan struct{})
	m := &spinnerModel{sp: spinner.New(spinner.WithSpinner(spinner.Dot)), message: message, cancel: cancel, styles: DefaultStyles(), ready: ready}
	prog := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx))
	h := &spinnerHandle{prog: prog, done: make(chan struct{})}
	go func() {
		_, _ = prog.Run()
		close(h.done)
	}()
	// Don't hand the spinner back until it is rendering, otherwise output
	// printed above it in the first milliseconds leaves a stray blank line.
	select {
	case <-ready:
		// View has run but the first frame is flushed on the renderer's next
		// tick; a line printed above before that lands as a stray blank
		// line. One frame's grace avoids it.
		time.Sleep(firstFrame)
	case <-h.done:
	case <-time.After(startGrace):
	}
	return h
}

// startGrace bounds how long startSpinner waits for the program to come up;
// firstFrame is a little over one render tick at bubbletea's default rate.
const (
	startGrace = 250 * time.Millisecond
	firstFrame = 40 * time.Millisecond
)

// Println prints a line above the spinner.
func (h *spinnerHandle) Println(line string) { h.prog.Println(line) }

// Stop ends the spinner and waits for the program to exit.
func (h *spinnerHandle) Stop() {
	h.prog.Send(spinnerStopMsg{})
	<-h.done
}

// WithSpinner runs fn while showing a spinner and message on the terminal.
// It returns fn's error; ctrl+c cancels with context.Canceled.
func WithSpinner(ctx context.Context, in io.Reader, out io.Writer, message string, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h := startSpinner(ctx, cancel, in, out, message)
	err := fn(ctx)
	h.Stop()
	if err == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
