package ui

import (
	"context"
	"io"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// spinnerDoneMsg carries the result of the wrapped function.
type spinnerDoneMsg struct{ err error }

// spinnerModel shows a spinner and a message while fn runs.
type spinnerModel struct {
	sp      spinner.Model
	message string
	fn      func(ctx context.Context) error
	ctx     context.Context
	err     error
	done    bool
	styles  Styles
}

func newSpinnerModel(ctx context.Context, message string, fn func(ctx context.Context) error) *spinnerModel {
	return &spinnerModel{sp: spinner.New(spinner.WithSpinner(spinner.Dot)), message: message, fn: fn, ctx: ctx, styles: DefaultStyles()}
}

func (m *spinnerModel) Init() tea.Cmd {
	fn, ctx := m.fn, m.ctx
	return tea.Batch(m.sp.Tick, func() tea.Msg { return spinnerDoneMsg{err: fn(ctx)} })
}

func (m *spinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinnerDoneMsg:
		m.err = msg.err
		m.done = true
		return m, tea.Quit
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.err = context.Canceled
			m.done = true
			return m, tea.Quit
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.sp, cmd = m.sp.Update(msg)
	return m, cmd
}

func (m *spinnerModel) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	return tea.NewView(m.sp.View() + " " + m.styles.Muted.Render(m.message))
}

// WithSpinner runs fn while showing a spinner and message on the terminal.
// It returns fn's error; ctrl+c cancels with context.Canceled.
func WithSpinner(ctx context.Context, in io.Reader, out io.Writer, message string, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newSpinnerModel(ctx, message, fn)
	prog := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx))
	final, err := prog.Run()
	if err != nil && m.err == nil {
		return err
	}
	if fm, ok := final.(*spinnerModel); ok {
		return fm.err
	}
	return m.err
}
