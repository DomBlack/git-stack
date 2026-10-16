package app

import "github.com/DomBlack/git-stack/pkg/git"

// UndoHistory lists the undo history, newest first, as "command (mode)".
func UndoHistory(repo git.Repo) []string {
	l, err := loadUndoLog(repo)
	if err != nil {
		panic(err)
	}
	out := make([]string, len(l.Entries))
	for i, e := range l.Entries {
		out[i] = e.Command + " (" + string(e.Mode) + ")"
	}
	return out
}
