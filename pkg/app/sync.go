package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/shell"
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
	// TrunkNotUpdated: the trunk should have moved but its checkout could
	// not be updated; NotUpdated says why.
	TrunkNotUpdated = "not-updated"
)

// Reasons a branch sync meant to move was left where it was.
const (
	// NotUpdatedDirty: uncommitted changes in its checkout would be
	// overwritten (Changed, and Untracked if there are those too). The
	// user's own work is in the way, so this is a warning, not a failure.
	NotUpdatedDirty = "dirty"
	// NotUpdatedUntracked: untracked files in its checkout are where the
	// update would write (Untracked). Also a warning.
	NotUpdatedUntracked = "untracked"
	// NotUpdatedLocked: another git process held a lock the update needed:
	// the checkout's index lock for longer than the retry budget (LockKind
	// "index"), or the branch's ref lock (LockKind "ref", never retried in a
	// checkout and possible for a branch with no checkout). Lock is the file.
	// Sync fails so it gets run again.
	NotUpdatedLocked = "locked"
	// NotUpdatedRefused: git would not move the checkout for any reason
	// other than the user's files being in the way (those are dirty and
	// untracked) or a held lock, e.g. a merge in progress there. Sync fails.
	NotUpdatedRefused = "refused"
)

// NotUpdated is a branch sync meant to move (a trunk or a branch the remote
// advanced) that is still where it was.
type NotUpdated struct {
	Name     string `json:"name"`
	Worktree string `json:"worktree,omitempty"`
	Reason   string `json:"reason"`
	// Detail is git's one line explanation, for refused.
	Detail string `json:"detail,omitempty"`
	// Changed and Untracked are the files in the way, relative to the
	// checkout, for dirty and untracked; at most maxListedFiles each, with
	// MoreChanged / MoreUntracked counting the rest.
	Changed       []string `json:"changed,omitempty"`
	MoreChanged   int      `json:"moreChanged,omitempty"`
	Untracked     []string `json:"untracked,omitempty"`
	MoreUntracked int      `json:"moreUntracked,omitempty"`
	// Lock is the lock file that was held, for locked, and LockKind says
	// whose it is: "index" (the checkout's index) or "ref" (a branch's).
	Lock     string `json:"lock,omitempty"`
	LockKind string `json:"lockKind,omitempty"`
}

// Failed reports whether the branch not moving makes the sync fail (as
// opposed to a notice about the user's own uncommitted changes).
func (n NotUpdated) Failed() bool {
	return n.Reason != NotUpdatedDirty && n.Reason != NotUpdatedUntracked
}

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
	// NotUpdated lists branches sync meant to move but couldn't.
	NotUpdated []NotUpdated `json:"notUpdated,omitempty"`
	Notices    []string     `json:"notices,omitempty"`
}

// errRefused says a checked out branch could not be moved for a reason other
// than uncommitted changes: git refused to fast forward it, or a reset would
// overwrite untracked files.
var errRefused = errors.New("the checkout was left alone")

// blockedError says a checked out branch could not be moved because files
// in its worktree are in the way: local changes the move would overwrite
// and untracked files where it would write. Both are the user's own work.
type blockedError struct{ changed, untracked []string }

func (e *blockedError) Error() string {
	return fmt.Sprintf("%d changed and %d untracked files are in the way", len(e.changed), len(e.untracked))
}

// errLocked says a checked out branch could not be moved because another
// git process held its worktree's index lock throughout the retries.
var errLocked = errors.New("another git process holds a lock it needs")

// syncState is what the phases share.
type syncState struct {
	repo      git.Repo
	graph     *stack.Graph
	remote    string
	trunks    []string
	local     map[string]git.Branch // every local branch, with tip and checkout location
	worktrees []git.Worktree
	prs       map[string]forge.PullRequest // best PR per head branch
	dirty     *dirtyCache
}

// worktreeRepo is repo seen from another worktree.
func (st *syncState) worktreeRepo(path string) git.Repo { return worktreeRepo(st.repo, path) }

