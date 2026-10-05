package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// SyncOptions mirrors gt sync's flags.
type SyncOptions struct {
	// Force approves resetting a diverged trunk to the remote and deleting
	// merged or closed branches without asking.
	Force bool
	// DeleteAll approves deleting merged or closed branches without asking.
	DeleteAll bool
	// NoRestack skips the restack phase.
	NoRestack bool
}

// Trunk statuses.
const (
	TrunkUpToDate      = "up-to-date"
	TrunkAhead         = "ahead" // local commits the remote doesn't have; left alone
	TrunkFastForwarded = "fast-forwarded"
	TrunkReset         = "reset" // diverged and reset to the remote with consent
	TrunkDiverged      = "diverged"
	TrunkDirty         = "dirty"
	TrunkNoRemote      = "no-remote"
)

// TrunkSync is what happened to one trunk.
type TrunkSync struct {
	Name   string `json:"name"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Status string `json:"status"`
}

// DeletedBranch is a branch sync deleted and why.
type DeletedBranch struct {
	Name   string `json:"name"`
	Head   string `json:"head,omitempty"`
	Reason string `json:"reason"`
}

// KeptBranch is a deletion candidate that was kept and why.
type KeptBranch struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// BranchMove is a branch whose tip moved.
type BranchMove struct {
	Name  string `json:"name"`
	Stack string `json:"stack,omitempty"` // the stack's bottom branch
	From  string `json:"from"`
	To    string `json:"to"`
}

// Conflict is a branch that could not be restacked.
type Conflict struct {
	Stack  string   `json:"stack"`
	Branch string   `json:"branch"`
	Onto   string   `json:"onto"`
	Files  []string `json:"files,omitempty"`
}

// RemoteAhead is a branch with commits on the remote we don't have.
type RemoteAhead struct {
	Name    string `json:"name"`
	Commits int    `json:"commits"`
}

// SyncResult reports everything sync did.
type SyncResult struct {
	Remote    string          `json:"remote"`
	Trunks    []TrunkSync     `json:"trunks"`
	Deleted   []DeletedBranch `json:"deleted,omitempty"`
	Kept      []KeptBranch    `json:"kept,omitempty"`
	Updated   []BranchMove    `json:"updated,omitempty"`
	Restacked []BranchMove    `json:"restacked,omitempty"`
	Conflicts []Conflict      `json:"conflicts,omitempty"`
	Behind    []RemoteAhead   `json:"behind,omitempty"`
	Notices   []string        `json:"notices,omitempty"`
}

// errDirty says a worktree has uncommitted changes so its checkout can't be moved.
var errDirty = errors.New("worktree has uncommitted changes")

// errRefused says git refused to fast forward a checked out branch for a reason other than uncommitted changes.
var errRefused = errors.New("git refused to fast forward")

// syncState is what the phases share.
type syncState struct {
	repo      git.Repo
	graph     *stack.Graph
	remote    string
	trunks    []string
	local     map[string]git.Branch // every local branch, with tip and checkout location
	worktrees []git.Worktree
	prs       map[string]forge.PullRequest // best PR per head branch
	dirty     map[string]bool              // worktree path -> dirty, cached
}

// worktreeRepo is repo seen from another worktree.
func (st *syncState) worktreeRepo(path string) git.Repo {
	r := st.repo
	r.TopLevel = path
	return r
}

// isDirty reports whether a worktree has staged or unstaged changes.
// Untracked files don't count.
func (st *syncState) isDirty(ctx context.Context, g *git.Client, path string) bool {
	if v, ok := st.dirty[path]; ok {
		return v
	}
	r := st.worktreeRepo(path)
	staged, _ := g.HasStagedChanges(ctx, r)
	unstaged, _ := g.HasUnstagedChanges(ctx, r)
	st.dirty[path] = staged || unstaged
	return st.dirty[path]
}

// Sync fetches, moves trunk, deletes merged branches, pulls in branches the
// remote advanced and restacks every stack, the way gt sync does. Nothing is
// pushed; submit does that.
func (a *App) Sync(ctx context.Context, repo git.Repo, o SyncOptions) (SyncResult, error) {
	var res SyncResult
	st, err := a.gatherSync(ctx, repo)
	if err != nil {
		return res, err
	}
	res.Remote = st.remote

	err = a.progress(ctx, PhaseSync, "Fetching "+st.remote, func(ctx context.Context) error {
		return a.d.Git.Fetch(ctx, repo, st.remote)
	})
	if err != nil {
		return res, stack.Newf(stack.KindAPIFailure, "could not fetch from %s", st.remote).
			WithDetail(err.Error()).WithCause(err).
			WithSteps("check the remote and your network, then run git stack sync again")
	}
	st.prs = PRsFor(a.loadPRs(ctx, repo, PRsFresh))
	if err := a.syncTrunks(ctx, st, o, &res); err != nil {
		return res, err
	}
	if err := a.syncCleanup(ctx, st, o, &res); err != nil {
		return res, err
	}
	if err := a.syncRemote(ctx, st, &res); err != nil {
		return res, err
	}
	// Later task: restack.

	if a.d.Forge != nil {
		if _, err := a.RefreshPRs(ctx, repo); err != nil {
			a.d.Log.Debug("refresh after sync", "err", err)
		}
	}
	return res, nil
}

// gatherSync loads everything the phases look at.
func (a *App) gatherSync(ctx context.Context, repo git.Repo) (*syncState, error) {
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return nil, err
	}
	st := &syncState{repo: repo, graph: graph, dirty: map[string]bool{}, local: map[string]git.Branch{}}
	st.trunks = graph.Trunks
	if len(st.trunks) == 0 {
		def, ok, err := a.d.Git.DefaultBranch(ctx, repo)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, stack.New(stack.KindNotInStack, "there is no stack and no default branch to sync").
				WithSteps("check out your trunk and run git stack create to start a stack")
		}
		st.trunks = []string{def}
	}
	st.remote = a.d.Git.RemoteFor(ctx, repo, st.trunks[0])
	branches, err := a.d.Git.Branches(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, b := range branches {
		st.local[b.Name] = b
	}
	if st.worktrees, err = a.d.Git.Worktrees(ctx, repo); err != nil {
		return nil, err
	}
	return st, nil
}

// moveBranch moves a local branch from one tip to another. A branch nobody
// has checked out moves by ref; one checked out in a clean worktree is fast
// forwarded there (or reset when reset is set, for a diverged trunk); one
// checked out in a dirty worktree is left alone with errDirty.
func (a *App) moveBranch(ctx context.Context, st *syncState, name, from, to string, reset bool) error {
	lb := st.local[name]
	if lb.Worktree == "" {
		if err := a.d.Git.UpdateRefs(ctx, st.repo, []git.RefUpdate{{Ref: "refs/heads/" + name, New: to, Old: from}}); err != nil {
			return err
		}
	} else {
		if st.isDirty(ctx, a.d.Git, lb.Worktree) {
			return errDirty
		}
		wt := st.worktreeRepo(lb.Worktree)
		var err error
		if reset {
			err = a.d.Git.ResetHard(ctx, wt, to)
		} else {
			err = a.d.Git.MergeFF(ctx, wt, to)
			if err != nil {
				return fmt.Errorf("%w: %w", errRefused, err)
			}
		}
		if err != nil {
			return err
		}
	}
	lb.Head = to
	st.local[name] = lb
	return nil
}

// notice appends a formatted notice.
func (r *SyncResult) notice(format string, args ...any) {
	r.Notices = append(r.Notices, fmt.Sprintf(format, args...))
}
