package ui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ReorderOptions configures the reorder list.
type ReorderOptions struct {
	Trunk string
	// Branches run bottom to top, as the stack is stored.
	Branches []string
	Current  string
	// PRs maps a branch to its pull request number, when known.
	PRs    map[string]int
	Styles *Styles
}

// ReorderResult is the user's choice.
type ReorderResult struct {
	// Order is the new order, bottom to top.
	Order       []string
	Cancelled   bool
	Interrupted bool
}

// Reorder is the bubbletea model: the stack shown top first, a cursor,
// and keys that move the branch under it. Exported for tests.
type Reorder struct {
	trunk    string
	original []string // bottom to top
	rows     []string // display order, top first
	current  string
	prs      map[string]int
	cursor   int
	styles   Styles
	result   ReorderResult
}

// NewReorder builds the model with the cursor on the current branch.
func NewReorder(o ReorderOptions) *Reorder {
	rows := slices.Clone(o.Branches)
	slices.Reverse(rows)
	st := DefaultStyles()
	if o.Styles != nil {
		st = *o.Styles
	}
	r := &Reorder{trunk: o.Trunk, original: slices.Clone(o.Branches), rows: rows, current: o.Current, prs: o.PRs, styles: st}
	if i := slices.Index(rows, o.Current); i >= 0 {
		r.cursor = i
	}
	return r
}

// Init has nothing to start.
func (r *Reorder) Init() tea.Cmd { return nil }

// Update handles keys.
func (r *Reorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return r, nil
	}
	shifted := key.Mod&tea.ModShift != 0
	switch {
	case key.Code == tea.KeyUp && shifted:
		r.swap(r.cursor - 1)
		return r, nil
	case key.Code == tea.KeyDown && shifted:
		r.swap(r.cursor + 1)
		return r, nil
	}
	switch key.String() {
	case "ctrl+c":
		r.result = ReorderResult{Cancelled: true, Interrupted: true}
		return r, tea.Quit
	case "esc", "q":
		r.result = ReorderResult{Cancelled: true}
		return r, tea.Quit
	case "enter":
		order := slices.Clone(r.rows)
		slices.Reverse(order)
		r.result = ReorderResult{Order: order}
		return r, tea.Quit
	case "up", "k":
		r.cursor = max(r.cursor-1, 0)
	case "down", "j":
		r.cursor = min(r.cursor+1, len(r.rows)-1)
	case "K":
		r.swap(r.cursor - 1)
	case "J":
		r.swap(r.cursor + 1)
	case "home":
		r.cursor = 0
	case "end":
		r.cursor = len(r.rows) - 1
	}
	return r, nil
}

// swap moves the branch under the cursor to row i, keeping the cursor on it.
func (r *Reorder) swap(i int) {
	if i < 0 || i >= len(r.rows) {
		return
	}
	r.rows[r.cursor], r.rows[i] = r.rows[i], r.rows[r.cursor]
	r.cursor = i
}

// View renders the list inline.
func (r *Reorder) View() tea.View {
	st := r.styles
	var b strings.Builder
	b.WriteString(st.Title.Render("Reorder the stack"))
	b.WriteString("\n")
	n := len(r.rows)
	for i, name := range r.rows {
		cursor := "  "
		if i == r.cursor {
			cursor = st.Selected.Render("❯ ")
		}
		label := st.Branch.Render(name)
		if name == r.current {
			label = st.Current.Render(name) + st.Muted.Render("  current")
		}
		if pr, ok := r.prs[name]; ok {
			label += st.Muted.Render(fmt.Sprintf("  #%d", pr))
		}
		// original is bottom to top; row i is position n-1-i from the bottom.
		if r.original[n-1-i] != name {
			label += st.Warning.Render("  moved")
		}
		b.WriteString(cursor + label + "\n")
	}
	b.WriteString("  " + st.Trunk.Render(r.trunk) + st.Muted.Render("  trunk") + "\n")
	b.WriteString(st.Help.Render("↑/↓ or j/k move cursor · shift+↑/↓ or K/J move branch · enter apply · esc cancel"))
	v := tea.NewView(b.String())
	v.AltScreen = false
	return v
}

// Result returns the choice once the program has finished.
func (r *Reorder) Result() ReorderResult { return r.result }

// RunReorder runs the list on the given terminal streams. Ctrl-C (or a
// cancelled context) is an interrupt and comes back as context.Canceled;
// Escape is a cancel and returns no error.
func RunReorder(ctx context.Context, in io.Reader, out io.Writer, o ReorderOptions) (ReorderResult, error) {
	m := NewReorder(o)
	prog := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx))
	final, err := prog.Run()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return ReorderResult{Cancelled: true, Interrupted: true}, err
	}
	res := m.Result()
	if fr, ok := final.(*Reorder); ok {
		res = fr.Result()
	}
	if res.Interrupted {
		return res, context.Canceled
	}
	return res, nil
}
