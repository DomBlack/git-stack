package app_test

import (
	"context"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
)

// neverPrompter fails the test if any question is asked.
type neverPrompter struct{ t *testing.T }

func (p neverPrompter) Confirm(q string, _ bool) (bool, error) {
	p.t.Errorf("unexpected Confirm(%q)", q)
	return false, nil
}

func (p neverPrompter) Select(q string, _ []string) (int, error) {
	p.t.Errorf("unexpected Select(%q)", q)
	return 0, nil
}

// TestSubmitDoesNotAskWhenEveryBranchHasAPR covers running `git ss` a
// second time: nothing new is being created, so draft vs ready for review
// is irrelevant and the question must not be asked.
func TestSubmitDoesNotAskWhenEveryBranchHasAPR(t *testing.T) {
	deps, sf, fg, repo, _ := submitFixture(t)
	fg.prs = append(fg.prs, forge.PullRequest{Number: 8, Head: "b", Base: "a", State: forge.StateOpen, URL: "u/b"})
	deps.Prompter = neverPrompter{t: t}
	a := app.New(deps)

	res, err := a.Submit(context.Background(), repo, app.SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.opts) != 1 {
		t.Fatalf("submit should still push: %+v", sf.opts)
	}
	if sf.opts[0].Interactive {
		t.Error("no new PRs means no editor either")
	}
	for _, pr := range res.PullRequests {
		if pr.Created || pr.WouldCreate || pr.Number == 0 {
			t.Errorf("every branch already had a PR: %+v", pr)
		}
	}

	// A dry run with nothing new should not ask either.
	if _, err := a.Submit(context.Background(), repo, app.SubmitOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
}
