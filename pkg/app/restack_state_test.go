package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git"
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
