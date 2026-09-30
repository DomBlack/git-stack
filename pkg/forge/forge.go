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
	Number    int       `json:"number"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	State     State     `json:"state"`
	Head      string    `json:"head"`
	Base      string    `json:"base"`
	UpdatedAt time.Time `json:"updatedAt"`
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

// Forge performs pull-request operations for the repository's remote.
type Forge interface {
	// ListPRs returns every pull request whose head is a branch of this
	// repository, in one call.
	ListPRs(ctx context.Context, repo git.Repo) ([]PullRequest, error)
	CreatePR(ctx context.Context, repo git.Repo, in CreatePR) (PullRequest, error)
	UpdatePR(ctx context.Context, repo git.Repo, number int, in UpdatePR) error
}
