package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/git"
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
	Notices  []string `json:"notices,omitempty"`
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
	state                                     *restackState //nolint:unused // set once a conflict is saved for continue and abort
}

// restack is the shared body of Restack and modify's restack. command
// names the CLI command for messages; headline overrides the progress line.
func (a *App) restack(ctx context.Context, repo git.Repo, o RestackOptions, command, headline string) (RestackResult, error) {
	if err := a.noRebaseActive(ctx, repo, command); err != nil {
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
// parentName. A conflict ends the run at that branch with an error.
func (a *App) restackBranches(ctx context.Context, run *restackRun, branches []stack.Branch, parentName string, res *RestackResult) error {
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
	})
	if p.err != nil {
		return p.err
	}
	if err := a.checkCurrentMove(ctx, run.repo, &p); err != nil {
		return err
	}
	if err := a.applyMoves(ctx, run, &p, res); err != nil {
		return err
	}
	if p.conflict != nil {
		return conflictError(p.conflict.Branch, p.conflict.Onto, p.conflict.Files)
	}
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
	for _, m := range p.moves {
		heads[m.name] = m.to
		switch {
		case m.worktree == run.repo.TopLevel:
			if err := a.d.Git.ResetKeep(ctx, run.repo, m.to); err != nil {
				return stack.Newf(stack.KindInvalidArgs, "%s is checked out here with local changes in the way of its restack", m.name).
					WithSteps("commit or stash them, then try again").WithCause(err)
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
		run.moved = append(run.moved, movedRef{Name: m.name, From: m.from, To: m.to, OldBase: m.oldBase, Worktree: m.worktree})
		res.Moved = append(res.Moved, BranchMove{Name: m.name, Stack: run.stackName, From: m.from, To: m.to})
		if m.empty {
			res.Notices = append(res.Notices, fmt.Sprintf("%s has no commits of its own any more; its changes are already in its parent", m.name))
		}
	}
	return a.d.Meta.Update(ctx, run.repo, func(g *stack.Graph) error {
		for i := range g.Stacks {
			for j := range g.Stacks[i].Branches {
				b := &g.Stacks[i].Branches[j]
				if to, ok := heads[b.Name]; ok {
					b.Head = to
				}
				if base, ok := p.bases[b.Name]; ok {
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
func (a *App) noRebaseActive(ctx context.Context, repo git.Repo, command string) error { //nolint:unparam // command names the caller in the messages the resumable restack adds
	st, err := loadRestackState(repo)
	if err != nil {
		return err
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

// restackContinue resumes an interrupted restack.
func (a *App) restackContinue(ctx context.Context, repo git.Repo, stageAll bool) (RestackResult, error) {
	st, err := loadRestackState(repo)
	if err != nil {
		return RestackResult{}, err
	}
	if st == nil {
		return RestackResult{}, a.nothingToResume(ctx, repo, "continue")
	}
	_ = stageAll
	return RestackResult{}, stack.New(stack.KindUnsupported, "continue is not implemented yet")
}

// restackAbort gives up an interrupted restack.
func (a *App) restackAbort(ctx context.Context, repo git.Repo) (RestackResult, error) {
	st, err := loadRestackState(repo)
	if err != nil {
		return RestackResult{}, err
	}
	if st == nil {
		return RestackResult{}, a.nothingToResume(ctx, repo, "abort")
	}
	return RestackResult{}, stack.New(stack.KindUnsupported, "abort is not implemented yet")
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
		steps = append(steps, "git add "+strings.Join(files, " "))
	} else {
		steps = append(steps, "git add <files>")
	}
	return e.WithSteps(append(steps, "git stack continue", "or give up with git stack abort")...)
}
