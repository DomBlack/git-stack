package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/DomBlack/git-stack/pkg/absorb"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// AbsorbOptions mirrors `gt absorb`.
type AbsorbOptions struct {
	Staging StagingMode
	// DryRun plans and shows where everything would go, changing no commit.
	DryRun bool
	// Force skips the confirmation on a terminal.
	Force bool
	// Preview, when set, is shown the plan before anything is asked or
	// changed. The CLI renders it; the MCP tool reads it off the result.
	Preview func(*AbsorbPlan)
}

// AbsorbTarget is the commit a hunk goes into.
type AbsorbTarget struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Branch  string `json:"branch"`
}

// AbsorbHunk is one staged run of changed lines and where it goes.
type AbsorbHunk struct {
	Path string `json:"path"`
	// OldStart and OldCount locate the lines in HEAD; an insertion has
	// OldCount 0 and goes after line OldStart.
	OldStart int `json:"old_start"`
	OldCount int `json:"old_count"`
	NewCount int `json:"new_count"`
	// Target is nil when no commit in the stack changed those lines; the
	// hunk stays staged.
	Target *AbsorbTarget `json:"target,omitempty"`
}

// AbsorbSkipped is a staged file absorb never plans, and why.
type AbsorbSkipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// AbsorbPlan is where every staged change goes.
type AbsorbPlan struct {
	Hunks   []AbsorbHunk    `json:"hunks"`
	Skipped []AbsorbSkipped `json:"skipped,omitempty"`
	// Absorbed and Unabsorbed count hunks; Commits counts the distinct
	// destination commits.
	Absorbed   int `json:"absorbed"`
	Unabsorbed int `json:"unabsorbed"`
	Commits    int `json:"commits"`
}

// AbsorbResult reports what absorb did.
type AbsorbResult struct {
	Branch string     `json:"branch"`
	Plan   AbsorbPlan `json:"plan"`
	DryRun bool       `json:"dry_run"`
	// Applied is false on a dry run, when there was nothing to absorb or
	// when the user said no.
	Applied bool `json:"applied"`
	// Rewritten lists the branches that moved, bottom to top.
	Rewritten []BranchMove `json:"rewritten,omitempty"`
	// Restacked lists the branches above that were rebased afterwards.
	Restacked []string `json:"restacked,omitempty"`
	Notices   []string `json:"notices,omitempty"`
}

const absorbCommand = "git stack absorb"

// Absorb folds each staged hunk into the commit of the current stack that
// last changed those lines, rewrites that commit and everything above it
// up to the current branch, and restacks the branches above.
func (a *App) Absorb(ctx context.Context, repo git.Repo, o AbsorbOptions) (AbsorbResult, error) {
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return AbsorbResult{}, err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		if errors.Is(err, git.ErrDetached) {
			return AbsorbResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch first")
		}
		return AbsorbResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return AbsorbResult{}, err
	}
	if graph.IsTrunk(current) {
		return AbsorbResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; absorb works on stacked branches", current)
	}
	s, i, ok := graph.StackOf(current)
	if !ok {
		return AbsorbResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", current)
	}
	if s.Branches[i].Merged() {
		return AbsorbResult{}, stack.Newf(stack.KindInvalidArgs, "%s's pull request has merged, so its commits can't be rewritten", current).
			WithSteps("run git stack sync to clean it up")
	}
	staged, err := a.stage(ctx, repo, o.Staging)
	if err != nil {
		return AbsorbResult{}, err
	}
	if !staged {
		return AbsorbResult{}, stack.New(stack.KindInvalidArgs, "nothing to absorb: no staged changes").
			WithSteps("stage the changes to absorb (or use -a / -u / -p)")
	}
	run, err := a.absorbCandidates(ctx, repo, s, i)
	if err != nil {
		return AbsorbResult{}, err
	}
	diff, err := a.d.Git.StagedPatch(ctx, repo)
	if err != nil {
		return AbsorbResult{}, err
	}
	files, err := absorb.Parse(diff)
	if err != nil {
		return AbsorbResult{}, fmt.Errorf("reading the staged diff: %w", err)
	}
	run.plan = absorb.Compute(run.commits, files)
	res := AbsorbResult{Branch: current, Plan: absorbPlanOf(&run.plan), DryRun: o.DryRun}
	if o.Preview != nil {
		o.Preview(&res.Plan)
	}
	if res.Plan.Absorbed == 0 || o.DryRun {
		return res, nil
	}
	if !o.Force && a.d.Prompter != nil {
		q := fmt.Sprintf("Absorb %d %s into %d %s?", res.Plan.Absorbed, pluralise(res.Plan.Absorbed, "hunk", "hunks"), res.Plan.Commits, pluralise(res.Plan.Commits, "commit", "commits"))
		yes, err := a.d.Prompter.Confirm(q, true)
		if err != nil {
			return res, err
		}
		if !yes {
			res.Notices = append(res.Notices, "nothing was absorbed")
			return res, nil
		}
	}
	return a.absorbApply(ctx, repo, run, res)
}

