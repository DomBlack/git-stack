// Package forge is the port for pull-request operations. It is vendor
// neutral: GitHub via gh, GitHub via the REST API and GitLab are adapters.
package forge

import (
	"context"
	"time"

	"github.com/DomBlack/git-stack/pkg/git"
)

// State of a pull request.
type State string

const (
	StateOpen   State = "open"
	StateDraft  State = "draft"
	StateMerged State = "merged"
	StateClosed State = "closed"
	// StateUnknown is used when only a number is known (e.g. from a backend
	// snapshot) and the forge has not been consulted.
	StateUnknown State = ""
)

// PullRequest is a forge-neutral pull request.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	State  State  `json:"state"`
	Head   string `json:"head"`
	Base   string `json:"base"`
	// HeadSHA is the head commit the forge last saw for this PR.
	HeadSHA string `json:"headSha,omitempty"`
	// MergeCommit is the commit the PR was merged as, "" unless merged.
	MergeCommit string    `json:"mergeCommit,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreatePR describes a new pull request.
type CreatePR struct {
	Head, Base  string
	Title, Body string
	Draft       bool
}

// UpdatePR carries optional changes; nil fields are left untouched.
type UpdatePR struct {
	Title *string
	Body  *string
	Base  *string
	// Ready marks a draft ready for review when true.
	Ready *bool
}

// MergeMethod says how a pull request lands on its base.
type MergeMethod string

const (
	// MergeDefault leaves the choice to the repository's settings.
	MergeDefault MergeMethod = ""
	MergeMerge   MergeMethod = "merge"
	MergeSquash  MergeMethod = "squash"
	MergeRebase  MergeMethod = "rebase"
)

// MergeStatus is where a stack merge ended up.
type MergeStatus string

const (
	// MergeMerged: the pull requests are on the base branch.
	MergeMerged MergeStatus = "merged"
	// MergeEnqueued: the base branch uses a merge queue and the pull
	// requests are in it; they land when the queue gets to them.
	MergeEnqueued MergeStatus = "enqueued"
)

// MergeOutcome reports a finished stack merge.
type MergeOutcome struct {
	Status MergeStatus
	// SHA is the merge commit on the base branch; empty when enqueued.
	SHA string
	// Message is the forge's own description of the outcome, if any.
	Message string
}

// CheckSummary is where the checks on a pull request's head commit stand,
// by name. Checks that passed, or were skipped or neutral, aren't listed.
type CheckSummary struct {
	// Failing checks finished without passing (failed, errored, timed
	// out, cancelled, needing action).
	Failing []string `json:"failing,omitempty"`
	// Pending checks are queued, waiting or still running.
	Pending []string `json:"pending,omitempty"`
}

// Clean reports whether nothing is failing or still running.
func (c CheckSummary) Clean() bool { return len(c.Failing) == 0 && len(c.Pending) == 0 }

// Forge performs pull-request operations for the repository's remote.
type Forge interface {
	// ListPRs returns every pull request whose head is a branch of this
	// repository, in one call.
	ListPRs(ctx context.Context, repo git.Repo) ([]PullRequest, error)
	CreatePR(ctx context.Context, repo git.Repo, in CreatePR) (PullRequest, error)
	UpdatePR(ctx context.Context, repo git.Repo, number int, in UpdatePR) error
	// MergeStack merges pull request number together with every open pull
	// request below it in its stack, all or nothing, and waits until the
	// forge reports the result (or has queued it). A refusal, such as a
	// draft in the way or a failed check, comes back as a *stack.Error.
	MergeStack(ctx context.Context, repo git.Repo, number int, method MergeMethod) (MergeOutcome, error)
	// Checks reports the checks on the current head commit of each pull
	// request in numbers, asking the forge about all of them together
	// rather than one at a time. A pull request with no checks at all may
	// be missing from the map.
	Checks(ctx context.Context, repo git.Repo, numbers []int) (map[int]CheckSummary, error)
	// PullRequestURL is the web address of pull request number, worked out
	// from local configuration only (never the network), so output can
	// link a PR the forge hasn't been asked about. "" when it can't tell.
	PullRequestURL(ctx context.Context, repo git.Repo, number int) (string, error)
}
