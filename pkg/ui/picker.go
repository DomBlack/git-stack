package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

// PickerOptions configures the checkout picker.
type PickerOptions struct {
	Rows []app.Row
	// Refresh, when set, is run in the background; its result updates the
	// PR decorations while the picker is open.
	Refresh func(ctx context.Context) ([]forge.PullRequest, error)
	// Styles defaults to DefaultStyles when nil.
	Styles *Styles
	Now    time.Time
	// Height limits the number of visible rows (0: derive from the terminal).
	Height int
	// Links makes PR references OSC 8 hyperlinks; set it when the picker's
	// output stream is a terminal.
	Links bool
	// PRURL finds the URL of a PR a row only has a number for; usually
	// Reporter.PRURL.
	PRURL func(number int) string
}

// PickerResult is the user's choice.
type PickerResult struct {
	Branch    string
	Cancelled bool
}

// prsRefreshedMsg carries background forge results.
type prsRefreshedMsg struct {
	prs []forge.PullRequest
	err error
}

// Picker is the bubbletea model. It is exported for tests.
type Picker struct {
	all     []TreeRow
	visible []TreeRow
	cursor  int
	offset  int
	filter  textinput.Model
	styles  Styles
	now     time.Time
	refresh func(ctx context.Context) ([]forge.PullRequest, error)
	ctx     context.Context
	height  int
	width   int
	links   bool
	prURL   func(int) string
	status  string
	result  PickerResult
}

// NewPicker builds the model.
func NewPicker(ctx context.Context, o PickerOptions) *Picker {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.Placeholder = "type to filter"
	ti.SetVirtualCursor(false)
	ti.SetWidth(40)
	ti.Focus()
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	styles := DefaultStyles()
	if o.Styles != nil {
		styles = *o.Styles
	}
	p := &Picker{
		all:     BuildTree(o.Rows),
		filter:  ti,
		styles:  styles,
		now:     o.Now,
		refresh: o.Refresh,
		ctx:     ctx,
		height:  o.Height,
		links:   o.Links,
		prURL:   o.PRURL,
	}
	p.visible = FilterTree(p.all, "")
	p.cursor = p.indexOfCurrent()
	return p
}

func (p *Picker) indexOfCurrent() int {
	for i, r := range p.visible {
		if r.Row.IsCurrent {
			return i
		}
	}
	return 0
}

// Init starts the background refresh, if any.
func (p *Picker) Init() tea.Cmd {
	if p.refresh == nil {
		return nil
	}
	p.status = "refreshing pull requests…"
	refresh, ctx := p.refresh, p.ctx
	return func() tea.Msg {
		prs, err := refresh(ctx)
		return prsRefreshedMsg{prs: prs, err: err}
	}
}

// Update handles keys, window size and refresh results.
func (p *Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		if p.height == 0 {
			p.height = max(msg.Height-4, 3)
		}
		return p, nil
	case prsRefreshedMsg:
		if msg.err != nil {
			p.status = "could not refresh pull requests: " + msg.err.Error()
			return p, nil
		}
		p.status = ""
		rows := make([]app.Row, len(p.all))
		for i, r := range p.all {
			rows[i] = r.Row
		}
		v := &app.View{Rows: rows}
		v.ApplyPRs(msg.prs)
		for i := range p.all {
			p.all[i].Row = v.Rows[i]
		}
		p.applyFilter()
		return p, nil
	case tea.KeyPressMsg:
		return p.handleKey(msg)
	}
	return p, nil
}

func (p *Picker) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	filterEmpty := p.filter.Value() == ""
	switch key := msg.String(); key {
	case "ctrl+c", "esc":
		p.result = PickerResult{Cancelled: true}
		return p, tea.Quit
	case "q":
		if filterEmpty {
			p.result = PickerResult{Cancelled: true}
			return p, tea.Quit
		}
	case "enter":
		if len(p.visible) > 0 {
			p.result = PickerResult{Branch: p.visible[p.cursor].Row.Name}
		} else {
			p.result = PickerResult{Cancelled: true}
		}
		return p, tea.Quit
	case "up", "ctrl+p", "ctrl+k":
		p.move(-1)
		return p, nil
	case "down", "ctrl+n", "ctrl+j":
		p.move(1)
		return p, nil
	case "pgup", "ctrl+u":
		p.move(-max(p.height, 1))
		return p, nil
	case "pgdown", "ctrl+d":
		p.move(max(p.height, 1))
		return p, nil
	case "home":
		p.move(-len(p.visible))
		return p, nil
	case "end":
		p.move(len(p.visible))
		return p, nil
	case "k":
		if filterEmpty {
			p.move(-1)
			return p, nil
		}
	case "j":
		if filterEmpty {
			p.move(1)
			return p, nil
		}
	}
	var cmd tea.Cmd
	p.filter, cmd = p.filter.Update(msg)
	p.applyFilter()
	return p, cmd
}

func (p *Picker) move(delta int) {
	if len(p.visible) == 0 {
		p.cursor = 0
		return
	}
	p.cursor = min(max(p.cursor+delta, 0), len(p.visible)-1)
}

func (p *Picker) applyFilter() {
	var selected string
	if p.cursor < len(p.visible) {
		selected = p.visible[p.cursor].Row.Name
	}
	p.visible = FilterTree(p.all, p.filter.Value())
	p.cursor = 0
	for i, r := range p.visible {
		if r.Row.Name == selected {
			p.cursor = i
			return
		}
	}
	// Prefer the first real match over a retained ancestor.
	for i, r := range p.visible {
		if r.IsMatch {
			p.cursor = i
			return
		}
	}
}

// View renders the picker inline.
func (p *Picker) View() tea.View {
	var b strings.Builder
	st := p.styles
	b.WriteString(st.Title.Render("Select a branch"))
	if p.status != "" {
		b.WriteString("  " + st.Muted.Render(p.status))
	}
	b.WriteString("\n")
	b.WriteString(p.filter.View())
	b.WriteString("\n")

	height := p.height
	if height <= 0 {
		height = len(p.visible)
	}
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+height {
		p.offset = p.cursor - height + 1
	}
	end := min(p.offset+height, len(p.visible))
	if len(p.visible) == 0 {
		b.WriteString(st.Muted.Render("  no branches match"))
		b.WriteString("\n")
	}
	for i := p.offset; i < end; i++ {
		b.WriteString(RenderRow(p.visible[i], RenderOptions{Styles: st, Now: p.now, Selected: i == p.cursor, Links: p.links, PRURL: p.prURL}))
		b.WriteString("\n")
	}
	if len(p.visible) > height {
		b.WriteString(st.Muted.Render(fmt.Sprintf("  %d/%d", p.cursor+1, len(p.visible))))
		b.WriteString("\n")
	}
	b.WriteString(st.Help.Render("↑/↓ or j/k move · type to filter · enter switch · esc cancel"))

	v := tea.NewView(b.String())
	v.AltScreen = false
	return v
}

// Result returns the choice once the program has finished.
func (p *Picker) Result() PickerResult { return p.result }

// RunPicker runs the picker on the given terminal streams.
func RunPicker(ctx context.Context, in io.Reader, out io.Writer, o PickerOptions) (PickerResult, error) {
	m := NewPicker(ctx, o)
	prog := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx))
	final, err := prog.Run()
	if err != nil {
		return PickerResult{Cancelled: true}, err
	}
	if fp, ok := final.(*Picker); ok {
		return fp.Result(), nil
	}
	return m.Result(), nil
}
