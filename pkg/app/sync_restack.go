package app

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// joinNames lists names for a message.
func joinNames(names []string) string { return strings.Join(names, ", ") }

// plannedMove is one branch's rebase, computed but not applied.
type plannedMove struct {
	name, from, to string
	worktree       string // where it is checked out, if anywhere
}

// restackPlan is one stack's computed restack.
type restackPlan struct {
	stack    *stack.Stack
	moves    []plannedMove
	bases    map[string]string // branch -> parent tip, for every branch that ends up on its parent
	conflict *Conflict
	err      error // planning failed; none of the stack's moves are applied
	notices  []string
}

// syncRestack rebases every stack onto its updated parents. Each stack is
// computed in parallel without touching refs or trees, then applied in one
// ref transaction per stack, then checked out worktrees are reset and the
// metadata's heads and bases brought up to date. A conflict stops its own
// stack at that branch; the others carry on. failed names stacks whose refs
// moved under us and were left alone.
func (a *App) syncRestack(ctx context.Context, st *syncState, res *SyncResult) (failed []string, err error) {
	n := 0
	for _, s := range st.graph.Stacks {
		n += len(s.Branches)
	}
	if n == 0 {
		return nil, nil
	}
	err = a.progress(ctx, PhaseSync, fmt.Sprintf("Restacking %d %s", n, pluralise(n, "branch", "branches")), func(ctx context.Context) error {
		plans := make([]restackPlan, len(st.graph.Stacks))
		sem := make(chan struct{}, max(1, runtime.NumCPU()))
		var wg sync.WaitGroup
		for i := range st.graph.Stacks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				plans[i] = a.planRestack(ctx, st, &st.graph.Stacks[i])
			}()
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
		bases := map[string]string{}
		for _, p := range plans {
			res.Notices = append(res.Notices, p.notices...)
			if p.err != nil {
				failed = append(failed, p.stack.Bottom())
				res.notice("stack %s was not restacked: %v", p.stack.Bottom(), p.err)
				continue
			}
			if p.conflict != nil {
				res.Conflicts = append(res.Conflicts, *p.conflict)
				res.notice("stack %s: %s conflicts when rebased onto %s (%s); run git stack restack from %s to resolve it",
					p.conflict.Stack, p.conflict.Branch, p.conflict.Onto, joinNames(p.conflict.Files), p.conflict.Branch)
			}
			if len(p.moves) > 0 {
				updates := make([]git.RefUpdate, len(p.moves))
				for i, m := range p.moves {
					updates[i] = git.RefUpdate{Ref: "refs/heads/" + m.name, New: m.to, Old: m.from}
				}
				if err := a.d.Git.UpdateRefs(ctx, st.repo, updates); err != nil {
					failed = append(failed, p.stack.Bottom())
					res.notice("stack %s: its branches moved while syncing, so it was left alone; run git stack sync again (%v)", p.stack.Bottom(), err)
					continue
				}
				resetFailed := false
				for _, m := range p.moves {
					if m.worktree != "" {
						if err := a.d.Git.ResetHard(ctx, st.worktreeRepo(m.worktree), m.to); err != nil {
							resetFailed = true
							res.notice("%s was restacked but its checkout in %s could not be updated: %v; run git reset --hard there", m.name, shortPath(m.worktree), err)
						}
					}
					lb := st.local[m.name]
					lb.Head = m.to
					st.local[m.name] = lb
					res.Restacked = append(res.Restacked, BranchMove{Name: m.name, Stack: p.stack.Bottom(), From: m.from, To: m.to})
				}
				if resetFailed {
					failed = append(failed, p.stack.Bottom())
				}
			}
			for name, base := range p.bases {
				bases[name] = base
			}
		}
		return a.d.Meta.Update(ctx, st.repo, func(g *stack.Graph) error {
			for i := range g.Stacks {
				for j := range g.Stacks[i].Branches {
					b := &g.Stacks[i].Branches[j]
					if lb, ok := st.local[b.Name]; ok {
						b.Head = lb.Head
					}
					if base, ok := bases[b.Name]; ok {
						b.Base = base
					}
				}
			}
			return nil
		})
	})
	return failed, err
}

// planRestack computes one stack's rebases, bottom to top, creating objects
// only. A branch already on its parent is skipped; a dirty checked out
// branch is left where it is and the branches above rebase onto its current
// tip; a conflict ends the plan at that branch.
func (a *App) planRestack(ctx context.Context, st *syncState, s *stack.Stack) restackPlan {
	p := restackPlan{stack: s, bases: map[string]string{}}
	parentName := s.Trunk
	parent := st.local[s.Trunk].Head
	oldParent := parent // the parent's tip before this restack, to find what the branch added
	for _, b := range s.Branches {
		lb, ok := st.local[b.Name]
		if !ok {
			continue
		}
		tip := lb.Head
		if lb.Worktree != "" && st.isDirty(ctx, a.d.Git, lb.Worktree) {
			p.notices = append(p.notices, fmt.Sprintf("%s is checked out in %s with uncommitted changes, so it was not restacked; commit or stash them and sync again",
				b.Name, shortPath(lb.Worktree)))
			parentName, parent, oldParent = b.Name, tip, tip
			continue
		}
		if a.ancestor(ctx, st, parent, tip) {
			// A branch sitting exactly on its parent keeps an older base: that
			// is what tells cleanup it was fast forwarded into trunk rather
			// than created empty, and it stays correct (the base is still
			// behind the tip, there are just no commits of its own left).
			if tip != parent || b.Base == "" {
				p.bases[b.Name] = parent
			}
			parentName, parent, oldParent = b.Name, tip, tip
			continue
		}
		from := b.Base
		if from == "" || !a.ancestor(ctx, st, from, tip) {
			mb, err := a.d.Git.MergeBase(ctx, st.repo, tip, oldParent)
			if err != nil {
				p.err = fmt.Errorf("%s: %w", b.Name, err)
				return p
			}
			from = mb
		}
		commits, err := a.d.Git.RevList(ctx, st.repo, from, tip)
		if err != nil {
			p.err = fmt.Errorf("%s: %w", b.Name, err)
			return p
		}
		r, err := a.d.Git.Replay(ctx, st.repo, commits, parent)
		if err != nil {
			p.err = fmt.Errorf("%s: %w", b.Name, err)
			return p
		}
		if r.Conflict != nil {
			p.conflict = &Conflict{Stack: s.Bottom(), Branch: b.Name, Onto: parentName, Files: r.Conflict.Files}
			return p
		}
		if lb.Worktree != "" {
			files, err := a.untrackedInTheWay(ctx, st, lb.Worktree, tip, r.Tip)
			if err != nil {
				p.err = fmt.Errorf("%s: %w", b.Name, err)
				return p
			}
			if len(files) > 0 {
				p.notices = append(p.notices, fmt.Sprintf("%s is checked out in %s and restacking it would overwrite untracked %s, so it was not restacked; move them aside and sync again",
					b.Name, shortPath(lb.Worktree), joinNames(files)))
				parentName, parent, oldParent = b.Name, tip, tip
				continue
			}
		}
		p.moves = append(p.moves, plannedMove{name: b.Name, from: tip, to: r.Tip, worktree: lb.Worktree})
		p.bases[b.Name] = parent
		parentName, parent, oldParent = b.Name, r.Tip, tip
	}
	return p
}
