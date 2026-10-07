package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

// LogOptions controls RenderLog.
type LogOptions struct {
	Styles Styles
	Now    time.Time
	// Links makes PR references OSC 8 hyperlinks.
	Links bool
	// PRURL finds the URL of a PR the row only has a number for (the forge
	// couldn't be reached and the stack metadata has no URL); usually
	// Reporter.PRURL.
	PRURL func(number int) string
	// Width is the terminal's width; a PR title or commit subject that
	// would run past it is cut short with "…". 0 never cuts (not a
	// terminal).
	Width int
}

// RenderLog renders every stack as a tree: trunk at the bottom, each stack a
// column rising from it, newest branch at the top. Under each branch name
// come its pull request and title, then its state, age and checkout, then
// its own commits, newest first (at most app.MaxCommits, then a count).
//
//	● feat-b
//	│  #13 Retry failed payments
//	│  draft · 2d ago
//	│
//	│  • 7ac32ef - Back off between retries
//	│  • 4f21abc - Retry failed payments
//	│
//	○ feat-a
//	│  #12 Add the payments queue
//	│  open · needs restack · 3h ago
//	│
//	│  • 9b1d2e0 - Add the payments queue
//	│
//	│ ○ hotfix
//	│ │  no PR
//	│ │  1w ago · in ~/src/app-hotfix
//	│ │
//	│ │  • 3c4d5e6 - Fix the typo in the banner
//	│ │
//	├─┘
//	■ main
//
// A trunk with no stacks is still drawn, so an empty repository shows where a
// stack would start. Untracked branches are not shown. It returns "" when
// there is no trunk at all.
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
	for gi, g := range groups {
		if gi > 0 {
			b.WriteString("\n")
		}
		for k, s := range g.stacks {
			prefix := strings.Repeat(st.Prefix.Render("│")+" ", k)
			gutter := prefix + st.Prefix.Render("│")
			for i := len(s) - 1; i >= 0; i-- {
				r := s[i]
				b.WriteString(prefix + logMarker(r, st) + " " + logName(r, st) + "\n")
				pr, title := logPR(r, o)
				b.WriteString(fitLine(gutter+"  "+pr, title, o.Width) + "\n")
				if meta := logMeta(r, o); meta != "" {
					b.WriteString(gutter + "  " + meta + "\n")
				}
				if len(r.Commits) > 0 {
					b.WriteString(gutter + "\n")
					for _, c := range r.Commits {
						head := gutter + "  " + st.Muted.Render("•") + " " + st.Muted.Render(shortCommit(c.SHA)) + " - "
						b.WriteString(fitLine(head, logText(c.Subject), o.Width) + "\n")
					}
					if r.MoreCommits > 0 {
						b.WriteString(gutter + "  " + st.Muted.Render(fmt.Sprintf("• and %d more", r.MoreCommits)) + "\n")
					}
				}
				b.WriteString(gutter + "\n")
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

// logPR is the first line under a branch: its pull request number (a link,
// in the colour of its state) and the title to put after it, or "no PR".
// The title is escaped but not styled, so fitLine can cut it.
func logPR(r app.Row, o LogOptions) (pr, title string) {
	st := o.Styles
	if r.PR == nil {
		return st.Muted.Render("no PR"), ""
	}
	label := fmt.Sprintf("#%d", r.PR.Number)
	if r.PR.State == forge.StateUnknown {
		label = st.Muted.Render(label)
	} else {
		label = prStyle(st, r.PR.State).Render(label)
	}
	pr = Hyperlink(o.Links, prURL(o.Links, r.PR, o.PRURL), label)
	if r.PR.Title == "" {
		// A cache from before titles were kept has only the number.
		return pr, ""
	}
	return pr + " ", logText(r.PR.Title)
}

// logText makes text from the repo or the forge (a commit subject, a PR
// title) safe for one line of the tree: control characters escaped, and
// tabs, which would throw out the width, as spaces.
func logText(s string) string {
	return strings.ReplaceAll(Printable(s), "\t", " ")
}

// fitLine is head followed by text, with text cut short ("…") so the line
// fits in width columns. head may hold styles and links; text must be
// plain. width 0 never cuts.
func fitLine(head, text string, width int) string {
	if width <= 0 || text == "" {
		return head + text
	}
	room := width - lipgloss.Width(head)
	if lipgloss.Width(text) <= room {
		return head + text
	}
	return head + ansi.Truncate(text, max(room, 1), "…")
}

// shortCommit is a commit id cut to the length we always show.
func shortCommit(sha string) string {
	return sha[:min(len(sha), shortSHA)]
}

// logMeta is the second line under a branch: the pull request's state, then
// whether it needs a restack or a push, its age and the worktree it's
// checked out in. "" when there's none of that to say.
func logMeta(r app.Row, o LogOptions) string {
	st := o.Styles
	var parts []string
	if r.PR != nil && r.PR.State != forge.StateUnknown {
		parts = append(parts, prStyle(st, r.PR.State).Render(string(r.PR.State)))
	}
	if r.NeedsRestack {
		parts = append(parts, st.Restack.Render("needs restack"))
	}
	if r.NeedsPush {
		parts = append(parts, st.Restack.Render("needs push"))
	}
	if age := RelativeTime(r.LastCommit, o.Now); age != "" {
		parts = append(parts, st.Muted.Render(age))
	}
	if r.Worktree != "" {
		parts = append(parts, st.Muted.Render("in "+ShortPath(r.Worktree)))
	}
	return strings.Join(parts, st.Muted.Render(" · "))
}
