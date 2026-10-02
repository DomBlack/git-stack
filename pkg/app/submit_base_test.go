package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
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

	// When the branch below really has merged, trunk is the right base.
	deps2, sf2, fg2, repo2, _ := submitFixture(t)
	sf2.baseOnTrunk = true
	fg2.prs[0].State = forge.StateMerged
	res2, err := app.New(deps2).Submit(context.Background(), repo2, app.SubmitOptions{NoEdit: true})
	if err != nil {
		t.Fatal(err)
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
