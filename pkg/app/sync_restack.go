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

// stackPlan is one stack's computed restack.
type stackPlan struct {
	stack *stack.Stack
	restackPlan
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
		plans := make([]stackPlan, len(st.graph.Stacks))
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

// planRestack computes one stack's rebases onto its trunk.
func (a *App) planRestack(ctx context.Context, st *syncState, s *stack.Stack) stackPlan {
	return stackPlan{stack: s, restackPlan: a.planMoves(ctx, planInput{
		repo: st.repo, stackName: s.Bottom(), branches: s.Branches,
		parentName: s.Trunk, parentTip: st.local[s.Trunk].Head,
		local: st.local, dirty: st.dirty,
	})}
}
