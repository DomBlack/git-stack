package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// commitOn adds a commit to branch without checking it out.
func commitOn(t *testing.T, dir, branch, msg string) string {
	t.Helper()
	parent := gittest.Run(t, dir, "rev-parse", branch)
	tree := gittest.Run(t, dir, "rev-parse", branch+"^{tree}")
	sha := gittest.Run(t, dir, "commit-tree", tree, "-p", parent, "-m", msg)
	gittest.Run(t, dir, "update-ref", "refs/heads/"+branch, sha)
	return sha
}

func TestUndoableRecordsNewestFirst(t *testing.T) {
	a, repo, dir := stateRepo(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "main")
	for i, cmd := range []string{"git stack one", "git stack two"} {
		mode := []undoMode{undoKeep, undoSoft}[i]
		err := a.undoable(ctx, repo, cmd, mode, func(context.Context) error {
			commitOn(t, dir, "b", cmd)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err := loadUndoLog(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 2 || l.Entries[0].Command != "git stack two" || l.Entries[1].Command != "git stack one" {
		t.Fatalf("entries = %+v", l.Entries)
	}
	e := l.Entries[0]
	if e.Mode != undoSoft || e.Worktree != repo.TopLevel || e.At.IsZero() {
		t.Errorf("entry = %+v", e)
	}
	if e.Before.Refs["b"] != l.Entries[1].After.Refs["b"] || e.After.Refs["b"] != gittest.Run(t, dir, "rev-parse", "b") {
		t.Errorf("refs before %v after %v", e.Before.Refs, e.After.Refs)
	}
	if _, ok := e.Before.Refs["main"]; !ok {
		t.Error("the trunk should be recorded")
	}
	if len(e.Before.Worktrees) != 1 || e.Before.Worktrees[0].Branch != "main" {
		t.Errorf("worktrees = %+v", e.Before.Worktrees)
	}
}

func TestUndoableRecordsNothingForANoop(t *testing.T) {
	a, repo, _ := stateRepo(t)
	ctx := context.Background()
	if err := a.undoable(ctx, repo, "git stack restack", undoKeep, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(undoLogPath(repo)); !errors.Is(err, os.ErrNotExist) {
		l, _ := loadUndoLog(repo)
		if len(l.Entries) != 0 {
			t.Errorf("a no-op recorded %+v", l.Entries)
		}
	}
}

func TestUndoableInsideWithoutUndoRecordsNothing(t *testing.T) {
	a, repo, dir := stateRepo(t)
	ctx := withoutUndo(context.Background())
	if err := a.undoable(ctx, repo, "git stack sync", undoKeep, func(context.Context) error {
		commitOn(t, dir, "b", "x")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if l, _ := loadUndoLog(repo); len(l.Entries) != 0 {
		t.Errorf("recorded %+v", l.Entries)
	}
}

func TestUndoableRecordsNothingOnError(t *testing.T) {
	a, repo, dir := stateRepo(t)
	ctx := context.Background()
	failed := errors.New("boom")
	err := a.undoable(ctx, repo, "git stack restack", undoKeep, func(context.Context) error {
		commitOn(t, dir, "b", "x")
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatalf("err = %v", err)
	}
	if l, _ := loadUndoLog(repo); len(l.Entries) != 0 {
		t.Errorf("recorded %+v", l.Entries)
	}
	err = a.undoable(ctx, repo, "git stack sync", undoKeep, func(context.Context) error {
		commitOn(t, dir, "b", "y")
		return stack.New(stack.KindPartial, "some of it")
	})
	if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindPartial {
		t.Fatalf("err = %v", err)
	}
	if l, _ := loadUndoLog(repo); len(l.Entries) != 1 {
		t.Errorf("a partial result should be recorded: %+v", l.Entries)
	}
}

// keepAliveParents lists the parents of the keep alive ref's commit, or nil
// when the ref doesn't exist.
func keepAliveParents(t *testing.T, dir string) []string {
	t.Helper()
	out, err := gittest.RunErr(t, dir, "rev-list", "--parents", "-n1", undoRef)
	if err != nil {
		return nil
	}
	parents := strings.Fields(out)[1:]
	slices.Sort(parents)
	return parents
}

func TestUndoHistoryKeepsTwentyAndTheirCommits(t *testing.T) {
	a, repo, dir := stateRepo(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "main")
	first := gittest.Run(t, dir, "rev-parse", "b")
	for i := range undoLimit + 1 {
		err := a.undoable(ctx, repo, fmt.Sprintf("git stack %d", i), undoKeep, func(context.Context) error {
			commitOn(t, dir, "b", fmt.Sprint(i))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err := loadUndoLog(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != undoLimit || l.Entries[undoLimit-1].Command != "git stack 1" {
		t.Fatalf("kept %d entries, oldest %q", len(l.Entries), l.Entries[len(l.Entries)-1].Command)
	}
	var want []string
	for _, e := range l.Entries {
		for _, sha := range e.Before.Refs {
			want = append(want, sha)
		}
		for _, w := range e.Before.Worktrees {
			want = append(want, w.Head)
		}
	}
	slices.Sort(want)
	want = slices.Compact(want)
	got := keepAliveParents(t, dir)
	if !slices.Equal(got, want) {
		t.Errorf("parents = %v, want %v", got, want)
	}
	if slices.Contains(got, first) {
		t.Error("the dropped entry's commit should no longer be kept alive")
	}
}

func TestClearUndo(t *testing.T) {
	a, repo, dir := stateRepo(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "main")
	if err := a.undoable(ctx, repo, "git stack one", undoKeep, func(context.Context) error {
		commitOn(t, dir, "b", "x")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if keepAliveParents(t, dir) == nil {
		t.Fatal("the ref should exist")
	}
	if err := a.clearUndo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	l, err := loadUndoLog(repo)
	if err != nil || len(l.Entries) != 0 || !l.Cleared {
		t.Errorf("log = %+v %v", l, err)
	}
	if keepAliveParents(t, dir) != nil {
		t.Error("the ref should be gone")
	}
}

// conflictRepo is stateRepo with main changing shared.txt the other way, so
// restacking b stops on a conflict.
func conflictRepo(t *testing.T) (*App, string) {
	t.Helper()
	a, _, dir := stateRepo(t)
	gittest.Commit(t, dir, "shared.txt", "b side", "b: shared")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "shared.txt", "main side", "main: shared")
	gittest.Run(t, dir, "switch", "-q", "b")
	return a, dir
}

func TestStoppedRestackCarriesTheUndo(t *testing.T) {
	for _, then := range []string{"continue", "abort"} {
		t.Run(then, func(t *testing.T) {
			a, dir := conflictRepo(t)
			ctx := context.Background()
			repo, err := a.d.Git.Discover(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Restack(ctx, repo, RestackOptions{})
			if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindConflict {
				t.Fatalf("restack = %v", err)
			}
			st, err := loadRestackState(repo)
			if err != nil || st == nil || st.Undo == nil || st.Undo.Command != "git stack restack" {
				t.Fatalf("state = %+v %v", st, err)
			}
			if l, _ := loadUndoLog(repo); len(l.Entries) != 0 {
				t.Fatalf("a stopped restack recorded %+v", l.Entries)
			}
			if then == "continue" {
				gittest.WriteFile(t, dir, "shared.txt", "resolved")
				gittest.Run(t, dir, "add", "shared.txt")
				t.Setenv("GIT_EDITOR", "true")
				_, err = a.Restack(ctx, repo, RestackOptions{Continue: true})
			} else {
				_, err = a.Restack(ctx, repo, RestackOptions{Abort: true})
			}
			if err != nil {
				t.Fatal(err)
			}
			l, _ := loadUndoLog(repo)
			want := 0
			if then == "continue" {
				want = 1
			}
			if len(l.Entries) != want {
				t.Fatalf("entries = %+v", l.Entries)
			}
			if want == 1 && (l.Entries[0].Command != "git stack restack" || l.Entries[0].Mode != undoKeep) {
				t.Errorf("entry = %+v", l.Entries[0])
			}
		})
	}
}
