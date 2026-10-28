package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/config"
)

// TestSubmitFixesBaseWhenGhStackSkipsQueuedBranches: gh stack bases a new
// PR on trunk when the branches below are queued for merge, which shows the
// whole stack's diff. Submit moves it back onto the parent and says so.
func TestSubmitFixesBaseWhenGhStackSkipsQueuedBranches(t *testing.T) {
	deps, sf, fg, repo, _ := submitFixture(t) // a has open #7; b is new
	sf.baseOnTrunk = true
	res, err := app.New(deps).Submit(context.Background(), repo, app.SubmitOptions{NoEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	b := res.PullRequests[1]
	if b.Number == 0 || !b.BaseFixed {
		t.Fatalf("b's base should have been fixed: %+v", b)
	}
	up, ok := fg.updates[b.Number]
	if !ok || up.Base == nil || *up.Base != "a" {
		t.Errorf("expected base update to a, got %+v", fg.updates)
	}
	found := false
	for _, n := range res.Notices {
		if strings.Contains(n, "moved the base of") && strings.Contains(n, "back to a") {
			found = true
		}
	}
	if !found {
		t.Errorf("notice missing: %v", res.Notices)
	}

	// When the branch below really has merged and sync kept it (the prune
	// policy says never), trunk is the right base.
	f2, sf2 := submitSyncFixture(t)
	f2.deps.Config.SyncPrune = config.SyncPruneNever
	sf2.baseOnTrunk = true
	f2.mergeOnRemote(t, "a", 0)
	fg2 := f2.forge
	res2, err := app.New(f2.deps).Submit(context.Background(), f2.repo, app.SubmitOptions{NoEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Sync == nil || len(res2.Sync.Deleted) != 0 || len(res2.PullRequests) != 1 || res2.PullRequests[0].Branch != "b" {
		t.Fatalf("a should have been kept by the sync and left out of the submit: %+v", res2)
	}
	for _, pr := range res2.PullRequests {
		if pr.BaseFixed {
			t.Errorf("no base fix expected when the parent merged: %+v", pr)
		}
	}
	for n := range fg2.updates {
		if fg2.updates[n].Base != nil {
			t.Errorf("unexpected base update: %+v", fg2.updates)
		}
	}

	// Correct bases are left alone.
	deps3, sf3, fg3, repo3, _ := submitFixture(t)
	if _, err := app.New(deps3).Submit(context.Background(), repo3, app.SubmitOptions{NoEdit: true}); err != nil {
		t.Fatal(err)
	}
	for n := range fg3.updates {
		if fg3.updates[n].Base != nil {
			t.Errorf("unexpected base update: %+v", fg3.updates)
		}
	}
	if len(sf3.opts) != 1 {
		t.Errorf("no base fix means a single backend submit, got %d", len(sf3.opts))
	}
}
