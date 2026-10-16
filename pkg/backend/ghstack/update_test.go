//go:build unix

package ghstack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

const mainFile = `{
  "schemaVersion": 1,
  "repository": "o/r",
  "future": {"keep": [1, 2]},
  "stacks": [
    {
      "id": "S1",
      "number": 7,
      "trunk": {"branch": "main", "head": "t1"},
      "branches": [
        {"branch": "a", "head": "ha", "base": "t1", "pullRequest": {"number": 1, "merged": true}, "extra": "yes"},
        {"branch": "b", "head": "hb", "base": "ha"}
      ]
    },
    {
      "trunk": {"branch": "main"},
      "branches": [{"branch": "done", "pullRequest": {"number": 2, "merged": true}}]
    }
  ]
}
`

const wtFile = `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"w","head":"hw"}]}]}`

func updateFixture(t *testing.T) (*Backend, git.Repo, string, string) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "w", wt)
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(repo.CommonDir, FileName)
	wtPath := filepath.Join(repo.CommonDir, "worktrees", "wt", FileName)
	if err := os.WriteFile(mainPath, []byte(mainFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtPath, []byte(wtFile), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(exec.New(), g), repo, mainPath, wtPath
}

func TestUpdateEditsInPlace(t *testing.T) {
	b, repo, mainPath, wtPath := updateFixture(t)
	if err := os.Chmod(mainPath, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(wtPath)
	err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
		for i := range g.Stacks {
			s := &g.Stacks[i]
			switch s.Bottom() {
			case "a":
				s.Branches = s.Branches[1:] // a pruned
				s.Branches[0].Head = "hb2"
				s.Branches[0].Base = "t2"
			case "done":
				s.Branches = nil // finished stack
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(mainPath); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("the rewritten file must keep its mode: %v %v", fi, err)
	}
	got, _ := os.ReadFile(mainPath)
	s := string(got)
	for _, want := range []string{`"future"`, `"keep"`, `"id": "S1"`, `"number": 7`, `"head": "t1"`, `"branch": "b"`, `"head": "hb2"`, `"base": "t2"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in:\n%s", want, s)
		}
	}
	for _, gone := range []string{`"branch": "a"`, `"extra"`, `"done"`} {
		if strings.Contains(s, gone) {
			t.Errorf("should be gone: %s in:\n%s", gone, s)
		}
	}
	if strings.Index(s, `"schemaVersion"`) > strings.Index(s, `"repository"`) || strings.Index(s, `"repository"`) > strings.Index(s, `"future"`) {
		t.Error("member order must be preserved")
	}
	if after, _ := os.ReadFile(wtPath); string(after) != string(before) {
		t.Error("the untouched worktree file must be byte for byte the same")
	}
	g, err := b.Load(context.Background(), repo)
	if err != nil || len(g.Stacks) != 2 || g.Stacks[0].Bottom() != "b" || g.Stacks[1].Bottom() != "w" {
		t.Errorf("reload = %+v %v", g, err)
	}
}

func TestUpdateWritesEachWorktreeFile(t *testing.T) {
	b, repo, _, wtPath := updateFixture(t)
	err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
		for i := range g.Stacks {
			if g.Stacks[i].Bottom() == "w" {
				g.Stacks[i].Branches[0].Head = "hw2"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(wtPath); !strings.Contains(string(got), `"hw2"`) {
		t.Errorf("worktree file not updated: %s", got)
	}
}

// A branch renamed with RenamedFrom keeps its record: pull request,
// members we don't model and its place in the stack.
func TestUpdateRenamesABranch(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
		s, i, _ := g.StackOf("a")
		s.Branches[i].Name, s.Branches[i].RenamedFrom = "a2", "a"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(mainPath)
	s := string(got)
	for _, want := range []string{`"branch": "a2"`, `"extra": "yes"`, `"number": 1`, `"head": "ha"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in:\n%s", want, s)
		}
	}
	if strings.Contains(s, `"branch": "a"`) {
		t.Errorf("the old name should be gone:\n%s", s)
	}
	g, err := b.Load(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	st, i, ok := g.StackOf("a2")
	if !ok || i != 0 || st.Branches[1].Name != "b" || st.Branches[0].PR == nil || st.Branches[0].PR.Number != 1 {
		t.Errorf("reload = %+v", st)
	}
}

func TestUpdateFnErrorWritesNothing(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	before, _ := os.ReadFile(mainPath)
	err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
		g.Stacks[0].Branches = nil
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("fn's error must be returned")
	}
	if after, _ := os.ReadFile(mainPath); string(after) != string(before) {
		t.Error("nothing may be written when fn fails")
	}
}

func TestUpdateTimesOutOnHeldLock(t *testing.T) {
	b, repo, _, _ := updateFixture(t)
	old := lockWait
	lockWait = 150 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	f, err := os.OpenFile(filepath.Join(repo.CommonDir, LockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	err = b.Update(context.Background(), repo, func(*stack.Graph) error { return nil })
	if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindLocked {
		t.Errorf("want KindLocked, got %v", err)
	}
}

func TestUpdateAppendsStackToCurrentWorktreeFile(t *testing.T) {
	b, repo, mainPath, wtPath := updateFixture(t)
	ctx := context.Background()
	// From the main checkout the new stack lands in the main file.
	err := b.Update(ctx, repo, func(g *stack.Graph) error {
		g.Stacks = append(g.Stacks, stack.Stack{Trunk: "main", Branches: []stack.Branch{{Name: "n1", Head: "h1", Base: "t1"}, {Name: "n2"}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mainPath)
	s := string(data)
	for _, want := range []string{`"branch": "n1"`, `"head": "h1"`, `"base": "t1"`, `"branch": "n2"`, `"future"`} {
		if !strings.Contains(s, want) {
			t.Errorf("main file lacks %s:\n%s", want, s)
		}
	}
	if wt, _ := os.ReadFile(wtPath); strings.Contains(string(wt), "n1") {
		t.Error("the worktree file got the stack too")
	}
	g, err := b.Load(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if s, _, ok := g.StackOf("n2"); !ok || s.Trunk != "main" || s.Bottom() != "n1" || s.ID != "" {
		t.Errorf("stack of n2 = %+v", s)
	}

	// From the linked worktree it lands in that worktree's file.
	wtRepo, err := git.New(exec.New()).Discover(ctx, worktreePath(filepath.Dir(wtPath)))
	if err != nil {
		t.Fatal(err)
	}
	err = b.Update(ctx, wtRepo, func(g *stack.Graph) error {
		g.Stacks = append(g.Stacks, stack.Stack{Trunk: "main", Branches: []stack.Branch{{Name: "w2"}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if wt, _ := os.ReadFile(wtPath); !strings.Contains(string(wt), `"branch": "w2"`) {
		t.Errorf("worktree file lacks w2:\n%s", wt)
	}
}

func TestUpdateAppendsStackToNewWorktreeFile(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	ctx := context.Background()
	// A second worktree with no file of its own: gh stack only reads the
	// worktree's file there, so the new stack must not go in the main one.
	dir := filepath.Join(t.TempDir(), "fresh")
	gittest.Run(t, repo.TopLevel, "worktree", "add", "-q", "-b", "f", dir)
	wtRepo, err := git.New(exec.New()).Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	err = b.Update(ctx, wtRepo, func(g *stack.Graph) error {
		g.Stacks = append(g.Stacks, stack.Stack{Trunk: "main", Branches: []stack.Branch{{Name: "f"}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(wtRepo.GitDir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if f, err := parseFile(data); err != nil || len(f.Stacks) != 1 || f.Stacks[0].Branches[0].Branch != "f" {
		t.Errorf("worktree file = %s (%v)", data, err)
	}
	if main, _ := os.ReadFile(mainPath); string(main) != mainFile {
		t.Errorf("the main file changed:\n%s", main)
	}
}

func TestUpdateCreatesTheFileForTheFirstStack(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	b := New(exec.New(), g)
	err = b.Update(context.Background(), repo, func(g *stack.Graph) error {
		g.Stacks = append(g.Stacks, stack.Stack{Trunk: "main", Branches: []stack.Branch{{Name: "first", Base: "t0"}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo.CommonDir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if f, err := parseFile(data); err != nil || len(f.Stacks) != 1 || f.Stacks[0].Trunk.Branch != "main" || f.Stacks[0].Branches[0].Base != "t0" {
		t.Errorf("file = %s (%v)", data, err)
	}
}

func TestUpdateRefusesBadNewStacks(t *testing.T) {
	b, repo, _, _ := updateFixture(t)
	for _, s := range []stack.Stack{{Branches: []stack.Branch{{Name: "x"}}}, {Trunk: "main"}} {
		err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
			g.Stacks = append(g.Stacks, s)
			return nil
		})
		if err == nil {
			t.Errorf("appending %+v succeeded", s)
		}
	}
	err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
		g.Stacks = g.Stacks[:1]
		return nil
	})
	if err == nil {
		t.Error("dropping a stack succeeded")
	}
}

func TestUpdateClearsAndRestoresRemoteID(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	ctx := context.Background()
	err := b.Update(ctx, repo, func(g *stack.Graph) error {
		g.Stacks[0].ID, g.Stacks[0].Number = "", 0
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mainPath)
	if s := string(data); strings.Contains(s, `"id"`) || !strings.Contains(s, `"future"`) || !strings.Contains(s, `"extra": "yes"`) {
		t.Errorf("id and number should be gone and the rest kept:\n%s", s)
	}
	if f, err := parseFile(data); err != nil || f.Stacks[0].ID != "" || f.Stacks[0].Number != 0 || f.Stacks[0].Branches[0].PullRequest.Number != 1 {
		t.Errorf("cleared file = %s (%v)", data, err)
	}
	err = b.Update(ctx, repo, func(g *stack.Graph) error {
		g.Stacks[0].ID, g.Stacks[0].Number = "S9", 9
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(mainPath)
	if s := string(data); !strings.Contains(s, `"id": "S9"`) || !strings.Contains(s, `"number": 9`) {
		t.Errorf("id and number should be back:\n%s", s)
	}
	g, _ := b.Load(ctx, repo)
	if g.Stacks[0].ID != "S9" || g.Stacks[0].Number != 9 {
		t.Errorf("loaded %q %d", g.Stacks[0].ID, g.Stacks[0].Number)
	}
	// A record with only one of the pair never carries an empty value for
	// the other: the number goes, the id stays.
	err = b.Update(ctx, repo, func(g *stack.Graph) error {
		g.Stacks[0].Number = 0
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(mainPath)
	if s := string(data); !strings.Contains(s, `"id": "S9"`) || strings.Contains(s, `"number": 0`) || strings.Contains(s, `"number": 9`) {
		t.Errorf("number should be removed and the id kept:\n%s", s)
	}
}

func TestUpdateReorderKeepsBranchRecords(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	ctx := context.Background()
	err := b.Update(ctx, repo, func(g *stack.Graph) error {
		s := &g.Stacks[0]
		s.Branches[0], s.Branches[1] = s.Branches[1], s.Branches[0]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mainPath)
	s := string(data)
	if strings.Index(s, `"branch": "b"`) > strings.Index(s, `"branch": "a"`) {
		t.Errorf("b should now come first:\n%s", s)
	}
	if !strings.Contains(s, `"pullRequest": {`) || !strings.Contains(s, `"extra": "yes"`) {
		t.Errorf("a's pull request and extra member should travel with it:\n%s", s)
	}
	g, _ := b.Load(ctx, repo)
	if names := g.Stacks[0].Names(); names[0] != "b" || names[1] != "a" || g.Stacks[0].Branches[1].PR == nil {
		t.Errorf("loaded %+v", g.Stacks[0])
	}
}

// A branch moved to another stack takes its pull request record and the
// members we don't model with it.
func TestUpdateMoveKeepsBranchRecords(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	ctx := context.Background()
	err := b.Update(ctx, repo, func(g *stack.Graph) error {
		src, dst := &g.Stacks[0], &g.Stacks[1]
		dst.Branches = append(dst.Branches, src.Branches[0])
		src.Branches = src.Branches[1:]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mainPath)
	f, err := parseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Stacks) != 2 || len(f.Stacks[1].Branches) != 2 || f.Stacks[1].Branches[1].Branch != "a" {
		t.Fatalf("file = %s", data)
	}
	if pr := f.Stacks[1].Branches[1].PullRequest; pr == nil || pr.Number != 1 || !strings.Contains(string(data), `"extra": "yes"`) {
		t.Errorf("a lost its records on the way:\n%s", data)
	}
}

// A stack appended with a remote id (an abort putting a stack back) keeps
// it, and its branches keep their records.
func TestUpdateAppendWritesRemoteIDAndKeepsRecords(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	ctx := context.Background()
	err := b.Update(ctx, repo, func(g *stack.Graph) error {
		a := g.Stacks[0].Branches[0]
		g.Stacks[0].Branches = g.Stacks[0].Branches[1:]
		g.Stacks = append(g.Stacks, stack.Stack{Trunk: "main", ID: "S5", Number: 5, Branches: []stack.Branch{a}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mainPath)
	f, err := parseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	last := f.Stacks[len(f.Stacks)-1]
	if last.ID != "S5" || last.Number != 5 || last.Branches[0].Branch != "a" || last.Branches[0].PullRequest == nil || !strings.Contains(string(data), `"extra": "yes"`) {
		t.Errorf("appended stack = %+v in\n%s", last, data)
	}
}

// A stack whose trunk changed gets its trunk branch rewritten; the trunk's
// recorded head stays.
func TestUpdateRewritesChangedTrunk(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	ctx := context.Background()
	err := b.Update(ctx, repo, func(g *stack.Graph) error {
		g.Stacks[0].Trunk = "release"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mainPath)
	if s := string(data); !strings.Contains(s, `"branch": "release"`) || !strings.Contains(s, `"head": "t1"`) {
		t.Errorf("trunk not rewritten:\n%s", s)
	}
	g, _ := b.Load(ctx, repo)
	if g.Stacks[0].Trunk != "release" {
		t.Errorf("loaded trunk %q", g.Stacks[0].Trunk)
	}
}

// A branch moving from the worktree's file into a stack in the main file
// keeps its members, and the worktree file loses it.
func TestUpdateMoveAcrossFilesKeepsBranchRecords(t *testing.T) {
	b, repo, mainPath, wtPath := updateFixture(t)
	wt := `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"w","head":"hw","pullRequest":{"number":4},"note":"kept"},{"branch":"w2","head":"hw2"}]}]}`
	if err := os.WriteFile(wtPath, []byte(wt), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err := b.Update(ctx, repo, func(g *stack.Graph) error {
		var src, dst *stack.Stack
		for i := range g.Stacks {
			switch g.Stacks[i].Bottom() {
			case "w":
				src = &g.Stacks[i]
			case "a":
				dst = &g.Stacks[i]
			}
		}
		dst.Branches = append(dst.Branches, src.Branches[0])
		src.Branches = src.Branches[1:]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mainPath)
	f, err := parseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	moved := f.Stacks[0].Branches[len(f.Stacks[0].Branches)-1]
	if moved.Branch != "w" || moved.PullRequest == nil || moved.PullRequest.Number != 4 || !strings.Contains(string(data), `"note": "kept"`) {
		t.Errorf("w lost its records on the way:\n%s", data)
	}
	if wtData, _ := os.ReadFile(wtPath); strings.Contains(string(wtData), `"branch": "w"`) || !strings.Contains(string(wtData), `"w2"`) {
		t.Errorf("worktree file should only hold w2 now:\n%s", wtData)
	}
}

func TestAdoptMovesStackRecordToCurrentWorktree(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	ctx := context.Background()
	gittest.Run(t, repo.TopLevel, "branch", "b")
	dir := filepath.Join(t.TempDir(), "fresh")
	gittest.Run(t, repo.TopLevel, "worktree", "add", "-q", dir, "b")
	wtRepo, err := b.git.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	// b's stack lives in the main checkout's file; from the worktree where b
	// is checked out it moves over whole.
	if err := b.adoptCurrent(ctx, wtRepo); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(wtRepo.GitDir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id": "S1"`, `"number": 7`, `"extra": "yes"`, `"branch": "b"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("worktree file lacks %s:\n%s", want, data)
		}
	}
	main, _ := os.ReadFile(mainPath)
	if strings.Contains(string(main), `"S1"`) || !strings.Contains(string(main), `"branch": "done"`) || !strings.Contains(string(main), `"future"`) {
		t.Errorf("main file should keep everything but S1:\n%s", main)
	}
	g, err := b.Load(ctx, wtRepo)
	if err != nil {
		t.Fatal(err)
	}
	if s, _, ok := g.StackOf("b"); !ok || s.ID != "S1" || s.Bottom() != "a" || s.Worktree != wtRepo.TopLevel {
		t.Errorf("stack of b = %+v", s)
	}

	// Already here: nothing is rewritten.
	before, _ := os.ReadFile(filepath.Join(wtRepo.GitDir, FileName))
	if err := b.adoptCurrent(ctx, wtRepo); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(wtRepo.GitDir, FileName)); string(after) != string(before) {
		t.Errorf("a second adopt changed the file:\n%s", after)
	}

	// And back to the main checkout once b is checked out there.
	gittest.Run(t, dir, "switch", "-q", "--detach")
	gittest.Run(t, repo.TopLevel, "switch", "-q", "b")
	if err := b.adoptCurrent(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if main, _ := os.ReadFile(mainPath); !strings.Contains(string(main), `"S1"`) {
		t.Errorf("S1 should be back in the main file:\n%s", main)
	}
	if wt, _ := os.ReadFile(filepath.Join(wtRepo.GitDir, FileName)); strings.Contains(string(wt), `"S1"`) {
		t.Errorf("S1 should have left the worktree file:\n%s", wt)
	}
}
