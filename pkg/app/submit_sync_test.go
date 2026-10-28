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

// submitSyncFixture is the sync fixture (main -> a -> b with a bare origin,
// a's PR open, HEAD on b) with a submit backend, so a submit can sync.
func submitSyncFixture(t *testing.T) (*syncFixture, *submitFake) {
	t.Helper()
	f := newSyncFixture(t)
	sf := &submitFake{forge: f.forge, graph: f.meta.g}
	f.deps.Submit = sf
	return f, sf
}

// A pull request lower in the stack merged since the last sync, and the
// metadata doesn't know yet. Pushing as is would open the new PR with the
// merged branch's commits still in it, so submit syncs first: the merged
// branch goes, the one above lands on trunk, and that is what gets submitted.
func TestSubmitSyncsFirstWhenABranchBelowHasMerged(t *testing.T) {
	f, sf := submitSyncFixture(t)
	merge := f.mergeOnRemote(t, "a", 0)
	res, err := app.New(f.deps).Submit(context.Background(), f.repo, app.SubmitOptions{NoEdit: true, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Merged, []string{"a"}) {
		t.Errorf("merged = %v, want [a]", res.Merged)
	}
	if res.Sync == nil {
		t.Fatal("submit should have synced first")
	}
	if d, ok := deleted(*res.Sync, "a"); !ok || d.Reason != app.ReasonMerged || branchExists(t, f.dir, "a") {
		t.Errorf("a should have been deleted as merged: %+v", res.Sync.Deleted)
	}
	if got := f.rev(t, "b^"); got != merge {
		t.Errorf("b should sit on the merge commit %s, is on %s", merge, got)
	}
	if !slices.Equal(res.Stack, []string{"b"}) {
		t.Errorf("stack = %v, want [b]", res.Stack)
	}
	if len(res.PullRequests) != 1 || res.PullRequests[0].Branch != "b" || !res.PullRequests[0].Created {
		t.Errorf("pull requests = %+v, want b created", res.PullRequests)
	}
	if len(sf.opts) != 1 {
		t.Errorf("gh stack submit should have run once, ran %d times", len(sf.opts))
	}
	pr := app.PRsFor(f.forge.prs)["b"]
	if pr.Base != "main" {
		t.Errorf("b's PR is based on %s, want main", pr.Base)
	}
}

// A dry run changes nothing, so it can't sync; it says which branches a
// real submit would sync away and leaves them out of the plan.
func TestSubmitDryRunReportsMergedBranchesWithoutSyncing(t *testing.T) {
	f, sf := submitSyncFixture(t)
	f.mergeOnRemote(t, "a", 0)
	res, err := app.New(f.deps).Submit(context.Background(), f.repo, app.SubmitOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Merged, []string{"a"}) || res.Sync != nil || len(sf.opts) != 0 {
		t.Errorf("dry run must report a as merged and not sync: %+v", res)
	}
	if !branchExists(t, f.dir, "a") {
		t.Error("a dry run must not delete anything")
	}
	if len(res.PullRequests) != 1 || res.PullRequests[0].Branch != "b" || !res.PullRequests[0].WouldCreate {
		t.Errorf("plan = %+v, want only b", res.PullRequests)
	}
	if n := strings.Join(res.Notices, "\n"); !strings.Contains(n, "a's pull request has merged") {
		t.Errorf("notices = %v", res.Notices)
	}
}

// Submitting from the merged branch itself still submits the rest of the
// stack: sync moves the checkout to trunk and the branches above carry on.
func TestSubmitFromAMergedBranchSubmitsWhatIsLeft(t *testing.T) {
	f, _ := submitSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "a")
	f.mergeOnRemote(t, "a", 0)
	res, err := app.New(f.deps).Submit(context.Background(), f.repo, app.SubmitOptions{NoEdit: true, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if branchExists(t, f.dir, "a") || gittest.Run(t, f.dir, "branch", "--show-current") != "main" {
		t.Error("a should be gone and the checkout on main")
	}
	if len(res.PullRequests) != 1 || res.PullRequests[0].Branch != "b" || !res.PullRequests[0].Created {
		t.Errorf("pull requests = %+v, want b created", res.PullRequests)
	}
}

// With --down the merged branch is the top of what was asked for, so once
// sync has deleted it there is nothing left to submit.
func TestSubmitDownOnAMergedBranchHasNothingLeft(t *testing.T) {
	f, sf := submitSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "a")
	f.mergeOnRemote(t, "a", 0)
	res, err := app.New(f.deps).Submit(context.Background(), f.repo, app.SubmitOptions{NoEdit: true, Down: true})
	if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindInvalidArgs || !strings.Contains(se.Msg, "nothing to submit") {
		t.Fatalf("err = %v, want nothing to submit", err)
	}
	if res.Sync == nil || len(sf.opts) != 0 {
		t.Errorf("the sync should be reported and nothing pushed: %+v", res)
	}
}
