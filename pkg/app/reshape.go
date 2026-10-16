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

// reshapeSnapshot is the shape of the stacks a reorder or move changed, as
// it was before, so abort (or a failure before anything moved) can put the
// order, membership, bases and remote ids back.
type reshapeSnapshot struct {
	Stacks []snapshotStack `json:"stacks"`
	// Created names the bottom branch of each stack the reshape added;
	// restoring empties them, which removes them.
	Created []string `json:"created,omitempty"`
	// Dropped names the branches the reshape took out of their stack;
	// restoring puts back each one that still exists.
	Dropped []string `json:"dropped,omitempty"`
	// Closes are the pull requests the reshape queued to close; restoring
	// forgets them again.
	Closes []int `json:"closes,omitempty"`
}

type snapshotStack struct {
	Trunk string `json:"trunk"`
	// Worktree is the working tree whose metadata held the stack.
	Worktree string           `json:"worktree,omitempty"`
	ID       string           `json:"id,omitempty"`
	Number   int              `json:"number,omitempty"`
	Branches []snapshotBranch `json:"branches"`
}

type snapshotBranch struct {
	Name string `json:"name"`
	Base string `json:"base,omitempty"`
}

// snapshotOf records every stack holding one of names, once each.
func snapshotOf(g *stack.Graph, names []string) *reshapeSnapshot {
	snap := &reshapeSnapshot{}
	seen := map[int]bool{}
	for _, n := range names {
		s, _, ok := g.StackOf(n)
		if !ok {
			continue
		}
		i := stackIndex(g, s)
		if seen[i] {
			continue
		}
		seen[i] = true
		ss := snapshotStack{Trunk: s.Trunk, Worktree: s.Worktree, ID: s.ID, Number: s.Number}
		for _, b := range s.Branches {
			ss.Branches = append(ss.Branches, snapshotBranch{Name: b.Name, Base: b.Base})
		}
		snap.Stacks = append(snap.Stacks, ss)
	}
	return snap
}

// stackIndex is s's position in g.Stacks (s must point into it).
func stackIndex(g *stack.Graph, s *stack.Stack) int {
	for i := range g.Stacks {
		if &g.Stacks[i] == s {
			return i
		}
	}
	return -1
}

