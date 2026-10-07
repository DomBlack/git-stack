package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

// TreeRow is a display row: the app row plus its tree prefix and, after
// filtering, the matched rune positions.
type TreeRow struct {
	Row app.Row
	// Prefix holds the tree glyphs ("├─ ", "│  └─ ").
	Prefix string
	// Matched positions in Row.Name when a filter is active.
	Matched []int
	// IsMatch is true when the row itself matched the filter (as opposed to
	// being kept as an ancestor of a match).
	IsMatch bool
}

// BuildTree computes tree prefixes for rows in display order. Stacks are
// linear, so only the branches directly above a trunk can have siblings.
func BuildTree(rows []app.Row) []TreeRow {
	out := make([]TreeRow, len(rows))
	// lastBottom[i] is true when row i is the last depth-1 branch under its trunk.
	lastBottom := make([]bool, len(rows))
	for i, r := range rows {
		if r.Depth != 1 {
			continue
		}
		last := true
		for _, later := range rows[i+1:] {
			if later.Depth == 0 {
				break
			}
			if later.Depth == 1 {
				last = false
				break
			}
		}
		lastBottom[i] = last
	}
	// bottomIsLast tracks the current stack's bottom flag as we descend.
	bottomIsLast := false
	for i, r := range rows {
		var b strings.Builder
		switch r.Depth {
		case 0:
		case 1:
			bottomIsLast = lastBottom[i]
			if bottomIsLast {
				b.WriteString("└─ ")
			} else {
				b.WriteString("├─ ")
			}
		default:
			if bottomIsLast {
				b.WriteString("   ")
			} else {
				b.WriteString("│  ")
			}
			for range r.Depth - 2 {
				b.WriteString("   ")
			}
			b.WriteString("└─ ")
		}
		out[i] = TreeRow{Row: r, Prefix: b.String()}
	}
	return out
}

// FuzzyMatch reports whether q is a case-insensitive subsequence of s and
// returns the matched rune indices.
func FuzzyMatch(s, q string) (bool, []int) {
	if q == "" {
		return true, nil
	}
	rs, rq := []rune(s), []rune(q)
	var idx []int
	j := 0
	for i, r := range rs {
		if j < len(rq) && unicode.ToLower(r) == unicode.ToLower(rq[j]) {
			idx = append(idx, i)
			j++
		}
	}
	if j != len(rq) {
		return false, nil
	}
	return true, idx
}

// FilterTree keeps rows matching query plus their ancestors, so the tree
// context survives filtering. An empty query returns every row.
func FilterTree(rows []TreeRow, query string) []TreeRow {
	if query == "" {
		out := make([]TreeRow, len(rows))
		copy(out, rows)
		for i := range out {
			out[i].Matched, out[i].IsMatch = nil, false
		}
		return out
	}
	keep := make([]bool, len(rows))
	matched := make([][]int, len(rows))
	for i, r := range rows {
		ok, idx := FuzzyMatch(r.Row.Name, query)
		if !ok {
			continue
		}
		keep[i] = true
		matched[i] = idx
		// Walk back to keep the nearest ancestor at each shallower depth.
		depth := r.Row.Depth
		for j := i - 1; j >= 0 && depth > 0; j-- {
			if rows[j].Row.Depth < depth && rows[j].Row.Tracked {
				keep[j] = true
				depth = rows[j].Row.Depth
			}
		}
	}
	var out []TreeRow
	for i, r := range rows {
		if !keep[i] {
			continue
		}
		r.Matched = matched[i]
		r.IsMatch = matched[i] != nil
		out = append(out, r)
	}
	return out
}

// RenderOptions controls RenderRow.
type RenderOptions struct {
	Styles Styles
	Now    time.Time
	// Selected highlights the row (picker cursor).
	Selected bool
	// Links makes PR references OSC 8 hyperlinks.
	Links bool
	// PRURL finds the URL of a PR the row only has a number for; usually
	// Reporter.PRURL.
	PRURL func(number int) string
}

// RenderRow renders one tree row: prefix, name, PR, restack marker, age and
// worktree.
func RenderRow(tr TreeRow, o RenderOptions) string {
	st := o.Styles
	r := tr.Row

	nameStyle := st.Branch
	switch {
	case r.IsCurrent:
		nameStyle = st.Current
	case r.IsTrunk:
		nameStyle = st.Trunk
	case !r.Tracked:
		nameStyle = st.Untracked
	}
	name := nameStyle.Render(r.Name)
	if len(tr.Matched) > 0 {
		name = lipgloss.StyleRunes(r.Name, tr.Matched, st.Match.Inherit(nameStyle), nameStyle)
	}
	marker := st.Muted.Render("○") + " "
	switch {
	case r.IsCurrent:
		marker = st.Current.Render("●") + " "
	case r.IsTrunk:
		marker = st.Trunk.Render("■") + " "
	}

	var meta []string
	if r.PR != nil {
		meta = append(meta, Hyperlink(o.Links, prURL(o.Links, r.PR, o.PRURL), renderPR(r.PR, st)))
	}
	if r.NeedsRestack {
		meta = append(meta, st.Restack.Render("needs restack"))
	}
	if r.NeedsPush {
		meta = append(meta, st.Restack.Render("needs push"))
	}
	if !r.Tracked && !r.IsTrunk {
		meta = append(meta, st.Muted.Render("untracked"))
	}
	if age := RelativeTime(r.LastCommit, o.Now); age != "" {
		meta = append(meta, st.Muted.Render(age))
	}
	if r.Worktree != "" {
		meta = append(meta, st.Muted.Render("in "+ShortPath(r.Worktree)))
	}

	line := marker + st.Prefix.Render(tr.Prefix) + name
	if len(meta) > 0 {
		line += "  " + strings.Join(meta, st.Muted.Render(" · "))
	}
	if o.Selected {
		return st.Selected.Render(line)
	}
	return line
}

func renderPR(pr *forge.PullRequest, st Styles) string {
	label := fmt.Sprintf("#%d", pr.Number)
	if pr.State == forge.StateUnknown {
		return st.Muted.Render(label)
	}
	return prStyle(st, pr.State).Render(label + " " + string(pr.State))
}

// prStyle is the colour for a pull request in state; the zero style (the
// line's own colour) when the state isn't known.
func prStyle(st Styles, state forge.State) lipgloss.Style {
	switch state {
	case forge.StateOpen:
		return st.PROpen
	case forge.StateDraft:
		return st.PRDraft
	case forge.StateMerged:
		return st.PRMerged
	case forge.StateClosed:
		return st.PRClosed
	default:
		return lipgloss.NewStyle()
	}
}

// RenderTree renders every row, one per line, without selection.
func RenderTree(rows []TreeRow, o RenderOptions) string {
	var b strings.Builder
	o.Selected = false
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(RenderRow(r, o))
	}
	return b.String()
}

// prURL is the PR's own URL, or what resolve knows for its number; it
// only looks when the PR is going to be linked.
func prURL(links bool, pr *forge.PullRequest, resolve func(int) string) string {
	if !links || pr.URL != "" || resolve == nil {
		return pr.URL
	}
	return resolve(pr.Number)
}