// absorbRun is one absorb in flight: the stack, the candidate commits
// oldest first, the tips of the candidate branches, and the plan.
type absorbRun struct {
	stack   *stack.Stack
	current int // index of the current branch in stack.Branches
	lowest  int // index of the lowest candidate branch
	commits []absorb.Commit
	local   map[string]git.Branch
	plan    absorb.Plan
}

// absorbCandidates collects the commits a hunk may go into: those of the
// current branch and the branches below it, stopping before the first
// branch whose pull request has merged. Every candidate branch but the
// bottom of the stack must sit on its parent; the bottom may be behind
// trunk, and its own commits are found the way restack finds them: from the
// trunk tip when the branch already contains it (so a branch rebased onto a
// newer trunk by hand doesn't claim trunk's commits), otherwise from its
// recorded base, otherwise from the merge base with trunk.
func (a *App) absorbCandidates(ctx context.Context, repo git.Repo, s *stack.Stack, i int) (*absorbRun, error) {
	local, err := a.localBranches(ctx, repo)
	if err != nil {
		return nil, err
	}
	lo := i
	for lo > 0 && !s.Branches[lo-1].Merged() {
		lo--
	}
	run := &absorbRun{stack: s, current: i, lowest: lo, local: local}
	for j := lo; j <= i; j++ {
		b := s.Branches[j]
		lb, ok := local[b.Name]
		if !ok {
			return nil, stack.Newf(stack.KindInvalidArgs, "%s no longer exists", b.Name)
		}
		var from string
		if j == 0 {
			trunk, ok := local[s.Trunk]
			if !ok {
				return nil, stack.Newf(stack.KindInvalidArgs, "trunk %s no longer exists", s.Trunk)
			}
			switch {
			case a.isAncestor(ctx, repo, trunk.Head, lb.Head):
				from = trunk.Head
			case b.Base != "" && a.isAncestor(ctx, repo, b.Base, lb.Head):
				from = b.Base
			default:
				if from, err = a.d.Git.MergeBase(ctx, repo, lb.Head, trunk.Head); err != nil {
					return nil, err
				}
			}
		} else {
			parent := s.Branches[j-1].Name
			pb, ok := local[parent]
			if !ok {
				return nil, stack.Newf(stack.KindInvalidArgs, "%s no longer exists", parent)
			}
			if !a.isAncestor(ctx, repo, pb.Head, lb.Head) {
				return nil, stack.Newf(stack.KindInvalidArgs, "%s needs restacking onto %s before its commits can be rewritten", b.Name, parent).
					WithSteps("git stack restack, then " + absorbCommand + " again")
			}
			from = pb.Head
		}
		if merges, err := a.d.Git.HasMerges(ctx, repo, from, lb.Head); err != nil {
			return nil, err
		} else if merges {
			return nil, stack.Newf(stack.KindInvalidArgs, "%s has a merge commit, which absorb can't rewrite", b.Name).
				WithSteps("fold the merge into plain commits first")
		}
		commits, err := a.d.Git.Commits(ctx, repo, lb.Head, from)
		if err != nil {
			return nil, err
		}
		for _, c := range slices.Backward(commits) { // oldest first
			diff, err := a.d.Git.DiffCommit(ctx, repo, c.SHA)
			if err != nil {
				return nil, err
			}
			files, err := absorb.Parse(diff)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", c.SHA, err)
			}
			run.commits = append(run.commits, absorb.Commit{SHA: c.SHA, Subject: c.Subject, Branch: b.Name, Files: files})
		}
	}
	return run, nil
}

// absorbPlanOf is the plan as a result carries it.
func absorbPlanOf(p *absorb.Plan) AbsorbPlan {
	out := AbsorbPlan{Hunks: []AbsorbHunk{}, Absorbed: p.Absorbed(), Unabsorbed: p.Unabsorbed(), Commits: p.Targets()}
	for _, h := range p.Hunks {
		ah := AbsorbHunk{Path: h.Path, OldStart: h.Hunk.OldStart, OldCount: h.Hunk.OldCount, NewCount: h.Hunk.NewCount}
		if h.Target != nil {
			ah.Target = &AbsorbTarget{SHA: h.Target.SHA, Subject: h.Target.Subject, Branch: h.Target.Branch}
		}
		out.Hunks = append(out.Hunks, ah)
	}
	for _, s := range p.Skipped {
		out.Skipped = append(out.Skipped, AbsorbSkipped{Path: s.Path, Reason: s.Reason})
	}
	return out
}

// absorbApply is filled in by the next task.
func (a *App) absorbApply(_ context.Context, _ git.Repo, _ *absorbRun, res AbsorbResult) (AbsorbResult, error) {
	return res, stack.New(stack.KindUnsupported, "absorb cannot apply yet")
}