// restoreReshape puts the snapshot back: every branch entry of the stacks
// involved keeps the head it has now, goes back to the stack and position
// it had, and gets its old base; each stack gets its trunk, worktree and
// remote id back, and the pending dissolve of those stacks is forgotten.
// Stacks the reshape created are emptied, which removes them; any other
// branch the snapshot does not name stays on the stack it is on now.
func (a *App) restoreReshape(ctx context.Context, repo git.Repo, snap *reshapeSnapshot) error {
	dropped := map[string]stack.Branch{}
	for _, name := range snap.Dropped {
		tip, ok, err := a.d.Git.Tip(ctx, repo, "refs/heads/"+name)
		if err != nil {
			return err
		}
		if ok {
			dropped[name] = stack.Branch{Name: name, Head: tip}
		}
	}
	var ids []string
	err := a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
		ids = nil
		// The slots involved, in index order: every stack holding a branch
		// the snapshot names, and every stack the reshape created.
		named := map[string]bool{}
		var involved []int
		for _, ss := range snap.Stacks {
			for _, sb := range ss.Branches {
				named[sb.Name] = true
				if s, _, ok := g.StackOf(sb.Name); ok {
					involved = append(involved, stackIndex(g, s))
				}
			}
		}
		created := map[int]bool{}
		for _, bottom := range snap.Created {
			if s, _, ok := g.StackOf(bottom); ok {
				i := stackIndex(g, s)
				created[i] = true
				involved = append(involved, i)
			}
		}
		slices.Sort(involved)
		involved = slices.Compact(involved)
		pool := map[string]stack.Branch{}
		leftovers := map[int][]stack.Branch{}
		bottoms := map[int]string{}
		for _, i := range involved {
			bottoms[i] = g.Stacks[i].Bottom()
			for _, b := range g.Stacks[i].Branches {
				switch {
				case named[b.Name]:
					pool[b.Name] = b
				case !created[i]:
					leftovers[i] = append(leftovers[i], b)
				}
			}
			g.Stacks[i].Branches = nil
		}
		for name, b := range dropped {
			if _, _, tracked := g.StackOf(name); !tracked {
				pool[name] = b
			}
		}
		// Each stack goes home to the first free slot, in index order, that
		// still carries its remote id; else that has its bottom branch at
		// the bottom on the same trunk; else that is on the same trunk;
		// else to a new stack. Exact matches are settled for every stack
		// before any falls back, so a moved bottom branch can't pull its
		// stack into the record of the stack it was moved onto. A created
		// slot is never a home.
		homes := make([]int, len(snap.Stacks))
		taken := map[int]bool{}
		claim := func(k int, match func(i int) bool) {
			for _, i := range involved {
				if !taken[i] && !created[i] && match(i) {
					homes[k], taken[i] = i, true
					return
				}
			}
		}
		for k := range homes {
			homes[k] = -1
		}
		for k, ss := range snap.Stacks {
			if ss.ID != "" && len(ss.Branches) > 0 {
				claim(k, func(i int) bool { return g.Stacks[i].ID == ss.ID })
			}
		}
		for k, ss := range snap.Stacks {
			if homes[k] < 0 && len(ss.Branches) > 0 {
				claim(k, func(i int) bool { return bottoms[i] == ss.Branches[0].Name && g.Stacks[i].Trunk == ss.Trunk })
			}
		}
		for k, ss := range snap.Stacks {
			if homes[k] >= 0 {
				continue
			}
			claim(k, func(i int) bool { return g.Stacks[i].Trunk == ss.Trunk })
			if homes[k] < 0 && slices.ContainsFunc(ss.Branches, func(sb snapshotBranch) bool { _, ok := pool[sb.Name]; return ok }) {
				g.Stacks = append(g.Stacks, stack.Stack{})
				homes[k] = len(g.Stacks) - 1
			}
		}
		for k, ss := range snap.Stacks {
			if homes[k] < 0 {
				continue // every branch of it is gone; nothing to put back
			}
			s := &g.Stacks[homes[k]]
			s.Trunk, s.Worktree, s.ID, s.Number = ss.Trunk, ss.Worktree, ss.ID, ss.Number
			if ss.ID != "" {
				ids = append(ids, ss.ID)
			}
			for _, sb := range ss.Branches {
				b, ok := pool[sb.Name]
				if !ok {
					continue // deleted since; nothing to put back
				}
				b.Base = sb.Base
				s.Branches = append(s.Branches, b)
			}
		}
		for _, i := range involved {
			g.Stacks[i].Branches = append(g.Stacks[i].Branches, leftovers[i]...)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := dropPendingClose(repo, snap.Closes); err != nil {
		return err
	}
	return dropPendingRemote(repo, ids)
}

// reshapeEdit is one reorder, move or init: what to change and what to
// restack afterwards.
type reshapeEdit struct {
	// touched names one branch in every existing stack the edit changes.
	touched []string
	// edit applies the new order and membership to the graph.
	edit func(g *stack.Graph) error
	// segments are the restacks to run afterwards, in order, each a run of
	// branches (bottom first) onto a parent.
	segments []restackSegment
	// created names the bottom branch of each stack edit appends.
	created []string
	// relink names one branch in each stack whose pull request order or
	// membership changed; its remote id is cleared and the stack queued
	// for dissolving on the next submit.
	relink []string
	// dropped names the branches edit takes out of their stack, and closes
	// their pull requests to close at the next submit.
	dropped []string
	closes  []pendingClose
	// finish runs once the segments are restacked, here or at the end of
	// git stack continue.
	finish *reshapeFinish
}

// reshape runs an edit: snapshot, metadata update with bases fixed, remote
// ids queued, then the segments through the restack engine. command names
// the CLI command for messages and the saved state; headline is the
// progress line. A conflict comes back as the usual conflict error with
// the snapshot saved for abort. Any other failure before a ref moved rolls
// the metadata back and says so.
func (a *App) reshape(ctx context.Context, repo git.Repo, command, headline string, e reshapeEdit) (RestackResult, error) {
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return RestackResult{}, err
	}
	snap := snapshotOf(graph, e.touched)
	snap.Created, snap.Dropped = e.created, e.dropped
	for _, c := range e.closes {
		snap.Closes = append(snap.Closes, c.Number)
	}
	local, err := a.localBranches(ctx, repo)
	if err != nil {
		return RestackResult{}, err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil && !errors.Is(err, git.ErrDetached) {
		return RestackResult{}, err
	}
	var pending []remoteStack
	queued, closing := false, false
	err = a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
		before := parentsOf(g)
		// The stacks to relink, and the names GitHub knows them by, are
		// found before the edit: a move can empty its source stack or carry
		// its bottom branch elsewhere, and the edit only appends stacks and
		// edits branches, so indexes hold.
		var relink []int
		pending, queued, closing = nil, false, false
		for _, name := range e.relink {
			if s, _, ok := g.StackOf(name); ok && s.ID != "" {
				if i := stackIndex(g, s); !slices.Contains(relink, i) {
					relink = append(relink, i)
					pending = append(pending, remoteStack{ID: s.ID, Number: s.Number, Stack: s.Bottom()})
				}
			}
		}
		count := len(g.Stacks)
		if err := e.edit(g); err != nil {
			return err
		}
		if len(g.Stacks) < count {
			return errors.New("the edit removed a stack")
		}
		for i := range g.Stacks {
			s := &g.Stacks[i]
			parent := s.Trunk
			for j := range s.Branches {
				b := &s.Branches[j]
				old, had := before[b.Name]
				if !had {
					old = parent // adopted: its own commits start at the merge base with its new parent
				}
				if !had || old != parent {
					if err := a.fixBase(ctx, repo, b, local, old); err != nil {
						return err
					}
				}
				parent = b.Name
			}
		}
		for _, i := range relink {
			g.Stacks[i].ID, g.Stacks[i].Number = "", 0
		}
		if len(e.closes) > 0 {
			if err := addPendingClose(repo, e.closes); err != nil {
				return err
			}
			closing = true
		}
		// Queue the dissolves before the cleared ids are written, so a
		// failed write never leaves an id gone with nothing queued.
		if len(pending) > 0 {
			if err := addPendingRemote(repo, pending); err != nil {
				return err
			}
			queued = true
		}
		return nil
	})
	if err != nil {
		if closing {
			if derr := dropPendingClose(repo, snap.Closes); derr != nil {
				err = errors.Join(err, derr)
			}
		}
		if queued {
			// The ids were not cleared after all; forget the dissolves again.
			ids := make([]string, len(pending))
			for i, p := range pending {
				ids[i] = p.ID
			}
			if derr := dropPendingRemote(repo, ids); derr != nil {
				return RestackResult{}, errors.Join(err, derr)
			}
		}
		return RestackResult{}, err
	}
	run := &restackRun{repo: repo, command: command, originalBranch: current, reshape: snap, finish: e.finish}
	res := RestackResult{Scope: stack.ScopeUpstack}
	if len(e.segments) == 0 {
		if err := a.finishReshape(ctx, run, &res); err != nil {
			return res, a.saveStopped(run, err)
		}
		return res, nil
	}
	restacked := false
	err = a.progress(ctx, PhaseRestack, headline, func(ctx context.Context) error {
		if err := a.restackSegments(ctx, run, e.segments, &res); err != nil {
			return err
		}
		restacked = true
		return a.finishReshape(ctx, run, &res)
	})
	if err == nil {
		return res, nil
	}
	if restacked {
		// Only the finish is left; continue does it again.
		return res, a.saveStopped(run, err)
	}
	// A saved state means the engine stopped somewhere continue and abort
	// can pick up (a conflict, usually); leave it to them. Without one,
	// put the shape back if no ref moved, else save what was done so the
	// reshape can still be finished or undone as one operation.
	if st, lerr := loadRestackState(repo); lerr != nil || st != nil {
		return res, err
	}
	if len(run.moved) == 0 && !a.refsMoved(ctx, repo, local) {
		if rerr := a.restoreReshape(ctx, repo, snap); rerr != nil {
			return res, asStackError(err).WithSteps(fmt.Sprintf("the stack's new shape was kept because putting it back failed (%v); git stack restack finishes it", rerr))
		}
		return res, asStackError(err).WithSteps("nothing was changed")
	}
	return res, a.saveStopped(run, err)
}

