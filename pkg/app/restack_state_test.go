package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestRestackStateRoundTrip(t *testing.T) {
	repo := git.Repo{TopLevel: t.TempDir()}
	repo.GitDir = filepath.Join(repo.TopLevel, ".git")
	repo.CommonDir = repo.GitDir
	if err := os.MkdirAll(repo.GitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if s, err := loadRestackState(repo); err != nil || s != nil {
		t.Fatalf("absent state = %+v %v, want nil, nil", s, err)
	}
	if err := clearRestackState(repo); err != nil {
		t.Fatalf("clearing an absent state: %v", err)
	}

	want := &restackState{
		Command: "git stack restack", OriginalBranch: "c", Trunk: "main",
		Conflict: "b", Onto: "a", NewBase: "n", ConflictTip: "t", ConflictOldBase: "o",
		Remaining: []string{"c"},
		Moved:     []movedRef{{Name: "a", From: "1", To: "2", OldBase: "0"}},
	}
	if err := saveRestackState(repo, want); err != nil {
		t.Fatal(err)
	}
	if p := restackStatePath(repo); p != filepath.Join(repo.GitDir, "git-stack", "restack.json") {
		t.Errorf("path = %s", p)
	}
	got, err := loadRestackState(repo)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("loaded = %+v %v", got, err)
	}

	if err := os.WriteFile(restackStatePath(repo), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRestackState(repo); err == nil {
		t.Error("corrupt state must be an error, not silently nil")
	}
	if err := clearRestackState(repo); err != nil {
		t.Fatal(err)
	}
	if s, _ := loadRestackState(repo); s != nil {
		t.Error("state should be gone")
	}

	if ghRebaseStateExists(repo) {
		t.Error("no gh state yet")
	}
	if err := os.WriteFile(filepath.Join(repo.GitDir, "gh-stack-rebase-state"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ghRebaseStateExists(repo) {
		t.Error("gh state should be detected")
	}
}

// memMeta keeps the graph in memory.
type memMeta struct{ graph *stack.Graph }

func (m *memMeta) Load(context.Context, git.Repo) (*stack.Graph, error) { return m.graph, nil }

func (m *memMeta) Update(_ context.Context, _ git.Repo, fn func(*stack.Graph) error) error {
	return fn(m.graph)
}

// stateRepo is main -> b in a real repo, with b tracked.
func stateRepo(t *testing.T) (*App, git.Repo, string) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Commit(t, dir, "shared.txt", "base", "shared")
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "b.txt", "b", "feat: b")
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	meta := &memMeta{graph: stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{{Name: "b"}}}})}
	return New(Deps{Git: g, Meta: meta}), repo, dir
}

func TestRecordRebasedIsIdempotent(t *testing.T) {
	a, repo, dir := stateRepo(t)
	ctx := context.Background()
	main := gittest.Run(t, dir, "rev-parse", "main")
	st := &restackState{Conflict: "b", ConflictTip: main, NewBase: main}
	run := &restackRun{repo: repo, state: st}
	var res RestackResult
	for range 2 {
		if err := a.recordRebased(ctx, run, &res); err != nil {
			t.Fatal(err)
		}
	}
	if len(run.moved) != 1 || len(res.Moved) != 1 {
		t.Errorf("moved = %+v, result = %+v; want b once", run.moved, res.Moved)
	}
	saved, err := loadRestackState(repo)
	if err != nil || saved == nil || len(saved.Moved) != 1 {
		t.Errorf("saved = %+v %v", saved, err)
	}
}

func TestApplyMovesSavesTheState(t *testing.T) {
	a, repo, dir := stateRepo(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "main")
	from, to := gittest.Run(t, dir, "rev-parse", "b"), gittest.Run(t, dir, "rev-parse", "main")
	st := &restackState{Conflict: "x"}
	if err := saveRestackState(repo, st); err != nil {
		t.Fatal(err)
	}
	run := &restackRun{repo: repo, command: restackCommand, state: st}
	p := &restackPlan{moves: []plannedMove{{name: "b", from: from, to: to}}}
	if err := a.applyMoves(ctx, run, p, &RestackResult{}); err != nil {
		t.Fatal(err)
	}
	saved, err := loadRestackState(repo)
	if err != nil || saved == nil || len(saved.Moved) != 1 || saved.Moved[0].Name != "b" || saved.Moved[0].From != from || saved.Moved[0].To != to {
		t.Errorf("saved = %+v %v", saved, err)
	}
}
