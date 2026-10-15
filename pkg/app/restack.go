package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/shell"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// RestackOptions mirrors `gt restack`.
type RestackOptions struct {
	Scope stack.Scope
	// Branch is where the scope is worked out from; the current branch when
	// empty. Nothing is checked out.
	Branch string
	// Continue resumes an interrupted restack after the conflicts were
	// staged; Abort gives it up and puts every moved branch back.
	Continue bool
	Abort    bool
	// StageAll runs git add -A before continuing.
	StageAll bool
}

// RestackResult reports what a restack did.
type RestackResult struct {
	Scope stack.Scope `json:"scope"`
	// Branches is the scope, bottom to top.
	Branches []string `json:"branches"`
	// Moved are the branches that were rebased, bottom to top.
	Moved []BranchMove `json:"moved,omitempty"`
	// InPlace were already on their parent.
	InPlace []string `json:"in_place,omitempty"`
	// Restored are the branches an abort put back.
	Restored []string `json:"restored,omitempty"`
	// OrderRestored is true when an abort also put a reorder or move's
	// stack shape back.
	OrderRestored bool     `json:"order_restored,omitempty"`
	Notices       []string `json:"notices,omitempty"`
}

const restackCommand = "git stack restack"

// Restack rebases part of the current stack onto its parents, locally and
// without checking anything out, the way gt restack does. The bottom
// branch goes onto the local trunk. A conflict stops at that branch.
func (a *App) Restack(ctx context.Context, repo git.Repo, o RestackOptions) (RestackResult, error) {
	switch {
	case o.Continue:
		return a.restackContinue(ctx, repo, o.StageAll)
	case o.Abort:
		return a.restackAbort(ctx, repo)
	}
	return a.restack(ctx, repo, o, restackCommand, "")
}

// restackRun is one restack operation in flight; on a conflict it is what
// gets saved so continue and abort can pick it up.
type restackRun struct {
	repo                                      git.Repo
	command, trunk, stackName, originalBranch string
	moved                                     []movedRef
	state                                     *restackState
	// then are the segments queued behind the one in progress, and
	// reshape the snapshot abort puts back; both are saved on a conflict.
	then    []restackSegment
	reshape *reshapeSnapshot
}

// restack is the shared body of Restack and modify's restack. command
// names the CLI command for messages; headline overrides the progress line.
func (a *App) restack(ctx context.Context, repo git.Repo, o RestackOptions, command, headline string) (RestackResult, error) {
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return RestackResult{}, err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil && !errors.Is(err, git.ErrDetached) {
		return RestackResult{}, err
	}
	start := o.Branch
	if start == "" {
		if current == "" {
			return RestackResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch first").
				WithSteps("or name one: " + command + " --branch <name>")
		}
		start = current
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return RestackResult{}, err
	}
	s, i, ok := graph.StackOf(start)
	if !ok {
		if graph.IsTrunk(start) {
			return RestackResult{}, stack.Newf(stack.KindNotInStack, "%s is a trunk; check out a stacked branch to restack it", start)
		}
		return RestackResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", start)
	}
	lo, hi := 0, len(s.Branches)
	switch o.Scope {
	case stack.ScopeUpstack:
		lo = i
	case stack.ScopeDownstack:
		hi = i + 1
	case stack.ScopeOnly:
		lo, hi = i, i+1
	}
	parent := s.Trunk
	if lo > 0 {
		parent = s.Branches[lo-1].Name
	}
	res := RestackResult{Scope: o.Scope, Branches: s.Names()[lo:hi]}
	if headline == "" {
		headline = "Restacking " + joinNames(res.Branches)
	}
	run := &restackRun{repo: repo, command: command, trunk: s.Trunk, stackName: s.Bottom(), originalBranch: current}
	err = a.progress(ctx, PhaseRestack, headline, func(ctx context.Context) error {
		return a.restackBranches(ctx, run, slices.Clone(s.Branches[lo:hi]), parent, &res)
	})
	return res, err
}

