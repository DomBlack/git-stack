package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// ViewOptions controls View.
type ViewOptions struct {
	// CurrentStackOnly limits rows to the stack of the current branch (and
	// its trunk).
	CurrentStackOnly bool
	// IncludeUntracked adds local branches that are in no stack.
	IncludeUntracked bool
	// SkipRestackCheck avoids the per-branch ancestry check (completion).
	SkipRestackCheck bool
	// PRs selects how pull-request state is obtained (default: none).
	PRs PRMode
}

// Row is one line of the stack tree, in display order.
type Row struct {
	Name string
	// Depth is 0 for a trunk, 1 for a bottom branch, and so on.
	Depth int
	// Parent is the branch below (empty for trunks and untracked branches).
	Parent    string
	IsTrunk   bool
	IsCurrent bool
	// Tracked is false for branches that are in no stack.
	Tracked bool
	// NeedsRestack is true when the parent's tip is not in the branch's
	// history.
	NeedsRestack bool
	Head         string
	LastCommit   time.Time
	// Worktree is the path of another worktree the branch is checked out in.
	Worktree string
	// PR is the known pull request, if any. State is StateUnknown until the
	// forge has been consulted.
	PR *forge.PullRequest
	// StackIndex identifies the stack within View.Graph (-1 for trunks and
	// untracked branches).
	StackIndex int
}

// View is the rendered state of the repository's stacks.
type View struct {
	Repo    git.Repo
	Current string
	Graph   *stack.Graph
	// Rows lists trunks and their stacks in display order, then untracked
	// branches when requested.
	Rows []Row
}

// Row returns the row for a branch.
func (v *View) Row(name string) (Row, bool) {
	i := slices.IndexFunc(v.Rows, func(r Row) bool { return r.Name == name })
	if i < 0 {
		return Row{}, false
	}
	return v.Rows[i], true
}

// View builds the stack tree from backend metadata and local git state.
// It never talks to the forge; see DecoratePRs.
func (a *App) View(ctx context.Context, repo git.Repo, o ViewOptions) (*View, error) {
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return nil, err
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil && !errors.Is(err, git.ErrDetached) {
		return nil, err
	}
	branches, err := a.d.Git.Branches(ctx, repo)
	if err != nil {
		return nil, err
	}
	local := make(map[string]git.Branch, len(branches))
	for _, b := range branches {
		local[b.Name] = b
	}

	v := &View{Repo: repo, Current: current, Graph: graph}
	stacks := graph.Stacks
	trunks := graph.Trunks
	if len(trunks) == 0 {
		// No stacks yet: still show the default branch as the trunk so the
		// tree (and the picker) has a root.
		if def, ok, err := a.d.Git.DefaultBranch(ctx, repo); err == nil && ok {
			trunks = []string{def}
		}
	}
	if o.CurrentStackOnly {
		if s, _, ok := graph.StackOf(current); ok {
			stacks = []stack.Stack{*s}
			trunks = []string{s.Trunk}
		} else if graph.IsTrunk(current) {
			trunks = []string{current}
			stacks = nil
			for _, s := range graph.StacksOn(current) {
				stacks = append(stacks, *s)
			}
		}
	}

	for _, trunk := range trunks {
		v.Rows = append(v.Rows, a.row(trunk, 0, "", true, current, local, -1))
		for si := range stacks {
			s := &stacks[si]
			if s.Trunk != trunk {
				continue
			}
			parent := trunk
			for depth, b := range s.Branches {
				row := a.row(b.Name, depth+1, parent, true, current, local, slices.IndexFunc(graph.Stacks, func(g stack.Stack) bool { return g.Trunk == s.Trunk && g.Bottom() == s.Bottom() }))
				if b.PR != nil {
					row.PR = &forge.PullRequest{Number: b.PR.Number, URL: b.PR.URL, Head: b.Name, Base: parent}
					if b.PR.Merged {
						row.PR.State = forge.StateMerged
					}
				}
				if !o.SkipRestackCheck && !b.Merged() {
					if _, ok := local[b.Name]; ok {
						if _, ok := local[parent]; ok {
							ok, err := a.d.Git.IsAncestor(ctx, repo, parent, b.Name)
							if err != nil {
								return nil, err
							}
							row.NeedsRestack = !ok
						}
					}
				}
				v.Rows = append(v.Rows, row)
				parent = b.Name
			}
		}
	}

	if o.IncludeUntracked {
		for _, b := range branches {
			if !graph.Tracked(b.Name) && !slices.Contains(trunks, b.Name) {
				v.Rows = append(v.Rows, a.row(b.Name, 0, "", false, current, local, -1))
			}
		}
	}
	if prs := a.loadPRs(ctx, repo, o.PRs); prs != nil {
		v.ApplyPRs(prs)
	}
	return v, nil
}

func (a *App) row(name string, depth int, parent string, tracked bool, current string, local map[string]git.Branch, stackIndex int) Row {
	r := Row{Name: name, Depth: depth, Parent: parent, IsTrunk: depth == 0 && tracked, IsCurrent: name == current, Tracked: tracked, StackIndex: stackIndex}
	if b, ok := local[name]; ok {
		r.Head = b.Head
		r.LastCommit = b.CommitTime
		if b.Worktree != "" && !r.IsCurrent {
			r.Worktree = b.Worktree
		}
	}
	return r
}
