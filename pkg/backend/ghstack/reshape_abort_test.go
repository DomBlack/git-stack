//go:build unix

package ghstack

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// TestAbortedMovePutsRecordsBack runs an abort of a move that stopped on a
// conflict through this adapter: x's stack (S1, #7) is moved onto y's (S2,
// #8), the rebase of x onto y conflicts, and abort must put both stacks
// back into their own records with their ids, numbers and pull request
// records. The metadata edit, the conflicting rebase and the saved state
// are what a move leaves behind; the use case itself lives above this
// package's tests.
func TestAbortedMovePutsRecordsBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		below bool // x's stack has w under x, so the source keeps a branch
	}{
		{"source keeps a branch", true},
		{"single branch source", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gittest.Isolate(t)
			dir := gittest.InitRepo(t)
			gittest.Commit(t, dir, "shared.txt", "base", "shared")
			rev := func(r string) string { return gittest.Run(t, dir, "rev-parse", r) }
			gittest.Run(t, dir, "switch", "-q", "-c", "y")
			gittest.Commit(t, dir, "shared.txt", "y", "feat: y")
			gittest.Run(t, dir, "switch", "-q", "main")
			if tc.below {
				gittest.Run(t, dir, "switch", "-q", "-c", "w")
				gittest.Commit(t, dir, "w.txt", "w", "feat: w")
			}
			gittest.Run(t, dir, "switch", "-q", "-c", "x")
			gittest.Commit(t, dir, "shared.txt", "x", "feat: x")
			xParent := "main"
			if tc.below {
				xParent = "w"
			}
			xHead, xBase, yHead := rev("x"), rev(xParent), rev("y")

			type br = map[string]any
			var s1 []br
			if tc.below {
				s1 = append(s1, br{"branch": "w", "head": rev("w"), "base": rev("main"), "pullRequest": br{"number": 10}})
			}
			s1 = append(s1, br{"branch": "x", "head": xHead, "base": xBase, "pullRequest": br{"number": 11}, "extra": "kept"})
			meta := br{"schemaVersion": 1, "stacks": []br{
				{"id": "S1", "number": 7, "trunk": br{"branch": "main"}, "branches": s1},
				{"id": "S2", "number": 8, "trunk": br{"branch": "main"}, "note": "y's record", "branches": []br{
					{"branch": "y", "head": yHead, "base": rev("main"), "pullRequest": br{"number": 12}},
				}},
			}}
			ctx := context.Background()
			g := git.New(exec.New())
			repo, err := g.Discover(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			writeJSON(t, filepath.Join(repo.CommonDir, FileName), meta)
			b := New(exec.New(), g)

			// What the move snapshots, then its edit: x goes on top of y and
			// x's stack loses its remote id, queued for dissolving.
			graph, err := b.Load(ctx, repo)
			if err != nil {
				t.Fatal(err)
			}
			type snapBranch struct {
				Name string `json:"name"`
				Base string `json:"base,omitempty"`
			}
			type snapStack struct {
				Trunk    string       `json:"trunk"`
				Worktree string       `json:"worktree,omitempty"`
				ID       string       `json:"id,omitempty"`
				Number   int          `json:"number,omitempty"`
				Branches []snapBranch `json:"branches"`
			}
			var snap []snapStack
			for _, s := range graph.Stacks {
				ss := snapStack{Trunk: s.Trunk, Worktree: s.Worktree, ID: s.ID, Number: s.Number}
				for _, sb := range s.Branches {
					ss.Branches = append(ss.Branches, snapBranch{sb.Name, sb.Base})
				}
				snap = append(snap, ss)
			}
			err = b.Update(ctx, repo, func(g *stack.Graph) error {
				src, i, _ := g.StackOf("x")
				x := src.Branches[i]
				src.Branches = src.Branches[:i]
				src.ID, src.Number = "", 0
				dst, _, _ := g.StackOf("y")
				dst.Branches = append(dst.Branches, x)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			writeJSON(t, filepath.Join(repo.CommonDir, "git-stack", "remote-stacks.json"),
				br{"dissolve": []br{{"id": "S1", "number": 7, "stack": snap[0].Branches[0].Name}}})
			if _, err := gittest.RunErr(t, dir, "rebase", "--onto", yHead, xBase, "x"); err == nil {
				t.Fatal("the rebase of x onto y should conflict")
			}
			writeJSON(t, filepath.Join(repo.GitDir, "git-stack", "restack.json"), br{
				"command": "git stack move", "original_branch": "x", "trunk": "main",
				"conflict": "x", "onto": "y", "new_base": yHead, "conflict_tip": xHead, "conflict_old_base": xBase,
				"remaining": []string{}, "moved": []br{},
				"reshape": br{"stacks": snap},
			})

			a := app.New(app.Deps{Git: g, Meta: b})
			res, err := a.Restack(ctx, repo, app.RestackOptions{Abort: true})
			if err != nil {
				t.Fatal(err)
			}
			if !res.OrderRestored {
				t.Errorf("result = %+v", res)
			}
			data, _ := os.ReadFile(filepath.Join(repo.CommonDir, FileName))
			f, err := parseFile(data)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.Stacks) != 2 {
				t.Fatalf("file = %s", data)
			}
			byID := map[string]stackRec{}
			for _, s := range f.Stacks {
				byID[s.ID] = s
			}
			x, y := byID["S1"], byID["S2"]
			wantX := []string{"x"}
			if tc.below {
				wantX = []string{"w", "x"}
				if f.Stacks[0].ID != "S1" || f.Stacks[1].ID != "S2" {
					t.Errorf("records out of their original order:\n%s", data)
				}
			}
			var xNames, yNames []string
			for _, b := range x.Branches {
				xNames = append(xNames, b.Branch)
			}
			for _, b := range y.Branches {
				yNames = append(yNames, b.Branch)
			}
			if x.Number != 7 || !slices.Equal(xNames, wantX) || y.Number != 8 || !slices.Equal(yNames, []string{"y"}) {
				t.Fatalf("stacks not put back:\n%s", data)
			}
			if pr := x.Branches[len(x.Branches)-1].PullRequest; pr == nil || pr.Number != 11 || !strings.Contains(string(data), `"extra": "kept"`) {
				t.Errorf("x lost its records:\n%s", data)
			}
			if pr := y.Branches[0].PullRequest; pr == nil || pr.Number != 12 {
				t.Errorf("y lost its record:\n%s", data)
			}
			if tc.below && (x.Branches[0].PullRequest == nil || x.Branches[0].PullRequest.Number != 10) {
				t.Errorf("w lost its record:\n%s", data)
			}
			if x.Branches[len(x.Branches)-1].Base != xBase {
				t.Errorf("x's base = %s, want %s", x.Branches[len(x.Branches)-1].Base, xBase)
			}
			var raw struct {
				Stacks []map[string]any `json:"stacks"`
			}
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			for _, rec := range raw.Stacks {
				if (rec["id"] == "S2") != (rec["note"] == "y's record") {
					t.Errorf("y's stack is not back in its own record:\n%s", data)
				}
			}
			if rev("x") != xHead || rev("y") != yHead {
				t.Error("refs not put back")
			}
			if _, err := os.Stat(filepath.Join(repo.CommonDir, "git-stack", "remote-stacks.json")); err == nil {
				t.Error("the pending dissolve should be forgotten")
			}
		})
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
