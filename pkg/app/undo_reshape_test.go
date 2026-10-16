package app_test

import (
	"context"
	"slices"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

// Pop leaves the branch's changes staged on its parent; undo brings the
// branch back and its checkout with it, with nothing left staged.
func TestUndoPop(t *testing.T) {
	f := newRestackFixture(t) // main -> a -> b -> c
	gittest.Run(t, f.dir, "switch", "-q", "b")
	ctx := context.Background()
	before := tips(t, f.dir, "a", "b", "c")
	if _, err := f.app.Pop(ctx, f.repo); err != nil {
		t.Fatal(err)
	}
	wantHistory(t, f.repo, "git stack pop (soft)")
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	if cur := current(t, f.dir); cur != "b" {
		t.Errorf("current = %s", cur)
	}
	if st := status(t, f.dir); st != "" {
		t.Errorf("status = %q", st)
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Errorf("stack = %v", f.names())
	}
}

func TestUndoFold(t *testing.T) {
	f := newRestackFixture(t) // HEAD on c
	ctx := context.Background()
	before := tips(t, f.dir, "a", "b", "c")
	if _, err := f.app.Fold(ctx, f.repo, app.FoldOptions{}); err != nil {
		t.Fatal(err)
	}
	wantHistory(t, f.repo, "git stack fold (keep)")
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	if cur := current(t, f.dir); cur != "c" {
		t.Errorf("current = %s", cur)
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Errorf("stack = %v", f.names())
	}
}

func TestUndoUntrackOnly(t *testing.T) {
	f := newRestackFixture(t)
	ctx := context.Background()
	before := tips(t, f.dir, "a", "b", "c")
	if _, err := f.app.Untrack(ctx, f.repo, app.UntrackOptions{Branch: "b", Only: true}); err != nil {
		t.Fatal(err)
	}
	wantHistory(t, f.repo, "git stack untrack --only (keep)")
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) || f.base("c") != before["b"] {
		t.Errorf("stack = %v, base of c %s", f.names(), f.base("c"))
	}
}

// Create --insert runs inside create, so undo gives the committed changes
// back staged on the branch it started from, and the branch above returns.
func TestUndoCreateInsert(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "a")
	ctx := context.Background()
	before := tips(t, f.dir, "a", "b", "c")
	gittest.WriteFile(t, f.dir, "n.txt", "n")
	gittest.Run(t, f.dir, "add", "n.txt")
	if _, err := f.app.Create(ctx, f.repo, app.CreateOptions{Name: "n", Message: []string{"Add n"}, Insert: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	if cur := current(t, f.dir); cur != "a" {
		t.Errorf("current = %s", cur)
	}
	if got := gittest.Run(t, f.dir, "diff", "--cached", "--name-only"); got != "n.txt" {
		t.Errorf("staged = %q", got)
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Errorf("stack = %v", f.names())
	}
}

func TestUndoLocalRename(t *testing.T) {
	f := newRestackFixture(t) // HEAD on c
	ctx := context.Background()
	before := tips(t, f.dir, "a", "b", "c")
	if _, err := f.app.Rename(ctx, f.repo, app.RenameOptions{Name: "d", Local: true}); err != nil {
		t.Fatal(err)
	}
	wantHistory(t, f.repo, "git stack rename --local (keep)")
	if _, err := f.app.Undo(ctx, f.repo, app.UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	wantTips(t, f.dir, before)
	if cur := current(t, f.dir); cur != "c" {
		t.Errorf("current = %s", cur)
	}
	if _, err := gittest.RunErr(t, f.dir, "rev-parse", "--verify", "-q", "refs/heads/d"); err == nil {
		t.Error("d should be gone")
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Errorf("stack = %v", f.names())
	}
}
