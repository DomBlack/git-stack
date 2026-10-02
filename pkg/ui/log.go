package ui

import (
	"strings"
	"time"

	"github.com/DomBlack/git-stack/pkg/app"
)

// LogOptions controls RenderLog.
type LogOptions struct {
	Styles Styles
	Now    time.Time
	// Links makes PR references OSC 8 hyperlinks.
	Links bool
}

// RenderLog renders every stack the way `gt log` does: trunk at the bottom,
// each stack a column rising from it, newest branch at the top, with the
// pull request and age under each branch name.
//
//	● feat-b
//	│  #13 draft · 2d ago
//	│
//	○ feat-a
//	│  #12 open · 3h ago
//	│
//	│ ○ hotfix
//	│ │  no PR · 1w ago
//	├─┘
//	■ main
//
// Untracked branches are not shown. It returns "" when there are no stacks.
func RenderLog(v *app.View, o LogOptions) string {
	if v == nil {
		return ""
	}
	type group struct {
		trunk  app.Row
		stacks [][]app.Row // bottom to top
	}
	var groups []*group
	var cur *group
	for _, r := range v.Rows {
		switch {
		case r.IsTrunk:
			cur = &group{trunk: r}
			groups = append(groups, cur)
		case !r.Tracked || cur == nil:
			continue
		case r.Depth == 1:
			cur.stacks = append(cur.stacks, []app.Row{r})
		default:
			if n := len(cur.stacks); n > 0 {
				cur.stacks[n-1] = append(cur.stacks[n-1], r)
			}
		}
	}

	st := o.Styles
	var b strings.Builder
	hasStacks := false
	for gi, g := range groups {
		if len(g.stacks) == 0 {
			continue
		}
		hasStacks = true
		if gi > 0 && b.Len() > 0 {
			b.WriteString("\n")
		}
		for k, s := range g.stacks {
			prefix := strings.Repeat(st.Prefix.Render("│")+" ", k)
			for i := len(s) - 1; i >= 0; i-- {
				r := s[i]
				b.WriteString(prefix + logMarker(r, st) + " " + logName(r, st) + "\n")
				b.WriteString(prefix + st.Prefix.Render("│") + "  " + logMeta(r, o) + "\n")
				b.WriteString(prefix + st.Prefix.Render("│") + "\n")
			}
		}
		if n := len(g.stacks); n > 1 {
			join := "├─" + strings.Repeat("┴─", n-2) + "┘"
			b.WriteString(st.Prefix.Render(join) + "\n")
		}
		b.WriteString(logMarker(g.trunk, st) + " " + logName(g.trunk, st))
		if age := RelativeTime(g.trunk.LastCommit, o.Now); age != "" {
			b.WriteString("  " + st.Muted.Render(age))
		}
		b.WriteString("\n")
	}
	if !hasStacks {
		return ""
	}
	return b.String()
}

func logMarker(r app.Row, st Styles) string {
	switch {
	case r.IsCurrent:
		return st.Current.Render("●")
	case r.IsTrunk:
		return st.Trunk.Render("■")
	default:
		return st.Muted.Render("○")
	}
}

func logName(r app.Row, st Styles) string {
	switch {
	case r.IsCurrent:
		return st.Current.Render(r.Name)
	case r.IsTrunk:
		return st.Trunk.Render(r.Name)
	default:
		return st.Branch.Render(r.Name)
	}
}

func logMeta(r app.Row, o LogOptions) string {
	st := o.Styles
	var parts []string
	if r.PR != nil {
		pr := renderPR(r.PR, st)
		if o.Links && r.PR.URL != "" {
			pr = Hyperlink(true, r.PR.URL, pr)
		}
		parts = append(parts, pr)
	} else {
		parts = append(parts, st.Muted.Render("no PR"))
	}
	if r.NeedsRestack {
		parts = append(parts, st.Restack.Render("needs restack"))
	}
	if age := RelativeTime(r.LastCommit, o.Now); age != "" {
		parts = append(parts, st.Muted.Render(age))
	}
	if r.Worktree != "" {
		parts = append(parts, st.Muted.Render("in "+r.Worktree))
	}
	return strings.Join(parts, st.Muted.Render(" · "))
}
