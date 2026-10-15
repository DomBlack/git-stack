// Package absorb decides which commit of a stack each staged change belongs
// to, the way git stack absorb needs it: it reads diffs as text, walks each
// changed run of lines backwards through the stack's commits until one
// that changed those lines stops it, and writes the patches that put the
// change into that commit and every commit above it. It is pure; pkg/app
// does the git work around it.
package absorb

// Hunk is one contiguous run of changed lines in one file: OldCount lines
// removed from the old file starting at OldStart, NewCount lines added to
// the new file starting at NewStart. Git's zero context conventions hold:
// an insertion has OldCount 0 and OldStart is the old line it goes after
// (0 for the top of the file); a deletion has NewCount 0 and NewStart is
// the new line before the gap. The planner only moves OldStart around;
// NewStart is what the diff said and is recomputed when a patch is written.
type Hunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
	// Lines is the body exactly as the diff had it: "-" and "+" lines, with
	// a "\ No newline at end of file" marker kept after the line it follows.
	Lines []string
}

// Net is how many lines the hunk adds to the file (negative for a removal).
func (h Hunk) Net() int { return h.NewCount - h.OldCount }

// FileDiff is one file's part of a diff.
type FileDiff struct {
	// OldPath and NewPath are the paths on each side, "" where the file does
	// not exist (a new or deleted file).
	OldPath, NewPath string
	Hunks            []Hunk
	// Binary is set when git printed no text hunks because the file is
	// binary; Submodule when the entry is a submodule pointer.
	Binary, Submodule bool
}

// IsNew reports whether the file was created by this diff.
func (f FileDiff) IsNew() bool { return f.OldPath == "" }

// IsDeleted reports whether the file was deleted by this diff.
func (f FileDiff) IsDeleted() bool { return f.NewPath == "" }

// Renamed reports whether the file changed path.
func (f FileDiff) Renamed() bool { return f.OldPath != "" && f.NewPath != "" && f.OldPath != f.NewPath }

// Path is the file's path for reports: the new one, or the old one for a
// deleted file.
func (f FileDiff) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}
