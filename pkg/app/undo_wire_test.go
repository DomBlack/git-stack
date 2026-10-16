package app_test

import (
	"context"
	"slices"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func wantHistory(t *testing.T, repo git.Repo, want ...string) {
	t.Helper()
	if got := app.UndoHistory(repo); !slices.Equal(got, want) {
		t.Errorf("undo history = %q, want %q", got, want)
	}
}

// advanceMain commits on main without leaving the current branch, so the
// stack above it needs a restack.
func advanceMain(t *testing.T, dir string) {
	t.Helper()
	cur := gittest.Run(t, dir, "branch", "--show-current")
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "m.txt", "m", "main moves on")
	gittest.Run(t, dir, "switch", "-q", cur)
}

func TestCoveredCommandsRecordForUndo(t *testing.T) {
	ctx := context.Background()
	t.Run("create", func(t *testing.T) {
		a, _, repo, dir, _ := mutFixture(t)
		gittest.WriteFile(t, dir, "b.txt", "b")
		gittest.Run(t, dir, "add", "b.txt")
		if _, err := a.Create(ctx, repo, app.CreateOptions{Message: []string{"Add B"}}); err != nil {
			t.Fatal(err)
		}
		wantHistory(t, repo, "git stack create (soft)")
	})
	t.Run("modify", func(t *testing.T) {
		a, _, repo, dir, _ := mutFixture(t)
		gittest.WriteFile(t, dir, "a.txt", "a2")
		gittest.Run(t, dir, "add", "a.txt")
		if _, err := a.Modify(ctx, repo, app.ModifyOptions{}); err != nil {
			t.Fatal(err)
		}
		wantHistory(t, repo, "git stack modify (soft)")
	})
	t.Run("absorb", func(t *testing.T) {
		f := newAbsorbFixture(t)
		f.stageLine(t, 5, "FIVE")
		if _, err := f.app.Absorb(ctx, f.repo, app.AbsorbOptions{}); err != nil {
			t.Fatal(err)
		}
		wantHistory(t, f.repo, "git stack absorb (soft)")
	})
	t.Run("restack", func(t *testing.T) {
		f := newRestackFixture(t)
		advanceMain(t, f.dir)
		if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); err != nil {
			t.Fatal(err)
		}
		wantHistory(t, f.repo, "git stack restack (keep)")
	})
	t.Run("reorder", func(t *testing.T) {
		f := newRestackFixture(t)
		if _, err := f.app.Reorder(ctx, f.repo, app.ReorderOptions{Order: []string{"b", "a", "c"}}); err != nil {
			t.Fatal(err)
		}
		wantHistory(t, f.repo, "git stack reorder (keep)")
	})
	t.Run("move", func(t *testing.T) {
		f := newRestackFixture(t)
		addStack(t, f, false)
		if _, err := f.app.Move(ctx, f.repo, app.MoveOptions{Source: "b", Onto: "x"}); err != nil {
			t.Fatal(err)
		}
		wantHistory(t, f.repo, "git stack move (keep)")
	})
	t.Run("init", func(t *testing.T) {
		f := newRestackFixture(t)
		gittest.Run(t, f.dir, "switch", "-q", "-c", "p", "main")
		gittest.Commit(t, f.dir, "p.txt", "p", "p")
		if _, err := f.app.Init(ctx, f.repo, app.InitOptions{Branches: []string{"p"}}); err != nil {
			t.Fatal(err)
		}
		wantHistory(t, f.repo, "git stack init (keep)")
	})
	t.Run("sync", func(t *testing.T) {
		f := newSyncFixture(t)
		f.advanceRemote(t, "main", "r.txt")
		if _, err := f.sync(t, app.SyncOptions{NoRestack: true}); err != nil {
			t.Fatal(err)
		}
		wantHistory(t, f.repo, "git stack sync (keep)")
	})
	t.Run("nothing to do", func(t *testing.T) {
		f := newRestackFixture(t)
		if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{}); err != nil {
			t.Fatal(err)
		}
		wantHistory(t, f.repo)
	})
}

func keepAliveExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := gittest.RunErr(t, dir, "rev-parse", "--verify", "--quiet", "refs/git-stack/undo")
	return err == nil
}

func TestSubmitClearsTheUndoHistory(t *testing.T) {
	deps, _, _, repo, dir := submitFixture(t)
	a := app.New(deps)
	ctx := context.Background()
	advanceMain(t, dir)
	if _, err := a.Restack(ctx, repo, app.RestackOptions{}); err != nil {
		t.Fatal(err)
	}
	wantHistory(t, repo, "git stack restack (keep)")
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	wantHistory(t, repo, "git stack restack (keep)")
	if _, err := a.Submit(ctx, repo, app.SubmitOptions{Publish: true}); err != nil {
		t.Fatal(err)
	}
	wantHistory(t, repo)
	if keepAliveExists(t, dir) {
		t.Error("the keep alive ref should be gone")
	}
}

func TestMergeClearsTheUndoHistory(t *testing.T) {
	f := mergeFixture(t)
	f.advanceRemote(t, "main", "r.txt")
	if _, err := f.sync(t, app.SyncOptions{NoRestack: true}); err != nil {
		t.Fatal(err)
	}
	wantHistory(t, f.repo, "git stack sync (keep)")
	if _, err := f.merge(t, app.MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	wantHistory(t, f.repo)
	if keepAliveExists(t, f.dir) {
		t.Error("the keep alive ref should be gone")
	}
}