// restackBranches plans and applies the moves for branches on top of
// parentName. A conflict starts a real git rebase of that branch and ends
// the run with a conflict error; if that rebase happens to go through, the
// run carries on above it.
func (a *App) restackBranches(ctx context.Context, run *restackRun, branches []stack.Branch, parentName string, res *RestackResult) error {
	for {
		local, err := a.localBranches(ctx, run.repo)
		if err != nil {
			return err
		}
		parentTip, ok := local[parentName]
		if !ok {
			return stack.Newf(stack.KindInvalidArgs, "%s no longer exists, so nothing above it can be restacked", parentName)
		}
		p := a.planMoves(ctx, planInput{
			repo: run.repo, stackName: run.stackName, branches: branches,
			parentName: parentName, parentTip: parentTip.Head,
			local: local, dirty: newDirtyCache(), currentWorktree: run.repo.TopLevel,
			strict: run.reshape != nil, command: run.command,
		})
		if p.err != nil {
			return p.err
		}
		if err := a.checkCurrentMove(ctx, run.repo, &p); err != nil {
			return err
		}
		if p.conflict != nil {
			if err := a.checkConflictStart(ctx, run, &p, local); err != nil {
				return err
			}
		}
		if err := a.applyMoves(ctx, run, &p, res); err != nil {
			return err
		}
		if p.conflict == nil {
			return a.finishRun(ctx, run)
		}
		idx := slices.IndexFunc(branches, func(b stack.Branch) bool { return b.Name == p.conflict.Branch })
		branches = branches[idx+1:]
		remaining := make([]string, len(branches))
		for i, b := range branches {
			remaining[i] = b.Name
		}
		if err := a.startConflictRebase(ctx, run, &p, remaining, local); err != nil {
			return err
		}
		// git finished the rebase by itself (rerere, say): record it and go on.
		if err := a.recordRebased(ctx, run, res); err != nil {
			return err
		}
		parentName = p.conflict.Branch
		if len(branches) == 0 {
			return a.finishRun(ctx, run)
		}
	}
}

// checkConflictStart refuses to begin the conflicting branch's rebase when
// it could not be resolved here: the branch is checked out in another
// worktree, or this working tree has changes git rebase would reject.
func (a *App) checkConflictStart(ctx context.Context, run *restackRun, p *restackPlan, local map[string]git.Branch) error {
	c := p.conflict
	if wt := local[c.Branch].Worktree; wt != "" && wt != run.repo.TopLevel && run.reshape != nil {
		// A reorder or move can't hand the conflict to the other worktree:
		// a restack there would not know about the reshape.
		return stack.Newf(stack.KindInvalidArgs, "%s conflicts when rebased onto %s, and it is checked out in %s, so the conflict can't be resolved here", c.Branch, c.Onto, shortPath(wt)).
			WithSteps("check out another branch there, then run " + run.command + " again")
	}
	if wt := local[c.Branch].Worktree; wt != "" && wt != run.repo.TopLevel {
		e := conflictError(c.Branch, c.Onto, c.Files)
		e.Msg += ", and it is checked out in " + shortPath(wt)
		e.NextSteps = []string{"resolve it there: cd " + wt + " && " + restackCommand}
		return e
	}
	changes, err := a.d.Git.LocalChanges(ctx, run.repo)
	if err != nil {
		return err
	}
	if len(changes) > 0 {
		return stack.Newf(stack.KindInvalidArgs, "resolving the conflict on %s needs a clean working tree", c.Branch).
			WithSteps("commit or stash your changes, then try again")
	}
	files, err := a.untrackedInTheWay(ctx, run.repo, run.repo.TopLevel, "HEAD", c.Branch)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		return stack.Newf(stack.KindInvalidArgs, "resolving the conflict on %s would overwrite untracked %s", c.Branch, joinNames(files)).
			WithSteps("move them aside, then try again")
	}
	return nil
}

