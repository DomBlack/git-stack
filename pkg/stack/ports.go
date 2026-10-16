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
	// entries, change Head and Base, rename one by setting Name and
	// RenamedFrom) and may also append stacks (each with a
	// trunk and at least one branch; they are written to the current
	// worktree's file, else the main checkout's, else a new file in the
	// common dir) and may set or clear a stack's ID and Number (clearing
	// removes them from the record). It must not remove or reorder existing
	// stacks; a stack left with no branches is removed. When fn returns an
	// error nothing is written and the error is returned.
	Update(ctx context.Context, repo git.Repo, fn func(*Graph) error) error
	// Snapshot reads the metadata exactly as stored, under the backend's
	// lock.
	Snapshot(ctx context.Context, repo git.Repo) (Snapshot, error)
	// Restore writes a snapshot back byte for byte under the lock, removing
	// the files that did not exist when it was taken.
	Restore(ctx context.Context, repo git.Repo, s Snapshot) error
}

// Snapshot is the backend's metadata exactly as stored, opaque to everything
// but the backend that made it.
type Snapshot struct {
	// Files maps each metadata file's path to its contents; an empty value
	// records that the file did not exist.
	Files map[string][]byte `json:"files"`
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

// LinkOptions controls Submitter.Link.
type LinkOptions struct {
	// Stack is the remote stack to add the pull requests to; 0 starts a new
	// one, which needs at least two pull requests.
	Stack int
	// Trunk is the base of the bottom pull request of a new stack.
	Trunk string
	// PRs are pull request numbers, bottom to top.
	PRs []int
}

// Submitter pushes the current stack and creates or updates its PRs.
type Submitter interface {
	Submit(ctx context.Context, repo git.Repo, o SubmitOptions) (SubmitResult, error)
	// Link joins pull requests that already exist into a stack on the
	// remote, without pushing any branch.
	Link(ctx context.Context, repo git.Repo, o LinkOptions) (SubmitResult, error)

	// Unstack dissolves the stack with the given number on the forge,
	// leaving its pull requests open and unstacked, so the next Submit
	// creates it again from the local order. A stack the forge no longer
	// has is not an error. The number must not be tracked locally any
	// more, or gh stack would remove the local record too.
	Unstack(ctx context.Context, repo git.Repo, number int) error
}
