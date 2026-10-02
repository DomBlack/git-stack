package ghstack

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git"
)

// TestLoadMergesWorktreeMetadata lays out a .git with a main checkout file
// and two linked worktrees' files and checks Load sees all of them, in the
// current worktree first order, with the worktree recorded on each stack.
func TestLoadMergesWorktreeMetadata(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, "main", ".git")
	for _, d := range []string{common, filepath.Join(common, "worktrees", "one"), filepath.Join(common, "worktrees", "two"), filepath.Join(root, "wt-one"), filepath.Join(root, "wt-two")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(common, FileName), `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"a"},{"branch":"b"}]}]}`)
	write(filepath.Join(common, "worktrees", "one", FileName), `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"w1"}]}]}`)
	write(filepath.Join(common, "worktrees", "one", "gitdir"), filepath.Join(root, "wt-one", ".git")+"\n")
	// two repeats branch "a": its stack is dropped, first one wins.
	write(filepath.Join(common, "worktrees", "two", FileName), `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"a"}]},{"trunk":{"branch":"main"},"branches":[{"branch":"w2"}]}]}`)
	write(filepath.Join(common, "worktrees", "two", "gitdir"), filepath.Join(root, "wt-two", ".git")+"\n")

	b := New(exectest.New(), nil)

	// From the main checkout.
	g, err := b.Load(context.Background(), git.Repo{TopLevel: filepath.Join(root, "main"), GitDir: common, CommonDir: common})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range g.Stacks {
		got = append(got, s.Bottom()+"@"+filepath.Base(s.Worktree))
	}
	want := []string{"a@main", "w1@wt-one", "w2@wt-two"}
	if len(got) != len(want) {
		t.Fatalf("stacks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stack %d = %q, want %q", i, got[i], want[i])
		}
	}

	// From worktree one: its own stack comes first.
	g, err = b.Load(context.Background(), git.Repo{TopLevel: filepath.Join(root, "wt-one"), GitDir: filepath.Join(common, "worktrees", "one"), CommonDir: common})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Stacks) != 3 || g.Stacks[0].Bottom() != "w1" || g.Stacks[0].Worktree != filepath.Join(root, "wt-one") || g.Stacks[1].Bottom() != "a" {
		t.Errorf("from a linked worktree: %+v", g.Stacks)
	}
	if !g.Tracked("w2") || !g.Tracked("b") {
		t.Error("every worktree's branches should be tracked")
	}
}
