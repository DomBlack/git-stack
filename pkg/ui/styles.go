// Package ui holds every terminal UI component: lipgloss styles, the
// reusable stack tree renderer and the bubbletea checkout picker. It is the
// only package allowed to import Charm libraries.
package ui

import "charm.land/lipgloss/v2"

// Styles groups the lipgloss styles used by the tree and picker.
type Styles struct {
	Trunk     lipgloss.Style
	Branch    lipgloss.Style
	Current   lipgloss.Style
	Untracked lipgloss.Style
	Prefix    lipgloss.Style
	Selected  lipgloss.Style
	Match     lipgloss.Style
	PROpen    lipgloss.Style
	PRDraft   lipgloss.Style
	PRMerged  lipgloss.Style
	PRClosed  lipgloss.Style
	Restack   lipgloss.Style
	Muted     lipgloss.Style
	Help      lipgloss.Style
	Title     lipgloss.Style
	// Reporter marks.
	Success lipgloss.Style
	Warning lipgloss.Style
	Error   lipgloss.Style
}

// DefaultStyles returns the standard palette (ANSI colours so NO_COLOR and
// 16-colour terminals degrade gracefully).
func DefaultStyles() Styles {
	return Styles{
		Trunk:     lipgloss.NewStyle().Bold(true),
		Branch:    lipgloss.NewStyle(),
		Current:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		Untracked: lipgloss.NewStyle().Faint(true),
		Prefix:    lipgloss.NewStyle().Faint(true),
		Selected:  lipgloss.NewStyle().Reverse(true),
		Match:     lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("3")),
		PROpen:    lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		PRDraft:   lipgloss.NewStyle().Faint(true),
		PRMerged:  lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
		PRClosed:  lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		Restack:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Muted:     lipgloss.NewStyle().Faint(true),
		Help:      lipgloss.NewStyle().Faint(true),
		Title:     lipgloss.NewStyle().Bold(true),
		Success:   lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		Warning:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Error:     lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	}
}
