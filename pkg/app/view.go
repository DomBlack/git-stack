package app

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
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
	// Commits fills each stacked branch's Row.Commits (one local git read
	// per branch, run concurrently).
	Commits bool
}

// MaxCommits is how many of a branch's own commits a Row carries; the rest
// are only counted, in Row.MoreCommits.
const MaxCommits = 10

// Commit is one of a branch's own commits.
type Commit struct {
	SHA     string
	Subject string
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
	// NeedsPush is true when the branch is on the remote but the local
	// branch has moved on from it (new commits, a restack, an amend), so
	// its PR is behind until the next submit.
	NeedsPush  bool
	Head       string
	LastCommit time.Time
	// Worktree is the path of another worktree the branch is checked out in.
	Worktree string
	// PR is the known pull request, if any. State is StateUnknown until the
	// forge has been consulted.
	PR *forge.PullRequest
	// StackIndex identifies the stack within View.Graph (-1 for trunks and
	// untracked branches).
	StackIndex int
	// Commits are the branch's own commits, newest first: those on it but
	// not on its parent (trunk for the bottom branch), nor on the parent
	// tip it was last restacked onto. At most MaxCommits, and only filled
	// when ViewOptions.Commits asks for them.
	Commits []Commit
	// MoreCommits counts the branch's commits beyond Commits.
	MoreCommits int
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

	var jobs []commitsJob
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
						if row.NeedsPush, err = a.needsPush(ctx, repo, b.Name, row.Head); err != nil {
							return nil, err
						}
					}
				}
				if o.Commits {
					_, hasBranch := local[b.Name]
					// Without the parent there's nothing to stop at; the
					// list would run back to the root commit.
					if _, hasParent := local[parent]; hasBranch && hasParent {
						jobs = append(jobs, commitsJob{row: len(v.Rows), branch: b.Name, parent: parent, base: b.Base})
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
	if err := a.fillCommits(ctx, repo, v, jobs); err != nil {
		return nil, err
	}
	prs, err := a.loadPRs(ctx, repo, o.PRs)
	if err != nil {
		return nil, err
	}
	if prs != nil {
		v.ApplyPRs(prs)
	}
	return v, nil
}

// commitsJob is one branch whose commits View lists.
type commitsJob struct {
	row                  int
	branch, parent, base string
}

// fillCommits lists each job's commits onto its row: one git read per
// branch, a few at a time.
func (a *App) fillCommits(ctx context.Context, repo git.Repo, v *View, jobs []commitsJob) error {
	errs := make([]error, len(jobs))
	sem := make(chan struct{}, max(1, runtime.NumCPU()))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			commits, err := a.d.Git.Commits(ctx, repo, "refs/heads/"+j.branch, "refs/heads/"+j.parent, j.base)
			if err != nil {
				errs[i] = err
				return
			}
			r := &v.Rows[j.row]
			for _, c := range commits[:min(len(commits), MaxCommits)] {
				r.Commits = append(r.Commits, Commit{SHA: c.SHA, Subject: c.Subject})
			}
			r.MoreCommits = len(commits) - len(r.Commits)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
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

// needsPush reports whether the local branch has moved on from what its
// remote tracking ref says was pushed. A branch that was never pushed, or
// that the remote is simply ahead of, doesn't count.
func (a *App) needsPush(ctx context.Context, repo git.Repo, name, head string) (bool, error) {
	_, tip, ok, err := a.remoteTip(ctx, repo, name)
	if err != nil || !ok || tip == head {
		return false, err
	}
	behind, err := a.d.Git.IsAncestor(ctx, repo, head, tip)
	return !behind, err
}
