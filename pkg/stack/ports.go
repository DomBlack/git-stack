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

// Metadata reads the stack graph. Implementations must be fast and offline:
// shell completion depends on this call.
type Metadata interface {
	Load(ctx context.Context, repo git.Repo) (*Graph, error)
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

// Submitter pushes the current stack and creates or updates its PRs.
type Submitter interface {
	Submit(ctx context.Context, repo git.Repo, o SubmitOptions) error
}

// SyncOptions controls Syncer.Sync.
type SyncOptions struct {
	// Prune deletes local branches whose PRs were merged.
	Prune bool
}

// Syncer fetches, updates trunk, restacks and prunes the current stack.
type Syncer interface {
	Sync(ctx context.Context, repo git.Repo, o SyncOptions) error
}
