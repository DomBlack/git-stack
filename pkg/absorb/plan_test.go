package absorb

import (
	"slices"
	"testing"
)

// edit is a hunk replacing removed lines from start with added ones.
func edit(start, removed, added int) Hunk {
	h := Hunk{OldStart: start, OldCount: removed, NewStart: start, NewCount: added}
	if removed == 0 {
		h.NewStart = start + 1
	}
	if added == 0 {
		h.NewStart = start - 1
	}
	for i := range removed {
		h.Lines = append(h.Lines, "-old"+string(rune('0'+i)))
	}
	for i := range added {
		h.Lines = append(h.Lines, "+new"+string(rune('0'+i)))
	}
	return h
}

func change(path string, hunks ...Hunk) FileDiff {
	return FileDiff{OldPath: path, NewPath: path, Hunks: hunks}
}

func created(path string, n int) FileDiff {
	return FileDiff{NewPath: path, Hunks: []Hunk{edit(0, 0, n)}}
}

func renamed(from, to string, hunks ...Hunk) FileDiff {
	return FileDiff{OldPath: from, NewPath: to, Hunks: hunks}
}

func TestPassBack(t *testing.T) {
	// h is the historical hunk, s the staged one in the tree h produced.
	cases := []struct {
		name   string
		h, s   Hunk
		ok     bool
		wantAt int // s.OldStart after passing, when ok
	}{
		{"before, disjoint", edit(10, 1, 1), edit(5, 1, 1), true, 5},
		{"before, touching last removed line against a deletion point", edit(10, 2, 0), edit(8, 2, 1), true, 8},
		{"insertion right before h's first new line", edit(10, 1, 3), edit(9, 0, 1), true, 9},
		{"after, disjoint, shifted by h's net", edit(10, 1, 3), edit(20, 1, 1), true, 18},
		{"after a deletion, shifted up", edit(10, 3, 0), edit(20, 2, 2), true, 23},
		{"insertion right after h's last new line, shifted", edit(10, 1, 3), edit(12, 0, 2), true, 10},
		{"overlap: same line", edit(10, 1, 1), edit(10, 1, 1), false, 0},
		{"overlap: s spans h", edit(11, 1, 1), edit(10, 2, 2), false, 0},
		{"overlap: insertion inside h", edit(10, 1, 3), edit(11, 0, 1), false, 0},
		{"insertion exactly at a deletion point", edit(10, 2, 0), edit(9, 0, 1), false, 0},
		{"insertion at h's first new line", edit(10, 0, 2), edit(11, 0, 1), false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := passBack(c.h, c.s)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && got.OldStart != c.wantAt {
				t.Errorf("OldStart = %d, want %d", got.OldStart, c.wantAt)
			}
			if ok && (got.OldCount != c.s.OldCount || got.NewCount != c.s.NewCount || !slices.Equal(got.Lines, c.s.Lines)) {
				t.Errorf("hunk body changed: %+v", got)
			}
		})
	}
}

// twoCommits is main -> A (changes line 10 of app.go) -> B (changes line 30).
func twoCommits() []Commit {
	return []Commit{
		{SHA: "A", Subject: "a", Branch: "base", Files: []FileDiff{change("app.go", edit(10, 1, 1))}},
		{SHA: "B", Subject: "b", Branch: "api", Files: []FileDiff{change("app.go", edit(30, 1, 1))}},
	}
}

func targets(p Plan) []string {
	var out []string
	for _, h := range p.Hunks {
		if h.Target == nil {
			out = append(out, "none")
		} else {
			out = append(out, h.Target.SHA)
		}
	}
	return out
}

