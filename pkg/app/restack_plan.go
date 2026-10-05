package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// dirtyCache remembers whether a worktree has staged or unstaged changes.
// Untracked files don't count. Safe for parallel planners.
type dirtyCache struct {
	mu sync.Mutex
	m  map[string]bool
}

func newDirtyCache() *dirtyCache { return &dirtyCache{m: map[string]bool{}} }

func (d *dirtyCache) isDirty(ctx context.Context, g *git.Client, repo git.Repo, path string) bool {
	d.mu.Lock()
	v, ok := d.m[path]
	d.mu.Unlock()
	if ok {
		return v
	}
	r := worktreeRepo(repo, path)
	staged, _ := g.HasStagedChanges(ctx, r)
	unstaged, _ := g.HasUnstagedChanges(ctx, r)
	v = staged || unstaged
	d.mu.Lock()
	d.m[path] = v
	d.mu.Unlock()
	return v
}

// worktreeRepo is repo seen from another worktree.
func worktreeRepo(repo git.Repo, path string) git.Repo {
	repo.TopLevel = path
	return repo
}

// planInput is one run of the planner: a parent and the ordered branches
// to put on top of it.
type planInput struct {
	repo git.Repo
	// stackName is the stack's bottom branch, for Conflict.Stack.
	stackName string
	// branches run bottom to top; parentName/parentTip is the parent of
	// branches[0] (a trunk, or the branch below the scope).
	branches   []stack.Branch
	parentName string
	parentTip  string
	local      map[string]git.Branch
	dirty      *dirtyCache
	// currentWorktree, when set, names the worktree whose checked out
	// branch the caller moves itself (with reset --keep) rather than
	// skipping it when it has local changes.
	currentWorktree string
}

// plannedMove is one branch's rebase, computed but not applied.
type plannedMove struct {
	name, from, to string
	oldBase        string // the base recorded before the move, for abort
	worktree       string // where it is checked out, if anywhere
	empty          bool   // every commit was already in the parent; to == parent tip
}

// restackPlan is a computed run of rebases.
type restackPlan struct {
	moves   []plannedMove
	inPlace []string          // already on their parent
	bases   map[string]string // branch -> parent tip, for every branch that ends up on its parent
	// conflict is set when the plan stopped on a branch; conflictFrom is
	// where that branch's own commits start and conflictNewBase the parent
	// tip it needs to go onto.
	conflict        *Conflict
	conflictFrom    string
	conflictNewBase string
	err             error // planning failed; none of the moves may be applied
	notices         []string
}

// planMoves computes the rebases for in.branches, bottom to top, creating
// objects only. A branch already on its parent is in place; a dirty checked
// out branch (outside in.currentWorktree) is left where it is and the
// branches above rebase onto its current tip; a conflict ends the plan at
// that branch.
func (a *App) planMoves(ctx context.Context, in planInput) restackPlan {
	p := restackPlan{bases: map[string]string{}}
	parentName, parent := in.parentName, in.parentTip
	oldParent := parent // the parent's tip before this run, to find what the branch added
	for _, b := range in.branches {
		lb, ok := in.local[b.Name]
		if !ok {
			continue
		}
		tip := lb.Head
		if lb.Worktree != "" && lb.Worktree != in.currentWorktree && in.dirty.isDirty(ctx, a.d.Git, in.repo, lb.Worktree) {
			p.notices = append(p.notices, fmt.Sprintf("%s is checked out in %s with uncommitted changes, so it was not restacked; commit or stash them and try again",
				b.Name, shortPath(lb.Worktree)))
			parentName, parent, oldParent = b.Name, tip, tip
			continue
		}
		if a.isAncestor(ctx, in.repo, parent, tip) {
			// A branch sitting exactly on its parent keeps an older base: that
			// is what tells cleanup it was fast forwarded into trunk rather
			// than created empty, and it stays correct (the base is still
			// behind the tip, there are just no commits of its own left).
			if tip != parent || b.Base == "" {
				p.bases[b.Name] = parent
			}
			p.inPlace = append(p.inPlace, b.Name)
			parentName, parent, oldParent = b.Name, tip, tip
			continue
		}
		from := b.Base
		if from == "" || !a.isAncestor(ctx, in.repo, from, tip) {
			mb, err := a.d.Git.MergeBase(ctx, in.repo, tip, oldParent)
			if err != nil {
				p.err = fmt.Errorf("%s: %w", b.Name, err)
				return p
			}
			from = mb
		}
		commits, err := a.d.Git.RevList(ctx, in.repo, from, tip)
		if err != nil {
			p.err = fmt.Errorf("%s: %w", b.Name, err)
			return p
		}
		r, err := a.d.Git.Replay(ctx, in.repo, commits, parent)
		if err != nil {
			p.err = fmt.Errorf("%s: %w", b.Name, err)
			return p
		}
		if r.Conflict != nil {
			p.conflict = &Conflict{Stack: in.stackName, Branch: b.Name, Onto: parentName, Files: r.Conflict.Files}
			p.conflictFrom, p.conflictNewBase = from, parent
			return p
		}
		if lb.Worktree != "" {
			files, err := a.untrackedInTheWay(ctx, in.repo, lb.Worktree, tip, r.Tip)
			if err != nil {
				p.err = fmt.Errorf("%s: %w", b.Name, err)
				return p
			}
			if len(files) > 0 {
				p.notices = append(p.notices, fmt.Sprintf("%s is checked out in %s and restacking it would overwrite untracked %s, so it was not restacked; move them aside and try again",
					b.Name, shortPath(lb.Worktree), joinNames(files)))
				parentName, parent, oldParent = b.Name, tip, tip
				continue
			}
		}
		p.moves = append(p.moves, plannedMove{name: b.Name, from: tip, to: r.Tip, oldBase: b.Base, worktree: lb.Worktree, empty: r.Tip == parent})
		p.bases[b.Name] = parent
		parentName, parent, oldParent = b.Name, r.Tip, tip
	}
	return p
}
