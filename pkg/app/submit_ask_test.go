package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/config"
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

// TestSubmitStreamedOutputIsNotEchoed checks that output the backend already
// relayed live is not returned for printing a second time.
func TestSubmitStreamedOutputIsNotEchoed(t *testing.T) {
	deps, sf, _, repo, _ := submitFixture(t)
	sf.streamed = true
	sf.output = "✓ Created 1 PR"
	res, err := app.New(deps).Submit(context.Background(), repo, app.SubmitOptions{NoEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "" {
		t.Errorf("streamed output must not be echoed, got %q", res.Output)
	}
	sres, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sres.Output != "" {
		t.Errorf("streamed sync output must not be echoed, got %q", sres.Output)
	}
}

// askPrompter answers every Confirm with a fixed answer and records the question.
type askPrompter struct {
	answer bool
	asked  []string
}

func (p *askPrompter) Confirm(q string, _ bool) (bool, error) {
	p.asked = append(p.asked, q)
	return p.answer, nil
}
func (p *askPrompter) Select(string, []string) (int, error) { return 0, nil }

// TestSyncAsksAboutMergedBranches: gh stack only prunes with --prune and
// cannot prompt through us, so sync asks on a terminal and otherwise says
// what it kept.
func TestSyncAsksAboutMergedBranches(t *testing.T) {
	// a's PR has merged; policy is ask.
	deps, sf, fg, repo, _ := submitFixture(t)
	deps.Config.SyncPrune = config.SyncPruneAsk
	fg.prs[0].State = forge.StateMerged
	ap := &askPrompter{answer: true}
	deps.Prompter = ap
	res, err := app.New(deps).Sync(context.Background(), repo, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ap.asked) != 1 || !strings.Contains(ap.asked[0], "Delete 1 merged branch (a)") {
		t.Errorf("question = %v", ap.asked)
	}
	if !sf.syncs[0].Prune || !res.Pruned {
		t.Errorf("yes at the prompt should prune: %+v %+v", sf.syncs, res)
	}

	// Declining keeps them.
	deps2, sf2, fg2, repo2, _ := submitFixture(t)
	deps2.Config.SyncPrune = config.SyncPruneAsk
	fg2.prs[0].State = forge.StateMerged
	deps2.Prompter = &askPrompter{answer: false}
	res2, err := app.New(deps2).Sync(context.Background(), repo2, app.SyncOptions{})
	if err != nil || sf2.syncs[0].Prune || res2.Pruned {
		t.Errorf("no at the prompt must not prune: %+v %+v %v", sf2.syncs, res2, err)
	}

	// No prompter (piped, MCP): keep and say so.
	deps3, sf3, fg3, repo3, _ := submitFixture(t)
	deps3.Config.SyncPrune = config.SyncPruneAsk
	fg3.prs[0].State = forge.StateMerged
	res3, err := app.New(deps3).Sync(context.Background(), repo3, app.SyncOptions{})
	if err != nil || sf3.syncs[0].Prune {
		t.Fatalf("non interactive must not prune: %+v %v", sf3.syncs, err)
	}
	found := false
	for _, n := range res3.Notices {
		if strings.Contains(n, "1 merged branch (a) kept") && strings.Contains(n, "sync -f") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a kept notice, got %v", res3.Notices)
	}

	// Nothing merged: no question asked.
	deps4, _, _, repo4, _ := submitFixture(t)
	deps4.Config.SyncPrune = config.SyncPruneAsk
	ap4 := &askPrompter{answer: true}
	deps4.Prompter = ap4
	if _, err := app.New(deps4).Sync(context.Background(), repo4, app.SyncOptions{}); err != nil || len(ap4.asked) != 0 {
		t.Errorf("no merged branches, no question: %v %v", ap4.asked, err)
	}
}