// isDirty reports whether a worktree has staged or unstaged changes.
func (st *syncState) isDirty(ctx context.Context, g *git.Client, path string) bool {
	return st.dirty.isDirty(ctx, g, st.repo, path)
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

	// Listing pull requests is a forge round trip that can take longer than
	// the fetch itself on a busy repository, so it runs alongside the fetch
	// and gets its own headline for whatever is left when the fetch is done.
	prsCh := make(chan []forge.PullRequest, 1)
	go func() { prsCh <- a.syncPRs(ctx, repo) }()
	err = a.progress(ctx, PhaseSync, "Fetching "+st.remote, func(ctx context.Context) error {
		return a.d.Git.Fetch(ctx, repo, st.remote)
	})
	if err != nil {
		return res, stack.Newf(stack.KindAPIFailure, "could not fetch from %s", st.remote).
			WithDetail(err.Error()).WithCause(err).
			WithSteps("check the remote and your network, then run git stack sync again")
	}
	err = a.progress(ctx, PhaseSync, "Checking pull requests on "+st.remote, func(ctx context.Context) error {
		select {
		case prs := <-prsCh:
			st.prs = PRsFor(prs)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		return res, err
	}
	if err := a.syncTrunks(ctx, st, o, &res); err != nil {
		return res, err
	}
	if err := a.syncCleanup(ctx, st, o, &res); err != nil {
		return res, err
	}
	if err := a.syncRemote(ctx, st, &res); err != nil {
		return res, err
	}
	var failed []string
	if !o.NoRestack {
		if failed, err = a.syncRestack(ctx, st, &res); err != nil {
			return res, err
		}
	}

	var stuck []NotUpdated
	for _, n := range res.NotUpdated {
		if n.Failed() {
			stuck = append(stuck, n)
		}
	}
	if len(res.Conflicts) > 0 || len(failed) > 0 || len(stuck) > 0 {
		var parts, steps []string
		for _, n := range stuck {
			part, step := n.explain()
			parts = append(parts, part)
			steps = append(steps, step...)
		}
		if n := len(res.Conflicts); n > 0 {
			names := make([]string, n)
			for i, c := range res.Conflicts {
				names[i] = c.Branch
			}
			parts = append(parts, fmt.Sprintf("%d %s (%s)", n, pluralise(n, "conflict", "conflicts"), joinNames(names)))
			steps = append(steps, "run git stack restack from the conflicting branch to resolve it")
		}
		if n := len(failed); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s not restacked (%s)", n, pluralise(n, "stack", "stacks"), joinNames(failed)))
			steps = append(steps, "see the notes above, then run git stack sync again")
		}
		kind := stack.KindConflict
		if len(res.Conflicts) == 0 && len(failed) == 0 {
			kind = stack.KindPartial
		}
		return res, stack.Newf(kind, "%s; everything else is in sync", strings.Join(parts, "; ")).WithSteps(steps...)
	}
	return res, nil
}

// syncPRs asks the forge for every PR, whatever the age of the cache: a PR
// merged a minute ago must be seen. The cache, refreshed by the same call,
// is only the fallback when the forge can't be reached.
func (a *App) syncPRs(ctx context.Context, repo git.Repo) []forge.PullRequest {
	if a.d.Forge != nil {
		prs, err := a.RefreshPRs(ctx, repo)
		if err == nil {
			return prs
		}
		a.d.Log.Debug("could not list pull requests; using the cache", "err", err)
	}
	prs, _, _ := a.CachedPRs(repo)
	return prs
}

// gatherSync loads everything the phases look at.
func (a *App) gatherSync(ctx context.Context, repo git.Repo) (*syncState, error) {
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return nil, err
	}
	st := &syncState{repo: repo, graph: graph, dirty: newDirtyCache(), local: map[string]git.Branch{}}
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
// has checked out moves by ref; one checked out in a worktree is fast
// forwarded there, keeping local changes the move doesn't touch (or reset
// when reset is set, for a diverged trunk, which needs a clean checkout).
// Files in the way leave it alone with a *blockedError naming them.
func (a *App) moveBranch(ctx context.Context, st *syncState, name, from, to string, reset bool) error {
	lb := st.local[name]
	if lb.Worktree == "" {
		if err := a.d.Git.UpdateRefs(ctx, st.repo, []git.RefUpdate{{Ref: "refs/heads/" + name, New: to, Old: from}}); err != nil {
			if git.LockPath(err) != "" {
				return fmt.Errorf("%w: %w", errLocked, err)
			}
			return err
		}
	} else {
		wt := st.worktreeRepo(lb.Worktree)
		var err error
		if reset {
			// reset --hard throws local changes away, so any of them stops it,
			// as does an untracked file where the reset would write.
			changed, cerr := a.d.Git.ChangedFiles(ctx, wt)
			if cerr != nil {
				return cerr
			}
			untracked, uerr := a.untrackedInTheWay(ctx, st.repo, lb.Worktree, from, to)
			if uerr != nil {
				return uerr
			}
			if len(changed)+len(untracked) > 0 {
				return &blockedError{changed: changed, untracked: untracked}
			}
			err = a.d.Git.ResetHard(ctx, wt, to)
		} else {
			// A fast forward keeps local changes it doesn't touch, as git pull
			// does; git refuses before touching anything when one is in the way.
			err = a.d.Git.MergeFF(ctx, wt, to)
		}
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return ctx.Err()
		case git.LockPath(err) != "":
			return fmt.Errorf("%w: %w", errLocked, err)
		default:
			if oe, ok := errors.AsType[*git.OverwriteError](err); ok {
				return &blockedError{changed: oe.Changed, untracked: oe.Untracked}
			}
			return fmt.Errorf("%w: %w", errRefused, err)
		}
	}
	lb.Head = to
	st.local[name] = lb
	return nil
}

