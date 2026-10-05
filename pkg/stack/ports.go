package stack

import (
	"context"

	"github.com/DomBlack/git-stack/pkg/git"
)

// Scope selects which part of the current stack an operation covers.
type Scope int

const (
	ScopeAll Scope = iota
	ScopeUpstack
	ScopeDownstack
	ScopeOnly
)

func (s Scope) String() string {
	switch s {
	case ScopeAll:
		return "all"
	case ScopeUpstack:
		return "upstack"
	case ScopeDownstack:
		return "downstack"
	case ScopeOnly:
		return "only"
	default:
		return "unknown"
	}
}

// Metadata reads and edits the stack graph. Load must be fast and offline:
// shell completion depends on it.
type Metadata interface {
	Load(ctx context.Context, repo git.Repo) (*Graph, error)
	// Update loads the graph fresh under the backend's lock, lets fn edit it
	// and writes the result back. fn may edit each stack's Branches (remove
	// entries, change Head and Base) but must not add, remove or reorder
	// stacks; a stack left with no branches is removed. When fn returns an
	// error nothing is written and the error is returned.
	Update(ctx context.Context, repo git.Repo, fn func(*Graph) error) error
}

// Tracker registers branches with the backend.
type Tracker interface {
	// InitStack creates a new stack on trunk from the given branch names.
	// Existing branches are adopted, missing ones are created from the
	// previous branch, and the last one is checked out.
	InitStack(ctx context.Context, repo git.Repo, trunk string, branches []string) error
	// AddTop creates (or adopts) name on top of the current stack and checks
	// it out. The current branch must be the top of its stack.
	AddTop(ctx context.Context, repo git.Repo, name string) error
}

// Restacker rebases branches onto their parents.
type Restacker interface {
	// Restack rebases the given scope of the current stack, locally, without
	// fetching. ScopeOnly may return KindUnsupported.
	Restack(ctx context.Context, repo git.Repo, scope Scope) error
	Continue(ctx context.Context, repo git.Repo) error
	Abort(ctx context.Context, repo git.Repo) error
}

// SubmitOptions controls Submitter.Submit.
type SubmitOptions struct {
	// Publish creates PRs ready for review instead of drafts.
	Publish bool
	// Interactive lets the backend open its own editor on the terminal.
	Interactive bool
}

// SubmitResult carries the backend's human-readable output for relaying.
type SubmitResult struct {
	Output string
	// Streamed is true when Output was already written to the user's
	// terminal as it was produced, so callers must not print it again.
	Streamed bool
}

// Submitter pushes the current stack and creates or updates its PRs.
type Submitter interface {
	Submit(ctx context.Context, repo git.Repo, o SubmitOptions) (SubmitResult, error)
}

// SyncOptions controls Syncer.Sync.
type SyncOptions struct {
	// Prune deletes local branches whose PRs were merged.
	Prune bool
	// Dir is the worktree to sync in; empty means the repository's own
	// working tree. gh stack syncs the stack of the branch checked out there.
	Dir string
}

// SyncResult carries the backend's human-readable output for relaying.
type SyncResult struct {
	Output string
	// Streamed is true when Output was already written to the user's
	// terminal as it was produced, so callers must not print it again.
	Streamed bool
}

// Syncer fetches, updates trunk, restacks and prunes the current stack.
type Syncer interface {
	Sync(ctx context.Context, repo git.Repo, o SyncOptions) (SyncResult, error)
}
