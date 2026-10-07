package app_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
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

func TestMergeChecks(t *testing.T) {
	// withSteps records what each progress step returned, so a refusal can
	// be seen to come from inside the merge step.
	withSteps := func(f *syncFixture) *[]error {
		var errs []error
		f.deps.Progress = func(ctx context.Context, _ app.Phase, _ string, fn func(context.Context) error) error {
			err := fn(ctx)
			errs = append(errs, err)
			return err
		}
		return &errs
	}

	t.Run("failing refuses", func(t *testing.T) {
		f := mergeFixture(t)
		steps := withSteps(f)
		f.forge.checks = map[int]forge.CheckSummary{
			7: {Failing: []string{"lint", "test (ubuntu-latest)"}},
			8: {Pending: []string{"build"}},
		}
		_, err := f.merge(t, app.MergeOptions{})
		se, ok := errors.AsType[*stack.Error](err)
		if !ok || se.Kind != stack.KindChecksFailing || se.Msg != "1 pull request has failing checks; nothing was merged" {
			t.Fatalf("err = %v", err)
		}
		want := []stack.PRChecks{
			{Number: 7, Branch: "a", Failing: []string{"lint", "test (ubuntu-latest)"}},
			{Number: 8, Branch: "b", Pending: []string{"build"}},
		}
		if !reflect.DeepEqual(se.Checks, want) {
			t.Errorf("checks = %+v", se.Checks)
		}
		if len(se.NextSteps) != 1 || !strings.Contains(se.NextSteps[0], "git stack merge --force") {
			t.Errorf("steps = %q", se.NextSteps)
		}
		if len(f.forge.merges) != 0 {
			t.Errorf("nothing should be merged: %v", f.forge.merges)
		}
		if len(f.forge.checked) != 1 || !slices.Equal(f.forge.checked[0], []int{7, 8}) {
			t.Errorf("checks asked for %v, want one call for [7 8]", f.forge.checked)
		}
		if len(*steps) != 1 || !errors.Is((*steps)[0], err) {
			t.Errorf("the refusal should end the merge step: %v", *steps)
		}
	})

	t.Run("pending refuses", func(t *testing.T) {
		f := mergeFixture(t)
		f.forge.checks = map[int]forge.CheckSummary{8: {Pending: []string{"build", "test"}}}
		_, err := f.merge(t, app.MergeOptions{})
		se, ok := errors.AsType[*stack.Error](err)
		if !ok || se.Kind != stack.KindChecksPending || se.Msg != "1 pull request still has checks running; nothing was merged" {
			t.Fatalf("err = %v", err)
		}
		if len(se.Checks) != 1 || se.Checks[0].Number != 8 || !strings.Contains(se.NextSteps[0], "wait") {
			t.Errorf("error = %+v", se)
		}
		if len(f.forge.merges) != 0 {
			t.Errorf("nothing should be merged: %v", f.forge.merges)
		}
	})

	t.Run("several pending", func(t *testing.T) {
		f := mergeFixture(t)
		f.forge.checks = map[int]forge.CheckSummary{7: {Pending: []string{"a"}}, 8: {Pending: []string{"b"}}}
		_, err := f.merge(t, app.MergeOptions{})
		if err == nil || err.Error() != "2 pull requests still have checks running; nothing was merged" {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("clean merges", func(t *testing.T) {
		f := mergeFixture(t)
		f.forge.checks = map[int]forge.CheckSummary{7: {}} // 8 has no checks at all
		res, err := f.merge(t, app.MergeOptions{NoSync: true})
		if err != nil || res.Status != forge.MergeMerged || len(res.Notices) != 0 {
			t.Fatalf("merge = %+v, %v", res, err)
		}
		if len(f.forge.merges) != 1 {
			t.Errorf("merges = %v", f.forge.merges)
		}
	})

	t.Run("already merged PRs aren't checked", func(t *testing.T) {
		f := mergeFixture(t)
		f.forge.prs[0].State = forge.StateMerged
		if _, err := f.merge(t, app.MergeOptions{NoSync: true}); err != nil {
			t.Fatal(err)
		}
		if len(f.forge.checked) != 1 || !slices.Equal(f.forge.checked[0], []int{8}) {
			t.Errorf("checked %v, want [[8]]", f.forge.checked)
		}
	})

	t.Run("force skips the checks", func(t *testing.T) {
		f := mergeFixture(t)
		f.forge.checks = map[int]forge.CheckSummary{7: {Failing: []string{"lint"}}}
		res, err := f.merge(t, app.MergeOptions{NoSync: true, Force: true})
		if err != nil || res.Status != forge.MergeMerged {
			t.Fatalf("merge = %+v, %v", res, err)
		}
		if len(f.forge.checked) != 0 || len(f.forge.merges) != 1 {
			t.Errorf("checked %v, merged %v", f.forge.checked, f.forge.merges)
		}
	})

	t.Run("an unreadable rollup doesn't block", func(t *testing.T) {
		f := mergeFixture(t)
		f.forge.checksErr = stack.New(stack.KindAPIFailure, "GitHub request failed").WithDetail("gh: HTTP 502: Bad Gateway")
		res, err := f.merge(t, app.MergeOptions{NoSync: true})
		if err != nil || res.Status != forge.MergeMerged || len(f.forge.merges) != 1 {
			t.Fatalf("merge = %+v, %v", res, err)
		}
		if len(res.Notices) != 1 || !strings.Contains(res.Notices[0], "couldn't verify") || !strings.Contains(res.Notices[0], "(HTTP 502: Bad Gateway)") {
			t.Errorf("notices = %q", res.Notices)
		}
	})
}
