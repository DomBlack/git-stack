package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Prompter asks yes/no and pick-one questions on the terminal. It satisfies
// app.Prompter.
type Prompter struct {
	In  io.Reader
	Out io.Writer
	Ctx context.Context
	// Wait, when set, is told a question is waiting on the user; the
	// function it returns is called once it has been answered. The CLI
	// passes Reporter.Waiting so the terminal can show it.
	Wait func(question string) func()
}

// ErrCancelled is returned when the user dismisses a prompt. Dismissing it
// with Ctrl-C also matches context.Canceled, so the command ends as
// interrupted (exit 130), like any other Ctrl-C.
var ErrCancelled = fmt.Errorf("cancelled")

// errInterrupted is a prompt dismissed with Ctrl-C.
var errInterrupted = fmt.Errorf("%w: %w", ErrCancelled, context.Canceled)

// Confirm asks a yes/no question.
func (p Prompter) Confirm(question string, defaultYes bool) (bool, error) {
	defer p.waiting(question)()
	m := &confirmModel{question: question, value: defaultYes, styles: DefaultStyles()}
	final, err := tea.NewProgram(m, tea.WithInput(p.In), tea.WithOutput(p.Out), tea.WithContext(p.ctx())).Run()
	if err != nil {
		return false, err
	}
	fm := final.(*confirmModel)
	answer := "no"
	switch {
	case fm.cancelled:
		answer = "cancelled"
	case fm.value:
		answer = "yes"
	}
	p.summary(question, answer)
	if fm.cancelled {
		return false, fm.cancelErr()
	}
	return fm.value, nil
}

// Select asks the user to pick one option and returns its index.
func (p Prompter) Select(question string, options []string) (int, error) {
	defer p.waiting(question)()
	m := &selectModel{question: question, options: options, styles: DefaultStyles()}
	final, err := tea.NewProgram(m, tea.WithInput(p.In), tea.WithOutput(p.Out), tea.WithContext(p.ctx())).Run()
	if err != nil {
		return -1, err
	}
	fm := final.(*selectModel)
	// The final frame is padded to the height of the interactive one (see
	// selectModel.View); bubbletea leaves the cursor on its last line, so
	// move up to the line after the summary and erase the padding.
	fmt.Fprint(p.Out, ansi.CursorUp(len(options))+ansi.EraseScreenBelow)
	if fm.cancelled {
		return -1, fm.cancelErr()
	}
	return fm.cursor, nil
}

// summary prints the answered question on its own line once the program has
// exited. Confirm renders an empty final frame and prints this instead,
// because bubbletea's inline renderer erases a final frame that has no
// trailing newline.
func (p Prompter) summary(question, answer string) {
	s := DefaultStyles()
	fmt.Fprintln(p.Out, s.Title.Render("? ")+question+" "+s.Muted.Render(answer))
}

func (p Prompter) waiting(question string) func() {
	if p.Wait == nil {
		return func() {}
	}
	return p.Wait(question)
}

func (p Prompter) ctx() context.Context {
	if p.Ctx == nil {
		return context.Background()
	}
	return p.Ctx
}

type confirmModel struct {
	question  string
	value     bool
	done      bool
	cancelled bool
	// interrupted: cancelled with Ctrl-C rather than Escape or q.
	interrupted bool
	styles      Styles
}

func (m *confirmModel) Init() tea.Cmd { return nil }

func (m *confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "y", "Y":
		m.value, m.done = true, true
		return m, tea.Quit
	case "n", "N":
		m.value, m.done = false, true
		return m, tea.Quit
	case "enter":
		m.done = true
		return m, tea.Quit
	case "ctrl+c":
		m.cancelled, m.interrupted, m.done = true, true, true
		return m, tea.Quit
	case "esc", "q":
		m.cancelled, m.done = true, true
		return m, tea.Quit
	}
	return m, nil
}

func (m *confirmModel) View() tea.View {
	hint := "[y/N]"
	if m.value {
		hint = "[Y/n]"
	}
	if m.done {
		return tea.NewView("")
	}
	return tea.NewView(m.styles.Title.Render("? ") + m.question + " " + m.styles.Muted.Render(hint) + " ")
}

type selectModel struct {
	question  string
	options   []string
	cursor    int
	done      bool
	cancelled bool
	// interrupted: cancelled with Ctrl-C rather than Escape or q.
	interrupted bool
	styles      Styles
}

func (m *selectModel) Init() tea.Cmd { return nil }

func (m *selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k", "ctrl+p":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j", "ctrl+n":
		m.cursor = min(m.cursor+1, len(m.options)-1)
	case "enter":
		m.done = true
		return m, tea.Quit
	case "ctrl+c":
		m.cancelled, m.interrupted, m.done = true, true, true
		return m, tea.Quit
	case "esc", "q":
		m.cancelled, m.done = true, true
		return m, tea.Quit
	default:
		if n := int(key.Code - '0'); key.Text != "" && n >= 1 && n <= len(m.options) {
			m.cursor = n - 1
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *selectModel) View() tea.View {
	var b strings.Builder
	b.WriteString(m.styles.Title.Render("? ") + m.question)
	if m.done {
		// bubbletea's inline renderer mispositions a final frame that is
		// shorter than the previous one (the question showed up twice), so
		// keep the height: the summary line plus blank padding, which
		// Prompter.Select erases after the program exits.
		answer := "cancelled"
		if !m.cancelled {
			answer = m.options[m.cursor]
		}
		b.WriteString(" " + m.styles.Muted.Render(answer) + "\n")
		b.WriteString(strings.Repeat("\n", len(m.options)))
		return tea.NewView(b.String())
	}
	b.WriteString("\n")
	for i, o := range m.options {
		line := fmt.Sprintf("  %d. %s", i+1, o)
		if i == m.cursor {
			line = m.styles.Current.Render(fmt.Sprintf("> %d. %s", i+1, o))
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(m.styles.Help.Render("↑/↓ move · enter select · esc cancel"))
	return tea.NewView(b.String())
}

// cancelErr is what a dismissed prompt returns: an interrupt for Ctrl-C,
// otherwise a plain cancel.
func (m *confirmModel) cancelErr() error { return cancelErr(m.interrupted) }

func (m *selectModel) cancelErr() error { return cancelErr(m.interrupted) }

func cancelErr(interrupted bool) error {
	if interrupted {
		return errInterrupted
	}
	return ErrCancelled
}
