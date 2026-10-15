package absorb

import "slices"

// Commit is a candidate destination: a commit of the stack with its diff
// against its first parent.
type Commit struct {
	SHA, Subject, Branch string
	Files                []FileDiff
}

// Target is the commit a hunk goes into.
type Target struct {
	SHA, Subject, Branch string
}

// HunkPlan is one staged hunk and where it goes.
type HunkPlan struct {
	// Path is the file's path in the index (the new path of a rename).
	Path string
	Hunk Hunk
	// Target is nil when no candidate commit changed the lines.
	Target *Target
}

// SkippedFile is a staged change absorb does not plan, with the reason.
type SkippedFile struct {
	Path, Reason string
}

// Reasons a staged file is skipped. They stay staged.
const (
	ReasonNewFile     = "new file"
	ReasonDeletedFile = "deleted file"
	ReasonBinary      = "binary file"
	ReasonSubmodule   = "submodule"
	ReasonNoText      = "no text changes"
)

// Plan is where every staged hunk goes and what it takes to put it there.
type Plan struct {
	Hunks   []HunkPlan
	Skipped []SkippedFile
	// Patches holds, for every commit to rewrite, the hunks to apply to its
	// tree by the file's path in that tree.
	Patches map[string]map[string][]Hunk
	// Rewrites lists the commits to rewrite, oldest first: the contiguous
	// run from the oldest destination up to the newest candidate. Every
	// one of them has a patch, since the hunk that stops at the oldest
	// destination was carried through all of the newer ones.
	Rewrites []string
}

// Absorbed counts the hunks with a destination.
func (p *Plan) Absorbed() int {
	n := 0
	for _, h := range p.Hunks {
		if h.Target != nil {
			n++
		}
	}
	return n
}

// Unabsorbed counts the hunks no commit took.
func (p *Plan) Unabsorbed() int { return len(p.Hunks) - p.Absorbed() }

// Targets counts the distinct destination commits.
func (p *Plan) Targets() int {
	seen := map[string]bool{}
	for _, h := range p.Hunks {
		if h.Target != nil {
			seen[h.Target.SHA] = true
		}
	}
	return len(seen)
}

// visit is one commit a hunk's search went through, with the path and
// line numbers the hunk had in that commit's tree.
type visit struct {
	sha, path string
	hunk      Hunk
}

// Compute decides a destination for every staged hunk. commits run oldest
// first (bottom branch first, as the stack has them); staged is the index
// against the newest of them. Each hunk starts at the newest commit with
// its HEAD line numbers and is moved backwards past every commit that
// doesn't touch its lines; the first commit that does, or the one that
// created the file, takes it. A hunk that passes every commit is left
// without a Target.
func Compute(commits []Commit, staged []FileDiff) Plan {
	p := Plan{Patches: map[string]map[string][]Hunk{}}
	oldest := len(commits)
	for _, f := range staged {
		if reason := skipReason(f); reason != "" {
			p.Skipped = append(p.Skipped, SkippedFile{Path: f.Path(), Reason: reason})
			continue
		}
		for _, staged := range f.Hunks {
			h, path := staged, f.OldPath
			var target *Target
			var visited []visit
			at := -1
		search:
			for i := len(commits) - 1; i >= 0; i-- {
				c := commits[i]
				visited = append(visited, visit{sha: c.SHA, path: path, hunk: h})
				j := slices.IndexFunc(c.Files, func(fd FileDiff) bool { return fd.NewPath == path && !fd.IsDeleted() })
				if j < 0 {
					continue
				}
				fd := c.Files[j]
				if fd.IsNew() {
					target, at = &Target{SHA: c.SHA, Subject: c.Subject, Branch: c.Branch}, i
					break
				}
				for _, prev := range slices.Backward(fd.Hunks) {
					moved, ok := passBack(prev, h)
					if !ok {
						target, at = &Target{SHA: c.SHA, Subject: c.Subject, Branch: c.Branch}, i
						break search
					}
					h = moved
				}
				path = fd.OldPath
			}
			p.Hunks = append(p.Hunks, HunkPlan{Path: f.Path(), Hunk: staged, Target: target})
			if target == nil {
				continue
			}
			oldest = min(oldest, at)
			for _, v := range visited {
				if p.Patches[v.sha] == nil {
					p.Patches[v.sha] = map[string][]Hunk{}
				}
				p.Patches[v.sha][v.path] = append(p.Patches[v.sha][v.path], v.hunk)
			}
		}
	}
	for _, c := range commits[oldest:] {
		p.Rewrites = append(p.Rewrites, c.SHA)
	}
	return p
}

// skipReason says why a staged file can't be planned, or "" when it can.
func skipReason(f FileDiff) string {
	switch {
	case f.Submodule:
		return ReasonSubmodule
	case f.Binary:
		return ReasonBinary
	case f.IsNew():
		return ReasonNewFile
	case f.IsDeleted():
		return ReasonDeletedFile
	case len(f.Hunks) == 0:
		return ReasonNoText
	}
	return ""
}

// passBack moves staged hunk s, whose old side line numbers are in the
// tree h produced, back past h into the tree h started from. ok is false
// when the two overlap, which means h's commit owns the lines s changes.
//
// s covers old lines OldStart to OldStart+OldCount-1, or is an insertion
// after OldStart; h produced new lines NewStart to NewStart+NewCount-1, or
// is a deletion whose gap comes after NewStart. s is before h when its last
// line (or insertion point) is before h's first new line, or when h only
// deletes, s removes lines and s's last removed line is exactly h's
// anchor: touching, not overlapping. s is after h when h's last new line
// (or deletion anchor) is before s's first line, or when h adds lines, s
// only inserts and s's insertion point is h's last added line; then s
// moves up by the lines h added.
func passBack(h, s Hunk) (Hunk, bool) {
	sLast := s.OldStart
	if s.OldCount > 0 {
		sLast = s.OldStart + s.OldCount - 1
	}
	hLast := h.NewStart
	if h.NewCount > 0 {
		hLast = h.NewStart + h.NewCount - 1
	}
	switch {
	case sLast < h.NewStart, h.NewCount == 0 && s.OldCount > 0 && sLast == h.NewStart:
		return s, true
	case hLast < s.OldStart, h.NewCount > 0 && s.OldCount == 0 && hLast == s.OldStart:
		s.OldStart -= h.Net()
		return s, true
	}
	return Hunk{}, false
}
