package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// insertFixture is main -> a -> b -> c with a remote stack id, HEAD on a.
func insertFixture(t *testing.T) *restackFixture {
	t.Helper()
	f := newRestackFixture(t)
	f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[0].Number = "S1", 4
	gittest.Run(t, f.dir, "switch", "-q", "a")
	return f
}

func TestCreateInsertPutsTheBranchAboveTheCurrentOne(t *testing.T) {
	f := insertFixture(t)
	ctx := context.Background()
	aTip := f.rev(t, "a")
	gittest.WriteFile(t, f.dir, "n.txt", "n")
	gittest.Run(t, f.dir, "add", "n.txt")

	res, err := f.app.Create(ctx, f.repo, app.CreateOptions{Name: "n", Message: []string{"Add n"}, Insert: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "n" || res.Parent != "a" || res.Commit == nil || !res.Inserted || !slices.Equal(res.Restacked, []string{"b", "c"}) {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(f.names(), []string{"a", "n", "b", "c"}) {
		t.Errorf("order = %v", f.names())
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "n" {
		t.Errorf("on %s, want n", cur)
	}
	if !f.has(t, "b", "n.txt") || !f.has(t, "c", "n.txt") || !f.has(t, "c", "b.txt") {
		t.Error("b and c should sit on n now")
	}
	if f.base("n") != aTip || f.base("b") != f.rev(t, "n") {
		t.Errorf("bases n=%s b=%s", f.base("n"), f.base("b"))
	}
	if s := f.fb.graph.Stacks[0]; s.ID != "" || s.Number != 0 {
		t.Errorf("remote id = %q #%d, want cleared", s.ID, s.Number)
	}
	if q := pendingQueue(t, f); !strings.Contains(q, `"S1"`) {
		t.Errorf("pending = %s, want S1 queued to dissolve", q)
	}
}

func TestCreateInsertWithNothingStaged(t *testing.T) {
	f := insertFixture(t)
	res, err := f.app.Create(context.Background(), f.repo, app.CreateOptions{Name: "n", Insert: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit != nil || !res.Inserted || len(res.Restacked) != 0 {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(f.names(), []string{"a", "n", "b", "c"}) {
		t.Errorf("order = %v", f.names())
	}
	if f.rev(t, "n") != f.rev(t, "a") {
		t.Error("n should be an empty branch on a")
	}
}

func TestCreateInsertOnTopIsAPlainCreate(t *testing.T) {
	f := insertFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "c")
	res, err := f.app.Create(context.Background(), f.repo, app.CreateOptions{Name: "n", Insert: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted || res.Parent != "c" {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c", "n"}) {
		t.Errorf("order = %v", f.names())
	}
	if f.fb.graph.Stacks[0].ID != "S1" || pendingQueue(t, f) != "" {
		t.Error("adding on top keeps the GitHub stack")
	}
}

func TestCreateInsertRefusals(t *testing.T) {
	f := insertFixture(t)
	ctx := context.Background()
	gittest.Run(t, f.dir, "switch", "-q", "main")
	if _, err := f.app.Create(ctx, f.repo, app.CreateOptions{Name: "n", Insert: true}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("on trunk: %v", err)
	}
	gittest.Run(t, f.dir, "switch", "-q", "a")
	f.fb.graph.Stacks[0].Branches[0].PR = &stack.PRRef{Number: 1, Merged: true}
	if _, err := f.app.Create(ctx, f.repo, app.CreateOptions{Name: "n", Insert: true}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) || !strings.Contains(err.Error(), "merged") {
		t.Errorf("merged branch: %v", err)
	}
	if exists, _ := f.fb.git.BranchExists(ctx, f.repo, "n"); exists {
		t.Error("a refused create must not leave a branch")
	}
}

func TestCreateInsertUndoesEverythingWhenTheCommitFails(t *testing.T) {
	f := insertFixture(t)
	ctx := context.Background()
	rejectCommits(t, f.dir)
	gittest.WriteFile(t, f.dir, "n.txt", "n")
	gittest.Run(t, f.dir, "add", "n.txt")

	_, err := f.app.Create(ctx, f.repo, app.CreateOptions{Name: "n", Message: []string{"Add n"}, Insert: true})
	if err == nil {
		t.Fatal("expected the commit to fail")
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Errorf("order = %v, want it put back", f.names())
	}
	if s := f.fb.graph.Stacks[0]; s.ID != "S1" || s.Number != 4 || pendingQueue(t, f) != "" {
		t.Errorf("remote id = %q #%d, pending %q; want it all put back", s.ID, s.Number, pendingQueue(t, f))
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "a" {
		t.Errorf("on %s, want back on a", cur)
	}
	if exists, _ := f.fb.git.BranchExists(ctx, f.repo, "n"); exists {
		t.Error("n should have been deleted")
	}
	if out := gittest.Run(t, f.dir, "diff", "--cached", "--name-only"); out != "n.txt" {
		t.Errorf("staged = %q, want n.txt still staged", out)
	}
}

// A conflict stops like a restack; abort puts the branches above back and
// leaves the new branch in the stack with its commit.
func TestCreateInsertConflictThenAbort(t *testing.T) {
	f := insertFixture(t)
	ctx := context.Background()
	bTip, cTip := f.rev(t, "b"), f.rev(t, "c")
	gittest.WriteFile(t, f.dir, "b.txt", "not b")
	gittest.Run(t, f.dir, "add", "b.txt")

	res, err := f.app.Create(ctx, f.repo, app.CreateOptions{Name: "n", Message: []string{"Add n"}, Insert: true})
	if !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if res.Branch != "n" || res.Commit == nil || !res.Inserted {
		t.Errorf("result = %+v, want the create reported", res)
	}
	nTip := f.rev(t, "n")

	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.names(), []string{"a", "n", "b", "c"}) {
		t.Errorf("order = %v, want n kept in place", f.names())
	}
	if f.rev(t, "b") != bTip || f.rev(t, "c") != cTip || f.rev(t, "n") != nTip {
		t.Error("b and c should be back where they were, n untouched")
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "n" {
		t.Errorf("on %s, want n", cur)
	}
}
