//go:build unix

package ghstack

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestSnapshotRestoreCommonFileOnly(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	b := New(exec.New(), g)
	path := filepath.Join(repo.CommonDir, FileName)
	if err := os.WriteFile(path, []byte(mainFile), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := b.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Update(context.Background(), repo, func(g *stack.Graph) error {
		g.Stacks[0].Branches = g.Stacks[0].Branches[1:]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Restore(context.Background(), repo, snap); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != mainFile {
		t.Errorf("restored file differs:\n%s", got)
	}
}

// Snapshot moves a worktree's own file into the catalog first, so the
// snapshot is the one file and Restore puts that back.
func TestSnapshotMovesWorktreeFilesFirst(t *testing.T) {
	b, repo, mainPath, wtPath := updateFixture(t)
	snap, err := b.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Files) != 1 || snap.Files[mainPath] == nil {
		t.Fatalf("snapshot files = %v", slices.Collect(maps.Keys(snap.Files)))
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree file left behind: %v", err)
	}
	merged := string(snap.Files[mainPath])
	if err := os.WriteFile(mainPath, []byte(`{"schemaVersion":1,"stacks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Restore(context.Background(), repo, snap); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(mainPath); string(got) != merged {
		t.Errorf("catalog differs:\n%s", got)
	}
}

// A catalog written after the snapshot is removed by Restore, since it
// didn't exist when the snapshot was taken; a worktree file named by an
// older git-stack's snapshot is put back as it was.
func TestRestoreRemovesFileThatDidNotExist(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	b := New(exec.New(), g)
	snap, err := b.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	err = b.Update(context.Background(), repo, func(g *stack.Graph) error {
		g.Stacks = append(g.Stacks, stack.Stack{Trunk: "main", Branches: []stack.Branch{{Name: "w"}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Restore(context.Background(), repo, snap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.CommonDir, FileName)); !os.IsNotExist(err) {
		t.Errorf("the catalog should be gone: %v", err)
	}
	if g, err := b.Load(context.Background(), repo); err != nil || g.Tracked("w") {
		t.Errorf("w should not be tracked after the restore: %+v %v", g, err)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, dir, "worktree", "add", "-q", "-b", "x", wt)
	wtPath := filepath.Join(repo.CommonDir, "worktrees", "wt", FileName)
	old := stack.Snapshot{Files: map[string][]byte{wtPath: []byte(wtFile)}}
	if err := b.Restore(context.Background(), repo, old); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(wtPath); string(got) != wtFile {
		t.Errorf("worktree file = %s", got)
	}
}

func TestRestoreWaitsForTheLock(t *testing.T) {
	b, repo, mainPath, _ := updateFixture(t)
	snap, err := b.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainPath, []byte(`{"schemaVersion":1,"stacks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(repo.CommonDir, LockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.Restore(context.Background(), repo, snap) }()
	select {
	case err := <-done:
		t.Fatalf("Restore finished while the lock was held: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if got, _ := os.ReadFile(mainPath); string(got) == string(snap.Files[mainPath]) {
		t.Fatal("Restore wrote while the lock was held")
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(mainPath); string(got) != string(snap.Files[mainPath]) {
		t.Errorf("main file differs:\n%s", got)
	}
}
