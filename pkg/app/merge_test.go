package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// branches lists the merged branches as "a,b".
func branches(res app.MergeResult) string {
	names := make([]string, len(res.PullRequests))
	for i, pr := range res.PullRequests {
		names[i] = pr.Branch
	}
	return strings.Join(names, ",")
}

// mergeFixture is the sync fixture (main -> a -> b, on b) with open PRs for
// both branches.
func mergeFixture(t *testing.T) *syncFixture {
	t.Helper()
	f := newSyncFixture(t)
	f.forge.prs = append(f.forge.prs, forge.PullRequest{Number: 8, Head: "b", Base: "a", State: forge.StateOpen, URL: "u/b"})
	return f
}

func (f *syncFixture) merge(t *testing.T, o app.MergeOptions) (app.MergeResult, error) {
	t.Helper()
	return app.New(f.deps).Merge(context.Background(), f.repo, o)
}

func TestMergeUpToTheCurrentBranch(t *testing.T) {
	f := mergeFixture(t)
	f.deps.Config.MergeMethod = "rebase"
	res, err := f.merge(t, app.MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if branches(res) != "a,b" || res.PullRequests[1].Number != 8 || res.PullRequests[1].URL != "u/b" || res.Trunk != "main" {
		t.Errorf("result = %+v", res)
	}
	if res.Status != forge.MergeMerged || res.SHA != "feedface" {
		t.Errorf("status = %s %s", res.Status, res.SHA)
	}
	// One call to the forge, for the top PR, with the configured method.
	if len(f.forge.merges) != 1 || f.forge.merges[0] != (mergeCall{8, forge.MergeRebase}) {
		t.Errorf("forge calls = %+v", f.forge.merges)
	}
	if res.Sync == nil {
		t.Error("a sync should follow the merge")
	}
}

func TestMergeUpToALowerBranchWithoutSync(t *testing.T) {
	f := mergeFixture(t)
	res, err := f.merge(t, app.MergeOptions{Branch: "a", Method: forge.MergeSquash, NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	if branches(res) != "a" || res.Sync != nil {
		t.Errorf("result = %+v", res)
	}
	if len(f.forge.merges) != 1 || f.forge.merges[0] != (mergeCall{7, forge.MergeSquash}) {
		t.Errorf("forge calls = %+v", f.forge.merges)
	}
}

func TestMergeRefusals(t *testing.T) {
	kind := func(err error) stack.Kind {
		var se *stack.Error
		if errors.As(err, &se) {
			return se.Kind
		}
		return -1
	}

	f := mergeFixture(t)
	f.forge.prs[0].State = forge.StateDraft
	_, err := f.merge(t, app.MergeOptions{})
	if kind(err) != stack.KindInvalidArgs || !strings.Contains(err.Error(), "draft") || len(f.forge.merges) != 0 {
		t.Errorf("draft below: %v (merges %v)", err, f.forge.merges)
	}

	f = mergeFixture(t)
	f.forge.prs = f.forge.prs[:1] // b has no PR
	_, err = f.merge(t, app.MergeOptions{})
	if kind(err) != stack.KindInvalidArgs || !strings.Contains(err.Error(), "no pull request") {
		t.Errorf("missing PR: %v", err)
	}

	f = mergeFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	_, err = f.merge(t, app.MergeOptions{})
	if kind(err) != stack.KindNotInStack {
		t.Errorf("from trunk: %v", err)
	}

	f = mergeFixture(t)
	f.forge.prs[1].State = forge.StateMerged
	_, err = f.merge(t, app.MergeOptions{})
	if kind(err) != stack.KindInvalidArgs || !strings.Contains(err.Error(), "already merged") {
		t.Errorf("already merged: %v", err)
	}

	// A merged PR lower down is skipped, not an error.
	f = mergeFixture(t)
	f.forge.prs[0].State = forge.StateMerged
	res, err := f.merge(t, app.MergeOptions{NoSync: true})
	if err != nil || branches(res) != "b" {
		t.Errorf("merged below: %+v %v", res, err)
	}

	f = mergeFixture(t)
	f.forge.mergeErr = stack.New(stack.KindAPIFailure, "GitHub refused to merge the stack: checks failing")
	_, err = f.merge(t, app.MergeOptions{})
	if kind(err) != stack.KindAPIFailure {
		t.Errorf("forge refusal should pass through: %v", err)
	}
}