// startConflictRebase saves the run and hands the conflicting branch to
// git rebase --onto. It returns the conflict error when git stops, nil when
// the rebase unexpectedly completes.
func (a *App) startConflictRebase(ctx context.Context, run *restackRun, p *restackPlan, remaining []string, local map[string]git.Branch) error {
	c := p.conflict
	var oldBase string
	if g, err := a.d.Meta.Load(ctx, run.repo); err == nil {
		if s, i, ok := g.StackOf(c.Branch); ok {
			oldBase = s.Branches[i].Base
		}
	}
	run.state = &restackState{
		Command: run.command, OriginalBranch: run.originalBranch, Trunk: run.trunk,
		Conflict: c.Branch, Onto: c.Onto, NewBase: p.conflictNewBase,
		ConflictTip: local[c.Branch].Head, ConflictOldBase: oldBase,
		Remaining: remaining, Moved: run.moved,
		Then: run.then, Reshape: run.reshape,
	}
	if err := saveRestackState(run.repo, run.state); err != nil {
		return err
	}
	stopped, err := a.d.Git.RebaseOnto(ctx, run.repo, p.conflictNewBase, p.conflictFrom, c.Branch)
	if err != nil {
		return rebaseError(err) // the state stays so --abort can still put the moved branches back
	}
	if stopped {
		files, ferr := a.d.Git.ConflictedFiles(ctx, run.repo)
		if ferr != nil {
			files = c.Files
		}
		return conflictError(c.Branch, c.Onto, files)
	}
	return nil
}

