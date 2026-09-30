package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Prompter asks yes/no and pick-one questions on the terminal. It satisfies
// app.Prompter.
type Prompter struct {
	In  io.Reader
	Out io.Writer
	Ctx context.Context
}

// ErrCancelled is returned when the user dismisses a prompt.
var ErrCancelled = fmt.Errorf("cancelled")

// Confirm asks a yes/no question.
func (p Prompter) Confirm(question string, defaultYes bool) (bool, error) {
	m := &confirmModel{question: question, value: defaultYes, styles: DefaultStyles()}
	final, err := tea.NewProgram(m, tea.WithInput(p.In), tea.WithOutput(p.Out), tea.WithContext(p.ctx())).Run()
	if err != nil {
		return false, err
	}
	fm := final.(*confirmModel)
	if fm.cancelled {
		return false, ErrCancelled
	}
	return fm.value, nil
}

// Select asks the user to pick one option and returns its index.
func (p Prompter) Select(question string, options []string) (int, error) {
	m := &selectModel{question: question, options: options, styles: DefaultStyles()}
	final, err := tea.NewProgram(m, tea.WithInput(p.In), tea.WithOutput(p.Out), tea.WithContext(p.ctx())).Run()
	if err != nil {
		return -1, err
	}
	fm := final.(*selectModel)
	if fm.cancelled {
		return -1, ErrCancelled
	}
	return fm.cursor, nil
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
	styles    Styles
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
	case "esc", "ctrl+c", "q":
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
		answer := "no"
		if m.value {
			answer = "yes"
		}
		if m.cancelled {
			answer = "cancelled"
		}
		return tea.NewView(m.styles.Title.Render("? ") + m.question + " " + m.styles.Muted.Render(answer) + "\n")
	}
	return tea.NewView(m.styles.Title.Render("? ") + m.question + " " + m.styles.Muted.Render(hint) + " ")
}

type selectModel struct {
	question  string
	options   []string
	cursor    int
	done      bool
	cancelled bool
	styles    Styles
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
	case "esc", "ctrl+c", "q":
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
		if m.cancelled {
			b.WriteString(" " + m.styles.Muted.Render("cancelled") + "\n")
		} else {
			b.WriteString(" " + m.styles.Muted.Render(m.options[m.cursor]) + "\n")
		}
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
