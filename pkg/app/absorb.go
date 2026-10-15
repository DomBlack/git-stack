package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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

// syncSteps is what to do when the stack's metadata names a branch that is gone.
const syncSteps = "run git stack sync to tidy the stack's metadata"

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
			return nil, stack.Newf(stack.KindInvalidArgs, "%s no longer exists", b.Name).WithSteps(syncSteps)
		}
		var from string
		if j == 0 {
			trunk, ok := local[s.Trunk]
			if !ok {
				return nil, stack.Newf(stack.KindInvalidArgs, "trunk %s no longer exists", s.Trunk).WithSteps(syncSteps)
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
				return nil, stack.Newf(stack.KindInvalidArgs, "%s no longer exists", parent).WithSteps(syncSteps)
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

// absorbApply writes the rewritten commits, moves the affected branches
// in one ref transaction, updates the metadata and restacks what sits
// above the current branch. res comes back with Applied set as soon as
// the branches have moved, so a conflict in the restack afterwards still
// reports the absorb.
func (a *App) absorbApply(ctx context.Context, repo git.Repo, run *absorbRun, res AbsorbResult) (AbsorbResult, error) {
	rewritten := map[string]bool{}
	for _, sha := range run.plan.Rewrites {
		rewritten[sha] = true
	}
	subjects := map[string]string{}
	for _, c := range run.commits {
		subjects[c.SHA] = c.Subject
	}
	// The branches that move: every candidate whose tip is rewritten,
	// which is the contiguous run from the oldest destination's branch up
	// to the current one.
	type move struct {
		name, from, worktree string
	}
	var moves []move
	for j := run.lowest; j <= run.current; j++ {
		lb := run.local[run.stack.Branches[j].Name]
		if rewritten[lb.Head] {
			moves = append(moves, move{name: lb.Name, from: lb.Head, worktree: lb.Worktree})
		}
	}
	// A checkout elsewhere with changes can't be brought along, and the
	// branches above depend on this one moving, so refuse before any work.
	dirty := newDirtyCache()
	for _, m := range moves {
		if m.worktree != "" && m.worktree != repo.TopLevel && dirty.isDirty(ctx, a.d.Git, repo, m.worktree) {
			return res, stack.Newf(stack.KindInvalidArgs, "%s is checked out in %s with uncommitted changes, so it can't be rewritten", m.name, shortPath(m.worktree)).
				WithSteps("commit or stash them there, then " + absorbCommand + " again")
		}
	}

	n := res.Plan.Absorbed
	headline := fmt.Sprintf("Absorbing %d %s into %d %s", n, pluralise(n, "hunk", "hunks"), res.Plan.Commits, pluralise(res.Plan.Commits, "commit", "commits"))
	err := a.progress(ctx, PhaseAbsorb, headline, func(ctx context.Context) error {
		// 1. Objects only: every rewritten commit, oldest first, on the
		// previous rewritten one.
		newSHA := map[string]string{}
		parent := ""
		for _, sha := range run.plan.Rewrites {
			info, err := a.d.Git.CommitInfo(ctx, repo, sha)
			if err != nil {
				return err
			}
			if parent == "" {
				parent = info.Parent
			}
			tree, err := a.d.Git.PatchTree(ctx, repo, info.Tree, absorb.PatchFiles(run.plan.Patches[sha]))
			if err != nil {
				if pe, ok := errors.AsType[*git.PatchError](err); ok {
					return stack.Newf(stack.KindInvalidArgs, "the change for %q could not be applied to %s; nothing was changed", subjects[sha], sha[:7]).
						WithSteps("this is a bug in " + absorbCommand + "; please report it with the detail above").WithDetail(pe.Detail).WithCause(err)
				}
				return err
			}
			if parent, err = a.d.Git.CommitTree(ctx, repo, tree, parent, info); err != nil {
				return err
			}
			newSHA[sha] = parent
		}
		// 2. Every affected branch in one transaction, the current one
		// included: moving its ref leaves the index and working tree as
		// they are, so the absorbed hunks simply drop out of the staged
		// diff.
		updates := make([]git.RefUpdate, len(moves))
		for i, m := range moves {
			updates[i] = git.RefUpdate{Ref: "refs/heads/" + m.name, New: newSHA[m.from], Old: m.from}
		}
		if err := a.d.Git.UpdateRefs(ctx, repo, updates); err != nil {
			if git.LockPath(err) != "" {
				return stack.New(stack.KindLocked, "the branches could not be moved: "+err.Error()).
					WithSteps("run " + absorbCommand + " again").WithCause(err)
			}
			return stack.New(stack.KindInvalidArgs, "a branch moved while absorbing, so nothing was changed").
				WithSteps("run " + absorbCommand + " again").WithCause(err)
		}
		res.Applied = true
		for _, m := range moves {
			res.Rewritten = append(res.Rewritten, BranchMove{Name: m.name, Stack: run.stack.Bottom(), From: m.from, To: newSHA[m.from]})
		}
		// 3. Clean checkouts elsewhere follow their branch.
		for _, m := range moves {
			if m.worktree != "" && m.worktree != repo.TopLevel {
				if err := a.d.Git.ResetHard(ctx, worktreeRepo(repo, m.worktree), newSHA[m.from]); err != nil {
					res.Notices = append(res.Notices, fmt.Sprintf("%s was rewritten but its checkout in %s could not be updated: %v; run git reset --hard there", m.name, shortPath(m.worktree), err))
				}
			}
		}
		// 4. Heads for every moved branch; bases for the ones above the
		// lowest (their parent moved too). The lowest keeps its base.
		heads, bases := map[string]string{}, map[string]string{}
		for i, m := range moves {
			heads[m.name] = newSHA[m.from]
			if i > 0 {
				bases[m.name] = newSHA[moves[i-1].from]
			}
		}
		return a.updateHeadsAndBases(ctx, repo, heads, bases)
	})
	if err != nil {
		return res, err
	}
	// 5. Everything above, the way modify does it.
	children := run.stack.Names()[run.current+1:] // linear stacks: everything above is a descendant
	if len(children) == 0 {
		return res, nil
	}
	k := len(children)
	rr, err := a.restack(ctx, repo, RestackOptions{Scope: stack.ScopeUpstack}, restackCommand,
		fmt.Sprintf("Restacking %d %s above %s", k, pluralise(k, "branch", "branches"), res.Branch))
	res.Notices = append(res.Notices, rr.Notices...)
	res.Restacked = movedNames(rr)
	left := make([]string, 0, len(children))
	for _, name := range children {
		if !slices.Contains(res.Restacked, name) {
			left = append(left, name)
		}
	}
	if se, ok := errors.AsType[*stack.Error](err); ok && se.Kind != stack.KindConflict && len(left) > 0 {
		// The absorb is done; say what is left, since re-running absorb
		// would not restack anything.
		isAre := pluralise(len(left), "was", "were")
		steps := make([]string, len(se.NextSteps))
		for i, step := range se.NextSteps {
			steps[i] = strings.ReplaceAll(step, "then try again", "then "+restackCommand)
		}
		err = &stack.Error{
			Kind:      se.Kind,
			Msg:       fmt.Sprintf("absorbed, but %s above %s %s not restacked: %s", joinNames(left), res.Branch, isAre, se.Msg),
			NextSteps: steps,
			Files:     se.Files,
			Branch:    se.Branch,
			Detail:    se.Detail,
			Cause:     err,
		}
	}
	return res, err
}
