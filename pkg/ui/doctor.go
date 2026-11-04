package ui

import (
	"strings"

	"github.com/DomBlack/git-stack/pkg/doctor"
)

// RenderDoctor draws git stack doctor's checks as a tree, each check on
// its own line with what it found, the checks under it indented beneath:
//
//	✔ git: git 2.47.1
//	✔ GitHub CLI (gh): gh version 2.62.0
//	  ✔ gh is logged in: Logged in to github.com account dom
//	  ✖ gh-stack is new enough: gh-stack v0.2.1 is older than v0.3.0
//	    ↳ git stack doctor --fix runs: gh extension upgrade stack
//
// On a terminal (tty) the marks are coloured; otherwise they are ok:,
// warn:, fail:, skip: and fixed: and the arrows "- ", so it stays ASCII.
// Text from outside (gh's stderr) is escaped.
func RenderDoctor(results []doctor.Result, st Styles, tty bool) string {
	var b strings.Builder
	renderChecks(&b, results, st, tty, "")
	return b.String()
}

func renderChecks(b *strings.Builder, results []doctor.Result, st Styles, tty bool, indent string) {
	for _, r := range results {
		line := Printable(r.Title)
		if r.Summary != "" {
			line += ": " + Printable(r.Summary)
		}
		if r.Status == doctor.Skipped {
			line = st.Muted.Render(line)
		}
		b.WriteString(indent + doctorMark(r.Status, st, tty) + " " + line + "\n")
		sub := indent + "  "
		for _, d := range r.Details {
			b.WriteString(sub + "  " + st.Muted.Render(Printable(d)) + "\n")
		}
		arrow := "  - "
		if tty {
			arrow = "  " + st.Muted.Render(markStep) + " "
		}
		switch {
		case r.FixError != "":
			b.WriteString(sub + arrow + "tried " + Printable(r.Fix) + ", but " + Printable(r.FixError) + "\n")
		case r.Status == doctor.Fixed:
			b.WriteString(sub + arrow + "fixed with " + Printable(r.Fix) + "\n")
		case r.Fix != "":
			b.WriteString(sub + arrow + "git stack doctor --fix runs: " + Printable(r.Fix) + "\n")
		}
		if r.Status != doctor.Fixed {
			for _, s := range r.Steps {
				b.WriteString(sub + arrow + Printable(s) + "\n")
			}
		}
		renderChecks(b, r.Checks, st, tty, sub)
	}
}

func doctorMark(s doctor.Status, st Styles, tty bool) string {
	if !tty {
		switch s {
		case doctor.OK:
			return "ok:"
		case doctor.Fixed:
			return "fixed:"
		case doctor.Warn:
			return "warn:"
		case doctor.Fail:
			return "fail:"
		default:
			return "skip:"
		}
	}
	switch s {
	case doctor.OK, doctor.Fixed:
		return st.Success.Render(markOK)
	case doctor.Warn:
		return st.Warning.Render(markWarn)
	case doctor.Fail:
		return st.Error.Render(markErr)
	default:
		return st.Muted.Render("○")
	}
}