// untrackedInTheWay lists the untracked files in the worktree at path that
// moving its checkout from one commit to another would overwrite: git reset
// --hard deletes them without a word. A file is in the way when the move
// adds the same path, a path under it (it would become a directory) or the
// directory it sits in (as a file).
func (a *App) untrackedInTheWay(ctx context.Context, repo git.Repo, path, from, to string) ([]string, error) {
	added, err := a.d.Git.AddedPaths(ctx, repo, from, to)
	if err != nil || len(added) == 0 {
		return nil, err
	}
	untracked, err := a.d.Git.Untracked(ctx, worktreeRepo(repo, path))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range untracked {
		if slices.ContainsFunc(added, func(f string) bool {
			return f == u || strings.HasPrefix(f, u+"/") || strings.HasPrefix(u, f+"/")
		}) {
			out = append(out, u)
		}
	}
	return out, nil
}

// notUpdated classifies why moveBranch left name alone, and false for an
// error that should stop the sync instead.
func (a *App) notUpdated(ctx context.Context, st *syncState, name, worktree string, err error) (NotUpdated, bool) {
	n := NotUpdated{Name: name, Worktree: worktree}
	if be, ok := errors.AsType[*blockedError](err); ok {
		n.Reason = NotUpdatedUntracked
		if len(be.changed) > 0 {
			n.Reason = NotUpdatedDirty
		}
		n.Changed, n.MoreChanged = capFiles(be.changed)
		n.Untracked, n.MoreUntracked = capFiles(be.untracked)
		return n, true
	}
	switch {
	case errors.Is(err, errLocked):
		n.Reason = NotUpdatedLocked
		n.Lock = git.LockPath(err)
		n.LockKind = "ref"
		if errors.Is(err, git.ErrIndexLocked) {
			n.LockKind = "index"
		}
		if !filepath.IsAbs(n.Lock) && worktree != "" {
			// git names the index lock relative to the worktree it ran in.
			if lock, lerr := a.d.Git.IndexLockPath(ctx, st.worktreeRepo(worktree)); lerr == nil && errors.Is(err, git.ErrIndexLocked) {
				n.Lock = lock
			}
		}
	case errors.Is(err, errRefused):
		n.Reason = NotUpdatedRefused
		// "the checkout was left alone: fast forward: <git>" -> "fast forward: <git>"
		n.Detail = strings.TrimPrefix(err.Error(), errRefused.Error()+": ")
	default:
		return NotUpdated{}, false
	}
	return n, true
}

// notice appends a formatted notice.
func (r *SyncResult) notice(format string, args ...any) {
	r.Notices = append(r.Notices, fmt.Sprintf(format, args...))
}

// explain is the one line reason a branch wasn't updated, for the error that
// ends the sync, and what to do about it.
func (n NotUpdated) explain() (string, []string) {
	where := ""
	if n.Worktree != "" {
		where = " in " + shortPath(n.Worktree)
	}
	switch n.Reason {
	case NotUpdatedLocked:
		steps := []string{"wait for the other git process (an IDE, a prompt, an agent) to finish, then run git stack sync again"}
		if n.Lock != "" {
			if lock, ok := shell.Path(shortPath(n.Lock)); ok {
				steps = append(steps, "if no git process is running the lock is stale; rm "+lock+", then git stack sync again")
			} else {
				steps = append(steps, fmt.Sprintf("if no git process is running the lock is stale; remove the lock file %q by hand, then git stack sync again", n.Lock))
			}
		}
		if n.LockKind == "index" {
			return fmt.Sprintf("%s is checked out%s and another git process holds its index lock, so %s was not updated", n.Name, where, n.Name), steps
		}
		// git takes a branch's lock after it has moved the checkout, so the
		// checkout can show the incoming files as staged until the next sync
		// finishes the job.
		msg := fmt.Sprintf("another git process holds the lock on %s, so %s was not updated", n.Name, n.Name)
		if n.Worktree != "" {
			msg += fmt.Sprintf("; until it is, its checkout%s may show the incoming changes as staged", where)
		}
		return msg, steps
	default:
		return fmt.Sprintf("%s is checked out%s and was not updated (%s)", n.Name, where, n.Detail),
			[]string{"sort out the checkout" + where + ", then run git stack sync again"}
	}
}

// maxListedFiles caps each file list in a NotUpdated, so one huge checkout
// doesn't flood an agent's context; the count of the rest is kept.
const maxListedFiles = 20

func capFiles(files []string) ([]string, int) {
	if len(files) <= maxListedFiles {
		return files, 0
	}
	return files[:maxListedFiles], len(files) - maxListedFiles
}