// finishReshape runs the reshape's finish step, if it has one, once every
// segment is restacked and the user is back on the branch they started
// from.
func (a *App) finishReshape(ctx context.Context, run *restackRun, res *RestackResult) error {
	if run.finish == nil {
		return nil
	}
	if run.finish.Pop != "" {
		if err := a.finishPop(ctx, run.repo, run.finish); err != nil {
			// HEAD may already be on the parent with the changes staged; a
			// retry must not switch back to the popped branch.
			if cur, _ := a.d.Git.CurrentBranch(ctx, run.repo); cur == run.finish.Parent {
				run.originalBranch = cur
			}
			return err
		}
		res.Popped = run.finish.Pop
	}
	return nil
}

// saveStopped saves a reshape that stopped without a conflict after some
// refs moved: what moved, the segments still to do (the one that failed
// first) and the snapshot, so git stack continue can finish it once the
// problem is fixed and git stack abort can put everything back.
func (a *App) saveStopped(run *restackRun, err error) error {
	e := asStackError(err)
	st := &restackState{
		Command: run.command, OriginalBranch: run.originalBranch, Trunk: run.trunk,
		Moved: run.moved, Then: run.then, Reshape: run.reshape, Finish: run.finish,
	}
	if serr := saveRestackState(run.repo, st); serr != nil {
		return e.WithSteps(fmt.Sprintf("the new shape was kept but could not be saved for git stack continue or abort (%v); git stack restack finishes it", serr))
	}
	e.NextSteps = resumeSteps(run.command)
	return e
}