// recordRebased writes the conflicting branch's new tip and base once its
// git rebase is done, and remembers the move for abort. A continue retried
// after a later failure calls it again; the move is remembered once.
func (a *App) recordRebased(ctx context.Context, run *restackRun, res *RestackResult) error {
	st := run.state
	tip, err := a.d.Git.RevParse(ctx, run.repo, st.Conflict)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(run.moved, func(m movedRef) bool { return m.Name == st.Conflict }) {
		m := movedRef{Name: st.Conflict, From: st.ConflictTip, To: tip, OldBase: st.ConflictOldBase, Stack: run.stackName}
		run.moved = append(run.moved, m)
		res.Moved = append(res.Moved, BranchMove{Name: m.Name, Stack: run.stackName, From: m.From, To: m.To})
	}
	st.Moved = run.moved
	err = a.d.Meta.Update(ctx, run.repo, func(g *stack.Graph) error {
		for i := range g.Stacks {
			for j := range g.Stacks[i].Branches {
				if b := &g.Stacks[i].Branches[j]; b.Name == st.Conflict {
					b.Head, b.Base = tip, st.NewBase
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Save the move so abort can still put it back if the run fails later.
	return saveRestackState(run.repo, st)
}

// finishRun puts the user back on the branch they started from when a
// git rebase moved them, and forgets the saved state.
func (a *App) finishRun(ctx context.Context, run *restackRun) error {
	if run.state == nil {
		return nil
	}
	if run.originalBranch != "" {
		cur, _ := a.d.Git.CurrentBranch(ctx, run.repo)
		if exists, _ := a.d.Git.BranchExists(ctx, run.repo, run.originalBranch); exists && cur != run.originalBranch {
			if err := a.d.Git.Switch(ctx, run.repo, run.originalBranch); err != nil {
				return err
			}
		}
	}
	if err := clearRestackState(run.repo); err != nil {
		return err
	}
	run.state = nil
	return nil
}

// checkCurrentMove refuses the run before anything moves when the branch
// checked out here has local changes to files its move would rewrite:
// reset --keep would refuse, but only after the branches below had moved.
func (a *App) checkCurrentMove(ctx context.Context, repo git.Repo, p *restackPlan) error {
	for _, m := range p.moves {
		if m.worktree != repo.TopLevel {
			continue
		}
		local, err := a.d.Git.LocalChanges(ctx, repo)
		if err != nil || len(local) == 0 {
			return err
		}
		changed, err := a.d.Git.ChangedPaths(ctx, repo, m.from, m.to)
		if err != nil {
			return err
		}
		var clash []string
		for _, f := range local {
			if slices.Contains(changed, f) {
				clash = append(clash, f)
			}
		}
		if len(clash) > 0 {
			return stack.Newf(stack.KindInvalidArgs, "%s has local changes to %s that restacking it would overwrite", m.name, joinNames(clash)).
				WithSteps("commit or stash them, then try again")
		}
	}
	return nil
}

// applyMoves moves the planned refs: the branch checked out here with
// reset --keep first (the one step that can refuse), then every other ref
// in one transaction with expected old values, then clean checkouts in
// other worktrees, then the metadata's heads and bases.
func (a *App) applyMoves(ctx context.Context, run *restackRun, p *restackPlan, res *RestackResult) error {
	res.Notices = append(res.Notices, p.notices...)
	res.InPlace = append(res.InPlace, p.inPlace...)
	var updates []git.RefUpdate
	var others []plannedMove
	heads := map[string]string{}
	recorded := map[string]bool{}
	for _, m := range p.moves {
		heads[m.name] = m.to
		switch {
		case m.worktree == run.repo.TopLevel:
			if err := a.d.Git.ResetKeep(ctx, run.repo, m.to); err != nil {
				return stack.Newf(stack.KindInvalidArgs, "%s is checked out here with local changes in the way of its restack", m.name).
					WithSteps("commit or stash them, then try again").WithCause(err)
			}
			// This branch has moved already. Remember it now, so abort can
			// put it back if the transaction below fails.
			run.moved = append(run.moved, movedRef{Name: m.name, From: m.from, To: m.to, OldBase: m.oldBase, Worktree: m.worktree, Stack: run.stackName})
			recorded[m.name] = true
			if run.state != nil {
				run.state.Moved = run.moved
				if err := saveRestackState(run.repo, run.state); err != nil {
					return err
				}
			}
		case m.worktree != "":
			updates = append(updates, git.RefUpdate{Ref: "refs/heads/" + m.name, New: m.to, Old: m.from})
			others = append(others, m)
		default:
			updates = append(updates, git.RefUpdate{Ref: "refs/heads/" + m.name, New: m.to, Old: m.from})
		}
	}
	if len(updates) > 0 {
		if err := a.d.Git.UpdateRefs(ctx, run.repo, updates); err != nil {
			return stack.New(stack.KindInvalidArgs, "a branch moved while it was being restacked, so its stack was left alone").
				WithSteps("run " + run.command + " again").WithCause(err)
		}
	}
	for _, m := range others {
		if err := a.d.Git.ResetHard(ctx, worktreeRepo(run.repo, m.worktree), m.to); err != nil {
			res.Notices = append(res.Notices, fmt.Sprintf("%s was restacked but its checkout in %s could not be updated: %v; run git reset --hard there", m.name, shortPath(m.worktree), err))
		}
	}
	for _, m := range p.moves {
		if !recorded[m.name] {
			run.moved = append(run.moved, movedRef{Name: m.name, From: m.from, To: m.to, OldBase: m.oldBase, Worktree: m.worktree, Stack: run.stackName})
		}
		res.Moved = append(res.Moved, BranchMove{Name: m.name, Stack: run.stackName, From: m.from, To: m.to})
		if m.empty {
			res.Notices = append(res.Notices, fmt.Sprintf("%s has no commits of its own any more; its changes are already in its parent", m.name))
		}
	}
	if run.state != nil {
		// On the continue path a run is already saved; the refs have moved,
		// so record them before anything else can fail and abort can put
		// them back too.
		run.state.Moved = run.moved
		if err := saveRestackState(run.repo, run.state); err != nil {
			return err
		}
	}
	return a.updateHeadsAndBases(ctx, run.repo, heads, p.bases)
}

// updateHeadsAndBases records the new head of every branch in heads and
// the new base of every branch in bases.
func (a *App) updateHeadsAndBases(ctx context.Context, repo git.Repo, heads, bases map[string]string) error {
	return a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
		for i := range g.Stacks {
			for j := range g.Stacks[i].Branches {
				b := &g.Stacks[i].Branches[j]
				if to, ok := heads[b.Name]; ok {
					b.Head = to
				}
				if base, ok := bases[b.Name]; ok {
					b.Base = base
				}
			}
		}
		return nil
	})
}

// localBranches indexes every local branch by name.
func (a *App) localBranches(ctx context.Context, repo git.Repo) (map[string]git.Branch, error) {
	list, err := a.d.Git.Branches(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make(map[string]git.Branch, len(list))
	for _, b := range list {
		out[b.Name] = b
	}
	return out, nil
}

// noRebaseActive refuses to start when a restack is waiting on conflicts
// or any other rebase is in progress, before anything reads HEAD.
func (a *App) noRebaseActive(ctx context.Context, repo git.Repo) error {
	st, err := loadRestackState(repo)
	if err != nil {
		return err
	}
	if st != nil && st.Conflict == "" {
		return stack.Newf(stack.KindRebaseActive, "a %s was interrupted before it finished", st.Command).
			WithSteps("fix what stopped it, then git stack continue", "or give up with git stack abort")
	}
	if st != nil {
		return stack.Newf(stack.KindRebaseActive, "a restack started by %s is waiting on conflicts in %s", st.Command, st.Conflict).
			WithSteps("resolve them, git add the files, then git stack continue", "or give up with git stack abort")
	}
	active, err := a.d.Git.RebaseInProgress(ctx, repo)
	if err != nil || !active {
		return err
	}
	if ghRebaseStateExists(repo) {
		return stack.New(stack.KindRebaseActive, "a gh stack rebase is in progress").
			WithSteps("finish it with gh stack rebase --continue", "or gh stack rebase --abort")
	}
	return stack.New(stack.KindRebaseActive, "a git rebase is in progress").
		WithSteps("finish it with git rebase --continue", "or git rebase --abort")
}

// nothingToResume is the continue/abort answer when there is no saved
// restack; verb is "continue" or "abort".
func (a *App) nothingToResume(ctx context.Context, repo git.Repo, verb string) error {
	active, err := a.d.Git.RebaseInProgress(ctx, repo)
	if err != nil {
		return err
	}
	switch {
	case active && ghRebaseStateExists(repo):
		return stack.Newf(stack.KindInvalidArgs, "no git-stack restack to %s; a gh stack rebase is in progress", verb).
			WithSteps("gh stack rebase --" + verb)
	case active:
		return stack.Newf(stack.KindInvalidArgs, "no git-stack restack to %s; a git rebase git-stack did not start is in progress", verb).
			WithSteps("git rebase --" + verb)
	default:
		return stack.Newf(stack.KindInvalidArgs, "nothing to %s", verb)
	}
}

// restackContinue resumes an interrupted restack: finish the conflicting
// branch's git rebase, record it, then restack what was left above it.
func (a *App) restackContinue(ctx context.Context, repo git.Repo, stageAll bool) (RestackResult, error) {
	st, err := loadRestackState(repo)
	if err != nil {
		return RestackResult{}, err
	}
	if st == nil {
		return RestackResult{}, a.nothingToResume(ctx, repo, "continue")
	}
	res := RestackResult{Branches: st.branches()}
	active, err := a.d.Git.RebaseInProgress(ctx, repo)
	if err != nil {
		return res, err
	}
	if active {
		// With no conflict saved, any rebase in progress is someone else's
		// and this refuses it.
		if err := a.checkRestackRebase(ctx, repo, st); err != nil {
			return res, err
		}
		if stageAll {
			if err := a.d.Git.Add(ctx, repo, git.AddAll); err != nil {
				return res, err
			}
		}
		stopped, err := a.d.Git.RebaseContinue(ctx, repo)
		if err != nil {
			return res, rebaseError(err)
		}
		if stopped {
			files, _ := a.d.Git.ConflictedFiles(ctx, repo)
			return res, conflictError(st.Conflict, st.Onto, files)
		}
	}
	if st.Conflict != "" && !a.isAncestor(ctx, repo, st.NewBase, st.Conflict) {
		// Finished by hand would have put the new base under the branch;
		// this rebase was abandoned instead.
		return res, stack.Newf(stack.KindInvalidArgs, "the rebase of %s was abandoned outside git-stack", st.Conflict).
			WithSteps("git stack abort to put the moved branches back, then " + st.Command + " again")
	}
	earlier := slices.Clone(st.Moved)
	run := &restackRun{repo: repo, command: st.Command, trunk: st.Trunk, originalBranch: st.OriginalBranch, moved: st.Moved, state: st, then: st.Then, reshape: st.Reshape}
	if g, err := a.d.Meta.Load(ctx, repo); err == nil {
		if s, _, ok := g.StackOf(st.Conflict); ok {
			run.stackName = s.Bottom()
		}
	}
	err = a.progress(ctx, PhaseRestack, "Continuing the restack", func(ctx context.Context) error {
		if st.Conflict == "" {
			// A reshape that stopped without a conflict: carry on with the
			// segment that failed. The state stays saved until it finishes.
			if err := a.restackSegments(ctx, run, st.Then, &res); err != nil {
				return err
			}
			// Every queued segment may have been empty, which never reaches
			// finishRun on its own.
			return a.finishRun(ctx, run)
		}
		if err := a.recordRebased(ctx, run, &res); err != nil {
			return err
		}
		if len(st.Remaining) > 0 {
			graph, err := a.d.Meta.Load(ctx, repo)
			if err != nil {
				return err
			}
			if err := a.restackBranches(ctx, run, branchesNamed(graph, st.Remaining), st.Conflict, &res); err != nil {
				return err
			}
		} else if err := a.finishRun(ctx, run); err != nil {
			return err
		}
		return a.restackSegments(ctx, run, st.Then, &res)
	})
	if err == nil {
		res.Moved = withEarlierMoves(earlier, res.Moved, run.stackName)
		return res, nil
	}
	cur, lerr := loadRestackState(repo)
	if lerr != nil {
		return res, err
	}
	if cur != nil {
		// The state stays saved, so a refusal's own "then run it again" no
		// longer applies: the way on is continue, or abort.
		if e, ok := errors.AsType[*stack.Error](err); ok && e.Kind == stack.KindInvalidArgs {
			e.NextSteps = resumeSteps(cur.Command)
		}
		return res, err
	}
	if run.reshape != nil && len(run.moved) > 0 {
		// A later segment can stop after an earlier one finished and cleared
		// the state; save it again so the reshape stays one operation.
		return res, a.saveStopped(run, err)
	}
	return res, err
}

// checkRestackRebase refuses a foreign rebase before either recovery
// command can stage files, advance it or abort it.
func (a *App) checkRestackRebase(ctx context.Context, repo git.Repo, st *restackState) error {
	ours, err := a.d.Git.RebaseMatches(ctx, repo, st.Conflict, st.NewBase, st.ConflictTip)
	if err != nil {
		return err
	}
	if !ours {
		return stack.New(stack.KindInvalidArgs, "a git rebase git-stack did not start is in progress").
			WithSteps("finish it with git rebase --continue or git rebase --abort, then git stack abort")
	}
	return nil
}

// withEarlierMoves puts the moves an operation made before this continue
// in front of the ones it made now, so the result covers the whole
// operation, bottom to top. Each keeps the stack it moved in; stackName
// labels moves saved before moves recorded their stack.
func withEarlierMoves(earlier []movedRef, now []BranchMove, stackName string) []BranchMove {
	out := make([]BranchMove, 0, len(earlier)+len(now))
	for _, m := range earlier {
		if !slices.ContainsFunc(now, func(b BranchMove) bool { return b.Name == m.Name }) {
			name := cmp.Or(m.Stack, stackName)
			out = append(out, BranchMove{Name: m.Name, Stack: name, From: m.From, To: m.To})
		}
	}
	return append(out, now...)
}

// restackSegments restacks each queued segment in turn, bottom first,
// with the rest queued on the run so a conflict saves them for continue.
// Each segment is reported under its own stack.
func (a *App) restackSegments(ctx context.Context, run *restackRun, segs []restackSegment, res *RestackResult) error {
	for i, seg := range segs {
		run.then = segs[i+1:]
		graph, err := a.d.Meta.Load(ctx, run.repo)
		if err != nil {
			return err
		}
		branches := branchesNamed(graph, seg.Branches)
		if len(branches) == 0 {
			continue
		}
		if s, _, ok := graph.StackOf(branches[0].Name); ok {
			run.stackName, run.trunk = s.Bottom(), s.Trunk
		}
		res.Branches = append(res.Branches, seg.Branches...)
		if err := a.restackBranches(ctx, run, branches, seg.Parent, res); err != nil {
			run.then = segs[i:] // what is left if nothing saved the run
			return err
		}
	}
	run.then = nil
	return nil
}

// branchesNamed looks the named branches up in the graph, keeping order
// and dropping any that are no longer tracked.
func branchesNamed(g *stack.Graph, names []string) []stack.Branch {
	out := make([]stack.Branch, 0, len(names))
	for _, n := range names {
		if s, i, ok := g.StackOf(n); ok {
			out = append(out, s.Branches[i])
		}
	}
	return out
}

// restackAbort gives an interrupted restack up: git puts the conflicting
// branch back, and every branch the operation moved is put back too,
// newest first, unless it has moved again since.
func (a *App) restackAbort(ctx context.Context, repo git.Repo) (RestackResult, error) {
	st, err := loadRestackState(repo)
	if err != nil {
		return RestackResult{}, err
	}
	if st == nil {
		return RestackResult{}, a.nothingToResume(ctx, repo, "abort")
	}
	res := RestackResult{Branches: st.branches()}
	err = a.progress(ctx, PhaseRestack, "Aborting the restack", func(ctx context.Context) error {
		if active, err := a.d.Git.RebaseInProgress(ctx, repo); err != nil {
			return err
		} else if active {
			if err := a.checkRestackRebase(ctx, repo, st); err != nil {
				return err
			}
			if err := a.d.Git.RebaseAbort(ctx, repo); err != nil {
				return err
			}
		}
		local, err := a.localBranches(ctx, repo)
		if err != nil {
			return err
		}
		if lb, ok := local[st.Conflict]; ok && lb.Head != st.ConflictTip &&
			!slices.ContainsFunc(st.Moved, func(m movedRef) bool { return m.Name == st.Conflict }) &&
			a.isAncestor(ctx, repo, st.NewBase, lb.Head) {
			// A rebase finished by hand was never recorded by continue.
			// Save its move before undoing it so a retried abort can still
			// restore the metadata after a later failure.
			st.Moved = append(st.Moved, movedRef{
				Name: st.Conflict, From: st.ConflictTip, To: lb.Head,
				OldBase: st.ConflictOldBase, Worktree: lb.Worktree,
			})
			if err := saveRestackState(repo, st); err != nil {
				return err
			}
		}
		dirty := newDirtyCache()
		var updates []git.RefUpdate
		var others []movedRef
		restored := map[string]movedRef{}
		var names []string
		for _, m := range slices.Backward(st.Moved) {
			lb, ok := local[m.Name]
			switch {
			case !ok:
				res.Notices = append(res.Notices, fmt.Sprintf("%s no longer exists and was left alone", m.Name))
				continue
			case lb.Head == m.From:
				// Already back, by hand or by an abort that failed later
				// on: only its metadata is left to put back.
				restored[m.Name] = m
				names = append(names, m.Name)
				continue
			case lb.Head != m.To:
				res.Notices = append(res.Notices, fmt.Sprintf("%s moved since the restack and was left alone", m.Name))
				continue
			}
			switch {
			case lb.Worktree == repo.TopLevel:
				if err := a.d.Git.ResetKeep(ctx, repo, m.From); err != nil {
					return stack.Newf(stack.KindInvalidArgs, "could not put %s back: local changes are in the way", m.Name).
						WithSteps("commit or stash them, then git stack abort").WithCause(err)
				}
			case lb.Worktree != "":
				if dirty.isDirty(ctx, a.d.Git, repo, lb.Worktree) {
					res.Notices = append(res.Notices, fmt.Sprintf("%s is checked out in %s with uncommitted changes and was left where the restack put it", m.Name, shortPath(lb.Worktree)))
					continue
				}
				updates = append(updates, git.RefUpdate{Ref: "refs/heads/" + m.Name, New: m.From, Old: m.To})
				others = append(others, m)
			default:
				updates = append(updates, git.RefUpdate{Ref: "refs/heads/" + m.Name, New: m.From, Old: m.To})
			}
			restored[m.Name] = m
			names = append(names, m.Name)
		}
		if len(updates) > 0 {
			if err := a.d.Git.UpdateRefs(ctx, repo, updates); err != nil {
				return err
			}
		}
		for _, m := range others {
			if err := a.d.Git.ResetHard(ctx, worktreeRepo(repo, local[m.Name].Worktree), m.From); err != nil {
				res.Notices = append(res.Notices, fmt.Sprintf("%s was put back but its checkout could not be updated: %v; run git reset --hard there", m.Name, err))
			}
		}
		res.Restored = names
		if err := a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
			for i := range g.Stacks {
				for j := range g.Stacks[i].Branches {
					b := &g.Stacks[i].Branches[j]
					if m, ok := restored[b.Name]; ok {
						b.Head, b.Base = m.From, m.OldBase
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if st.Reshape != nil {
			if err := a.restoreReshape(ctx, repo, st.Reshape); err != nil {
				return err
			}
			res.OrderRestored = true
		}
		slices.Reverse(res.Restored) // report bottom to top
		run := &restackRun{repo: repo, originalBranch: st.OriginalBranch, state: st}
		return a.finishRun(ctx, run)
	})
	return res, err
}

// conflictError is the error a stopped rebase surfaces: the branch, its
// files and the exact commands to carry on or give up. The commands are
// the same whether restack or modify started the run.
func conflictError(branch, onto string, files []string) *stack.Error {
	e := stack.Newf(stack.KindConflict, "%s conflicts when rebased onto %s", branch, onto)
	e.Branch, e.Files = branch, files
	steps := []string{"resolve the conflicts"}
	if len(files) > 0 {
		steps[0] = "resolve the conflicts in: " + joinNames(files)
		steps = append(steps, gitAddStep(files))
	} else {
		steps = append(steps, "git add <files>")
	}
	return e.WithSteps(append(steps, "git stack continue", "or give up with git stack abort")...)
}

// gitAddStep is the git add for the conflicted files, each quoted for the
// shell; if one has a control character no quoting makes safe to paste, it
// says what to do instead of printing a command.
func gitAddStep(files []string) string {
	args := make([]string, len(files))
	for i, f := range files {
		q, ok := shell.Arg(f)
		if !ok {
			return "git add each of those files once it's resolved"
		}
		args[i] = q
	}
	return "git add " + strings.Join(args, " ")
}
