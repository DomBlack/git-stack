package ghstack

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git"
)

// TestLoadMergesWorktreeMetadata lays out a .git with the shared catalog
// and two linked worktrees' own files, as gh stack 0.2 kept them, and
// checks Load sees all of them: the catalog first, then the current
// worktree's file, then the others, a stack sharing a branch with one
// already seen left out.
func TestLoadMergesWorktreeMetadata(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, "main", ".git")
	for _, d := range []string{common, filepath.Join(common, "worktrees", "one"), filepath.Join(common, "worktrees", "two")} {
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
	// two repeats branch "a": its stack is dropped, the catalog wins.
	write(filepath.Join(common, "worktrees", "two", FileName), `{"schemaVersion":1,"repository":"o/r","stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"a"}]},{"trunk":{"branch":"main"},"branches":[{"branch":"w2"}]}]}`)

	b := New(exectest.New(), nil)
	for _, repo := range []git.Repo{
		{TopLevel: filepath.Join(root, "main"), GitDir: common, CommonDir: common},
		{TopLevel: filepath.Join(root, "wt-two"), GitDir: filepath.Join(common, "worktrees", "two"), CommonDir: common},
	} {
		g, err := b.Load(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, s := range g.Stacks {
			got = append(got, strings.Join(s.Names(), ","))
		}
		want := []string{"a,b", "w1", "w2"}
		if filepath.Base(repo.GitDir) == "two" {
			want = []string{"a,b", "w2", "w1"}
		}
		if !slices.Equal(got, want) {
			t.Errorf("from %s: stacks = %v, want %v", repo.TopLevel, got, want)
		}
	}
}