// resumeSteps is the way on from a reshape that stopped with its state
// saved: fix what stopped it and continue, or abort.
func resumeSteps(command string) []string {
	verb := strings.TrimPrefix(command, "git stack ")
	return []string{"fix that, then git stack continue finishes the " + verb, "or git stack abort puts everything back"}
}

// refsMoved reports whether any branch's head differs from before, or
// cannot be read: a failed restack can have moved the checked out branch
// before the transaction that failed.
func (a *App) refsMoved(ctx context.Context, repo git.Repo, before map[string]git.Branch) bool {
	now, err := a.localBranches(ctx, repo)
	if err != nil {
		return true
	}
	for name, b := range before {
		if now[name].Head != b.Head {
			return true
		}
	}
	return false
}

// parentsOf maps every stacked branch to its parent (trunk for a bottom
// branch).
func parentsOf(g *stack.Graph) map[string]string {
	out := map[string]string{}
	for _, s := range g.Stacks {
		parent := s.Trunk
		for _, b := range s.Branches {
			out[b.Name] = parent
			parent = b.Name
		}
	}
	return out
}

// fixBase makes sure b's recorded base marks where its own commits start
// now that its parent changed: the recorded base when it is still in the
// branch's history, else the merge base with the old parent's tip. The
// planner replays base..tip, so this is what keeps the old parent's
// commits out of the branch.
func (a *App) fixBase(ctx context.Context, repo git.Repo, b *stack.Branch, local map[string]git.Branch, oldParent string) error {
	lb, ok := local[b.Name]
	if !ok {
		return stack.Newf(stack.KindInvalidArgs, "%s no longer exists", b.Name)
	}
	if b.Base != "" && a.isAncestor(ctx, repo, b.Base, lb.Head) {
		return nil
	}
	op, ok := local[oldParent]
	if !ok {
		return stack.Newf(stack.KindInvalidArgs, "%s, the branch below %s, no longer exists", oldParent, b.Name).
			WithSteps("run git stack sync first")
	}
	mb, err := a.d.Git.MergeBase(ctx, repo, lb.Head, op.Head)
	if err != nil {
		return fmt.Errorf("%s: %w", b.Name, err)
	}
	b.Base = mb
	return nil
}
