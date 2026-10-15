package app

import (
	"context"
	"errors"
	"fmt"

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
}

type snapshotStack struct {
	Trunk    string           `json:"trunk"`
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
		ss := snapshotStack{Trunk: s.Trunk, ID: s.ID, Number: s.Number}
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
// it had, and gets its old base; cleared remote ids come back and the
// pending dissolve of those stacks is forgotten; stacks the reshape
// created are emptied.
func (a *App) restoreReshape(ctx context.Context, repo git.Repo, snap *reshapeSnapshot) error {
	var ids []string
	err := a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
		// Where each snapshot stack lives now: the stack holding the
		// first of its branches that is still tracked.
		homes := make([]int, len(snap.Stacks))
		involved := map[int]bool{}
		for k, ss := range snap.Stacks {
			homes[k] = -1
			for _, sb := range ss.Branches {
				if s, _, ok := g.StackOf(sb.Name); ok {
					i := stackIndex(g, s)
					involved[i] = true
					if homes[k] < 0 {
						homes[k] = i
					}
				}
			}
		}
		for _, bottom := range snap.Created {
			if s, _, ok := g.StackOf(bottom); ok {
				involved[stackIndex(g, s)] = true
			}
		}
		pool := map[string]stack.Branch{}
		for i := range involved {
			for _, b := range g.Stacks[i].Branches {
				pool[b.Name] = b
			}
			g.Stacks[i].Branches = nil
		}
		taken := map[int]bool{}
		for k, ss := range snap.Stacks {
			home := homes[k]
			if home < 0 || taken[home] {
				home = -1
				for i := range involved {
					if !taken[i] && g.Stacks[i].Trunk == ss.Trunk {
						home = i
						break
					}
				}
			}
			if home < 0 {
				g.Stacks = append(g.Stacks, stack.Stack{Trunk: ss.Trunk})
				home = len(g.Stacks) - 1
			}
			taken[home] = true
			s := &g.Stacks[home]
			s.ID, s.Number = ss.ID, ss.Number
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
		return nil
	})
	if err != nil {
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
	snap.Created = e.created
	local, err := a.localBranches(ctx, repo)
	if err != nil {
		return RestackResult{}, err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil && !errors.Is(err, git.ErrDetached) {
		return RestackResult{}, err
	}
	var pending []remoteStack
	err = a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
		before := parentsOf(g)
		// The stacks to relink are found before the edit: a move can empty
		// its source stack or carry its bottom branch elsewhere, and the
		// edit only appends stacks and edits branches, so indexes hold.
		var relink []int
		for _, name := range e.relink {
			if s, _, ok := g.StackOf(name); ok && s.ID != "" {
				relink = append(relink, stackIndex(g, s))
			}
		}
		if err := e.edit(g); err != nil {
			return err
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
			s := &g.Stacks[i]
			pending = append(pending, remoteStack{ID: s.ID, Number: s.Number, Stack: s.Bottom()})
			s.ID, s.Number = "", 0
		}
		return nil
	})
	if err != nil {
		return RestackResult{}, err
	}
	if len(pending) > 0 {
		if err := addPendingRemote(repo, pending); err != nil {
			return RestackResult{}, err
		}
	}
	if len(e.segments) == 0 {
		return RestackResult{}, nil
	}
	run := &restackRun{repo: repo, command: command, originalBranch: current, reshape: snap}
	res := RestackResult{Scope: stack.ScopeUpstack}
	err = a.progress(ctx, PhaseRestack, headline, func(ctx context.Context) error {
		return a.restackSegments(ctx, run, e.segments, &res)
	})
	if err != nil && !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) && len(run.moved) == 0 {
		if rerr := a.restoreReshape(ctx, repo, snap); rerr != nil {
			return res, asStackError(err).WithSteps(fmt.Sprintf("the stack's new shape was kept because putting it back failed (%v); git stack restack finishes it", rerr))
		}
		return res, asStackError(err).WithSteps("nothing was changed")
	}
	return res, err
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
