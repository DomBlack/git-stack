package app_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func updated(res app.SyncResult, name string) (app.BranchMove, bool) {
	i := slices.IndexFunc(res.Updated, func(m app.BranchMove) bool { return m.Name == name })
	if i < 0 {
		return app.BranchMove{}, false
	}
	return res.Updated[i], true
}

func TestSyncFastForwardsBranchTheRemoteAdvanced(t *testing.T) {
	f := newSyncFixture(t) // on b
	sha := f.advanceRemote(t, "a", "suggestion.txt")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := updated(res, "a"); !ok || u.To != sha || f.rev(t, "a") != sha {
		t.Errorf("updated = %+v", res.Updated)
	}
}

func TestSyncFastForwardsCheckedOutBranch(t *testing.T) {
	f := newSyncFixture(t) // on b
	sha := f.advanceRemote(t, "b", "suggestion.txt")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := updated(res, "b"); !ok || f.rev(t, "b") != sha {
		t.Errorf("updated = %+v", res.Updated)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "suggestion.txt")); err != nil {
		t.Error("working tree should have the remote file")
	}
}

// Local changes the fast forward doesn't touch come along, as with git
// pull; ones it would overwrite leave the branch where it was, named.
func TestSyncDirtyCheckedOutBranch(t *testing.T) {
	t.Run("changes elsewhere are kept", func(t *testing.T) {
		f := newSyncFixture(t) // on b
		sha := f.advanceRemote(t, "b", "suggestion.txt")
		gittest.WriteFile(t, f.dir, "b.txt", "edited")
		res, err := f.sync(t, app.SyncOptions{NoRestack: true})
		if err != nil || f.rev(t, "b") != sha || len(res.NotUpdated) != 0 {
			t.Fatalf("b should fast forward: %+v %+v %v", res.Updated, res.NotUpdated, err)
		}
		if b, _ := os.ReadFile(filepath.Join(f.dir, "b.txt")); string(b) != "edited" {
			t.Error("the local change must survive")
		}
	})
	t.Run("changes in the way stop it", func(t *testing.T) {
		f := newSyncFixture(t) // on b
		before := f.rev(t, "b")
		f.advanceRemote(t, "b", "b.txt")
		gittest.WriteFile(t, f.dir, "b.txt", "edited")
		res, err := f.sync(t, app.SyncOptions{NoRestack: true})
		if err != nil || f.rev(t, "b") != before {
			t.Fatalf("b must stay: %v", err)
		}
		n := res.NotUpdated
		if len(n) != 1 || n[0].Name != "b" || n[0].Reason != app.NotUpdatedDirty || n[0].Worktree == "" ||
			!slices.Equal(n[0].Changed, []string{"b.txt"}) || len(n[0].Untracked) != 0 {
			t.Errorf("not updated = %+v", n)
		}
	})
}

func TestSyncLocalAheadIsLeftForSubmit(t *testing.T) {
	f := newSyncFixture(t) // on b
	tip := gittest.Commit(t, f.dir, "more.txt", "m", "more on b")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil || len(res.Updated) != 0 || len(res.Behind) != 0 || f.rev(t, "b") != tip {
		t.Errorf("local ahead: %+v %+v %v", res.Updated, res.Behind, err)
	}
}

func TestSyncDivergedByRestackStaysQuiet(t *testing.T) {
	f := newSyncFixture(t)
	// Rebase b locally onto a moved a: same patches, different shas.
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "a2.txt", "a2", "more on a")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	gittest.Run(t, f.dir, "rebase", "-q", "a")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil || len(res.Behind) != 0 || len(res.Updated) != 0 {
		t.Errorf("own restack must not nag: %+v %+v %v", res.Behind, res.Updated, err)
	}
}

func TestSyncNoticesRealRemoteCommits(t *testing.T) {
	f := newSyncFixture(t) // on b
	gittest.Commit(t, f.dir, "mine.txt", "m", "mine")
	f.advanceRemote(t, "b", "theirs.txt")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Behind) != 1 || res.Behind[0].Name != "b" || res.Behind[0].Commits != 1 {
		t.Errorf("behind = %+v", res.Behind)
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "1 commit on origin") {
		t.Errorf("notice missing: %v", res.Notices)
	}
}

func TestSyncRemoteBranchGone(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "push", "-q", "origin", "--delete", "a")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil || len(res.Updated) != 0 || len(res.Behind) != 0 {
		t.Errorf("a deleted remote branch is nothing to report: %+v %+v %v", res.Updated, res.Behind, err)
	}
}
