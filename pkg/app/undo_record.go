package app

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// undoSnapshot is a covered command's before state while it runs. A
// restack that stops on a conflict saves it with its state, so continue can
// record the whole command once it finishes.
type undoSnapshot struct {
	Command  string    `json:"command"`
	Mode     undoMode  `json:"mode"`
	Worktree string    `json:"worktree"`
	At       time.Time `json:"at"`
	// Before.Refs holds every local branch; recording keeps only the ones
	// in Tracked or in the graph afterwards.
	Before undoState `json:"before"`
	// Tracked are the trunks and stacked branches the graph had before.
	Tracked []string `json:"tracked"`
}

type undoCtxKey struct{}

// undoMark is what undoable leaves on the context: the snapshot of the
// command running, or none to say that nothing inside records.
type undoMark struct{ snap *undoSnapshot }

// undoFrom is the snapshot of the covered command ctx runs inside, if any.
func undoFrom(ctx context.Context) *undoSnapshot {
	if m, ok := ctx.Value(undoCtxKey{}).(undoMark); ok {
		return m.snap
	}
	return nil
}

// withoutUndo marks ctx so covered commands run inside it record nothing
// (merge's sync, which happens after GitHub has already changed).
func withoutUndo(ctx context.Context) context.Context {
	return context.WithValue(ctx, undoCtxKey{}, undoMark{})
}

// undoable runs a covered command's body: it takes the before snapshot,
// runs fn and, when anything changed, records an entry. A run that stopped
// with its restack state saved records nothing yet (continue does); one that
// failed records nothing either, except a partial result, whose done part
// can be undone. A command run inside another one's undoable (or inside
// withoutUndo) just runs.
func (a *App) undoable(ctx context.Context, repo git.Repo, command string, mode undoMode, fn func(context.Context) error) error {
	if _, ok := ctx.Value(undoCtxKey{}).(undoMark); ok {
		return fn(ctx)
	}
	before, tracked, err := a.captureUndo(ctx, repo)
	if err != nil {
		return err
	}
	snap := &undoSnapshot{
		Command: command, Mode: mode, Worktree: repo.TopLevel,
		At: time.Now(), Before: before, Tracked: tracked,
	}
	err = fn(context.WithValue(ctx, undoCtxKey{}, undoMark{snap}))
	if err != nil {
		if st, lerr := loadRestackState(repo); lerr != nil || st != nil {
			return err
		}
		if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindPartial {
			return err
		}
	}
	if rerr := a.recordUndo(ctx, repo, *snap); rerr != nil {
		// The command itself worked; losing its undo isn't worth failing it.
		a.d.Log.Warn("could not record the command for undo", "command", command, "err", rerr)
	}
	return err
}

// undoableResult is undoable for a use case that returns a result.
func undoableResult[T any](a *App, ctx context.Context, repo git.Repo, command string, mode undoMode, fn func(context.Context) (T, error)) (T, error) {
	var res T
	err := a.undoable(ctx, repo, command, mode, func(ctx context.Context) error {
		var err error
		res, err = fn(ctx)
		return err
	})
	return res, err
}

// captureUndo reads the state undo can put back: every local branch's tip,
// the metadata, the remote queue and each worktree's HEAD. tracked are the
// graph's trunks and branches.
func (a *App) captureUndo(ctx context.Context, repo git.Repo) (undoState, []string, error) {
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return undoState{}, nil, err
	}
	local, err := a.localBranches(ctx, repo)
	if err != nil {
		return undoState{}, nil, err
	}
	st := undoState{Refs: make(map[string]string, len(local))}
	for name, b := range local {
		st.Refs[name] = b.Head
	}
	if st.Meta, err = a.d.Meta.Snapshot(ctx, repo); err != nil {
		return undoState{}, nil, err
	}
	if st.Queue, err = os.ReadFile(remoteStacksPath(repo)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return undoState{}, nil, err
	}
	wts, err := a.d.Git.Worktrees(ctx, repo)
	if err != nil {
		return undoState{}, nil, err
	}
	for _, w := range wts {
		if w.Bare || w.Prunable {
			continue
		}
		st.Worktrees = append(st.Worktrees, undoWorktree{Path: w.Path, Branch: w.Branch, Head: w.Head})
	}
	return st, graphNames(graph), nil
}

// graphNames are the trunks and stacked branches of g.
func graphNames(g *stack.Graph) []string {
	names := slices.Clone(g.Trunks)
	for _, s := range g.Stacks {
		names = append(names, s.Names()...)
	}
	return names
}

// recordUndo reads the after state and adds the entry, keeping only the
// branches the graph tracked before or after. A command that changed
// nothing records nothing.
func (a *App) recordUndo(ctx context.Context, repo git.Repo, snap undoSnapshot) error {
	after, tracked, err := a.captureUndo(ctx, repo)
	if err != nil {
		return err
	}
	names := append(slices.Clone(snap.Tracked), tracked...)
	before := snap.Before
	beforeRefs, afterRefs := map[string]string{}, map[string]string{}
	for _, n := range names {
		beforeRefs[n] = before.Refs[n]
		afterRefs[n] = after.Refs[n]
	}
	before.Refs, after.Refs = beforeRefs, afterRefs
	if maps.Equal(before.Refs, after.Refs) && bytes.Equal(before.Queue, after.Queue) &&
		maps.EqualFunc(before.Meta.Files, after.Meta.Files, bytes.Equal) {
		return nil
	}
	e := undoEntry{Command: snap.Command, At: snap.At, Worktree: snap.Worktree, Mode: snap.Mode, Before: before, After: after}
	return a.editUndoLog(ctx, repo, func(l *undoLog) error {
		l.Entries = slices.Insert(l.Entries, 0, e)
		return nil
	})
}

// clearUndo empties the history once GitHub has a state undo can't take
// back (a submit or merge).
func (a *App) clearUndo(ctx context.Context, repo git.Repo) error {
	return a.editUndoLog(ctx, repo, func(l *undoLog) error {
		l.Entries, l.Cleared = nil, true
		return nil
	})
}