func TestComputeDestinations(t *testing.T) {
	cases := []struct {
		name     string
		commits  []Commit
		staged   []FileDiff
		want     []string // target per hunk
		rewrites []string
		skipped  int
	}{
		{"line changed by A", twoCommits(), []FileDiff{change("app.go", edit(10, 1, 1))}, []string{"A"}, []string{"A", "B"}, 0},
		{"line changed by B", twoCommits(), []FileDiff{change("app.go", edit(30, 1, 1))}, []string{"B"}, []string{"B"}, 0},
		{"trunk line nobody touched", twoCommits(), []FileDiff{change("app.go", edit(50, 1, 1))}, []string{"none"}, nil, 0},
		{"one run spanning A's and B's lines goes to the newer commit",
			[]Commit{
				{SHA: "A", Files: []FileDiff{change("app.go", edit(10, 1, 1))}},
				{SHA: "B", Files: []FileDiff{change("app.go", edit(11, 1, 1))}},
			},
			[]FileDiff{change("app.go", edit(10, 2, 2))}, []string{"B"}, []string{"B"}, 0},
		{"new file is skipped", twoCommits(), []FileDiff{created("new.go", 5)}, nil, nil, 1},
		{"file created in A takes a later edit", []Commit{{SHA: "A", Files: []FileDiff{created("app.go", 100)}}, {SHA: "B"}},
			[]FileDiff{change("app.go", edit(50, 1, 1))}, []string{"A"}, []string{"A", "B"}, 0},
		{"rename in B is followed back to the creation in A",
			[]Commit{{SHA: "A", Files: []FileDiff{created("old.go", 100)}}, {SHA: "B", Files: []FileDiff{renamed("old.go", "new.go")}}},
			[]FileDiff{change("new.go", edit(50, 1, 1))}, []string{"A"}, []string{"A", "B"}, 0},
		{"a staged rename with an edit is planned against the old path",
			[]Commit{{SHA: "A", Files: []FileDiff{change("old.go", edit(5, 1, 1))}}},
			[]FileDiff{renamed("old.go", "new.go", edit(5, 1, 1))}, []string{"A"}, []string{"A"}, 0},
		{"line numbers shift past an insertion in B",
			[]Commit{
				{SHA: "A", Files: []FileDiff{change("app.go", edit(10, 1, 1))}},
				{SHA: "B", Files: []FileDiff{change("app.go", edit(2, 0, 5))}}, // inserts 5 lines after line 2
			},
			[]FileDiff{change("app.go", edit(15, 1, 1))}, []string{"A"}, []string{"A", "B"}, 0},
		{"the older commit is checked after passing the newer one's other hunk",
			[]Commit{
				{SHA: "A", Files: []FileDiff{change("app.go", edit(10, 1, 1))}},
				{SHA: "B", Files: []FileDiff{change("app.go", edit(3, 1, 1), edit(30, 1, 1))}},
			},
			[]FileDiff{change("app.go", edit(10, 1, 1))}, []string{"A"}, []string{"A", "B"}, 0},
		{"deleted, binary, submodule and mode only files are skipped",
			twoCommits(),
			[]FileDiff{{OldPath: "gone.txt"}, {OldPath: "p.png", NewPath: "p.png", Binary: true}, {OldPath: "sub", NewPath: "sub", Submodule: true, Hunks: []Hunk{edit(1, 1, 1)}}, change("run.sh")},
			nil, nil, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Compute(c.commits, c.staged)
			if got := targets(p); !slices.Equal(got, c.want) {
				t.Errorf("targets = %v, want %v", got, c.want)
			}
			if !slices.Equal(p.Rewrites, c.rewrites) {
				t.Errorf("rewrites = %v, want %v", p.Rewrites, c.rewrites)
			}
			if len(p.Skipped) != c.skipped {
				t.Errorf("skipped = %+v", p.Skipped)
			}
			for _, sha := range p.Rewrites {
				if len(p.Patches[sha]) == 0 {
					t.Errorf("rewritten commit %s has no patch", sha)
				}
			}
		})
	}
}

// The hunk is carried into every snapshot above its destination, in each
// snapshot's own coordinates and path, so the trees above still contain it.
func TestComputeCarriesHunkForward(t *testing.T) {
	commits := []Commit{
		{SHA: "A", Branch: "base", Files: []FileDiff{created("old.go", 100)}},
		{SHA: "B", Branch: "api", Files: []FileDiff{renamed("old.go", "new.go", edit(2, 0, 5))}},
	}
	p := Compute(commits, []FileDiff{change("new.go", edit(50, 1, 1))})
	if p.Hunks[0].Target == nil || p.Hunks[0].Target.SHA != "A" || p.Hunks[0].Target.Branch != "base" {
		t.Fatalf("target = %+v", p.Hunks[0].Target)
	}
	if hs := p.Patches["B"]["new.go"]; len(hs) != 1 || hs[0].OldStart != 50 {
		t.Errorf("B's patch = %+v", p.Patches["B"])
	}
	if hs := p.Patches["A"]["old.go"]; len(hs) != 1 || hs[0].OldStart != 45 {
		t.Errorf("A's patch = %+v", p.Patches["A"])
	}
	if p.Absorbed() != 1 || p.Unabsorbed() != 0 || p.Targets() != 1 {
		t.Errorf("counts = %d %d %d", p.Absorbed(), p.Unabsorbed(), p.Targets())
	}
}

func TestComputeSeveralHunksAndFiles(t *testing.T) {
	commits := []Commit{
		{SHA: "A", Files: []FileDiff{change("a.go", edit(10, 1, 1)), change("b.go", edit(1, 1, 1))}},
		{SHA: "B", Files: []FileDiff{change("a.go", edit(30, 1, 1))}},
	}
	staged := []FileDiff{
		change("a.go", edit(10, 1, 1), edit(30, 1, 1), edit(60, 1, 1)),
		change("b.go", edit(1, 1, 1)),
	}
	p := Compute(commits, staged)
	if got := targets(p); !slices.Equal(got, []string{"A", "B", "none", "A"}) {
		t.Errorf("targets = %v", got)
	}
	if len(p.Patches["A"]["a.go"]) != 1 || len(p.Patches["B"]["a.go"]) != 2 || len(p.Patches["A"]["b.go"]) != 1 || len(p.Patches["B"]["b.go"]) != 1 {
		t.Errorf("patches = %+v", p.Patches)
	}
	if p.Absorbed() != 3 || p.Unabsorbed() != 1 || p.Targets() != 2 {
		t.Errorf("counts = %d %d %d", p.Absorbed(), p.Unabsorbed(), p.Targets())
	}
}
