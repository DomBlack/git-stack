package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// MergeOptions controls Merge.
type MergeOptions struct {
	// Branch is the highest branch to merge; every branch below it in the
	// stack goes with it. Empty means the current branch.
	Branch string
	// Method overrides stack.merge.method; empty leaves it to the config,
	// and an empty config leaves it to the repository's settings.
	Method forge.MergeMethod
	// NoSync skips the sync that normally follows a merge.
	NoSync bool
}

// MergedPR is one pull request that landed.
type MergedPR struct {
	Branch string `json:"branch"`
	Number int    `json:"number"`
	URL    string `json:"url,omitempty"`
}

// MergeResult reports what landed.
type MergeResult struct {
	Trunk string `json:"trunk"`
	// PullRequests that were merged, bottom to top.
	PullRequests []MergedPR        `json:"pullRequests"`
	Status       forge.MergeStatus `json:"status"`
	// SHA is the merge commit on trunk; empty when enqueued.
	SHA string `json:"sha,omitempty"`
	// Sync is what the sync afterwards did, when it ran.
	Sync    *SyncResult `json:"sync,omitempty"`
	Notices []string    `json:"notices,omitempty"`
}

// Merge lands the pull requests of the current stack up to and including a
// branch, in one all or nothing operation on the forge, then syncs so the
// merged branches go and whatever is left above is restacked onto trunk.
// Nothing is merged while any pull request in the way is missing, a draft or
// closed; the forge's own rules (required checks, reviews) apply on top.
func (a *App) Merge(ctx context.Context, repo git.Repo, o MergeOptions) (MergeResult, error) {
	if a.d.Forge == nil {
		return MergeResult{}, ErrNoForge
	}
	target := o.Branch
	if target == "" {
		cur, err := a.d.Git.CurrentBranch(ctx, repo)
		if err != nil {
			if errors.Is(err, git.ErrDetached) {
				return MergeResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch or name one")
			}
			return MergeResult{}, err
		}
		target = cur
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return MergeResult{}, err
	}
	s, i, ok := graph.StackOf(target)
	if !ok {
		if graph.IsTrunk(target) {
			return MergeResult{}, stack.Newf(stack.KindNotInStack, "%s is a trunk; name the branch to merge up to", target).
				WithSteps("git stack merge <branch> merges that branch and everything below it")
		}
		return MergeResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", target)
	}
	res := MergeResult{Trunk: s.Trunk}

	// Every pull request in the way must be open and ready; the forge would
	// refuse anyway, but it says so one PR at a time and with less context.
	prs := PRsFor(a.syncPRs(ctx, repo))
	for _, name := range s.Names()[:i+1] {
		pr, ok := prs[name]
		switch {
		case !ok:
			return res, stack.Newf(stack.KindInvalidArgs, "%s has no pull request yet", name).
				WithSteps("run git stack submit first")
		case pr.State == forge.StateMerged:
			if name == target {
				return res, stack.Newf(stack.KindInvalidArgs, "%s's pull request #%d is already merged", name, pr.Number).
					WithSteps("run git stack sync to tidy up")
			}
			continue // already landed; the forge skips it too
		case pr.State == forge.StateDraft:
			return res, stack.Newf(stack.KindInvalidArgs, "%s's pull request #%d is still a draft", name, pr.Number).
				WithSteps("run git stack submit --publish to mark the stack ready for review")
		case pr.State == forge.StateClosed:
			return res, stack.Newf(stack.KindInvalidArgs, "%s's pull request #%d was closed", name, pr.Number).
				WithSteps("reopen it on the forge, or run git stack sync to drop the branch")
		}
		res.PullRequests = append(res.PullRequests, MergedPR{Branch: name, Number: pr.Number, URL: pr.URL})
	}
	if len(res.PullRequests) == 0 {
		return res, stack.Newf(stack.KindInvalidArgs, "nothing to merge up to %s", target)
	}

	method := o.Method
	if method == forge.MergeDefault {
		method = forge.MergeMethod(a.d.Config.MergeMethod)
	}
	n := len(res.PullRequests)
	top := res.PullRequests[n-1].Number
	err = a.progress(ctx, PhaseMerge, fmt.Sprintf("Merging %d pull %s into %s", n, pluralise(n, "request", "requests"), s.Trunk), func(ctx context.Context) error {
		out, err := a.d.Forge.MergeStack(ctx, repo, top, method)
		if err != nil {
			return err
		}
		res.Status, res.SHA = out.Status, out.SHA
		return nil
	})
	if err != nil {
		return res, err
	}
	if res.Status == forge.MergeEnqueued {
		res.Notices = append(res.Notices, fmt.Sprintf("%s has a merge queue; the pull requests land when it gets to them, then git stack sync tidies up", s.Trunk))
		return res, nil
	}
	if o.NoSync {
		return res, nil
	}
	sync, err := a.Sync(ctx, repo, SyncOptions{})
	res.Sync = &sync
	return res, err
}
