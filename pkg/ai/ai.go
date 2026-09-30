// Package ai is the port for drafting commit messages, branch names and
// pull-request text. Adapters (Claude Code today, a direct API tomorrow)
// implement Drafter; prompts and output schemas live here so every adapter
// behaves the same.
package ai

import "context"

// CommitInput is the context for drafting a branch name and commit message.
// The caller computes everything; the model never runs git.
type CommitInput struct {
	// Diff is the staged diff, possibly truncated.
	Diff          string
	DiffTruncated bool
	// Message, when set, is used as-is and only a branch name is drafted.
	Message string
	// RecentSubjects are the last trunk commit subjects, for house style.
	RecentSubjects []string
	// BranchPrefix is prepended to the generated name by the caller; the
	// model is told about it so it does not repeat it.
	BranchPrefix string
	// TakenBranches lists existing branch names to avoid.
	TakenBranches []string
	// ExtraPrompt is appended verbatim (git config stack.ai.extraPrompt).
	ExtraPrompt string
}

// Commit is a drafted branch name (without prefix) and commit message.
type Commit struct {
	BranchName string `json:"branch"`
	Message    string `json:"message"`
}

// PRInput is the context for drafting a pull request title and body.
type PRInput struct {
	Branch, Parent string
	Diff           string
	DiffTruncated  bool
	// Commits are the full commit messages on the branch, oldest first.
	Commits        []string
	RecentSubjects []string
	// Template is the repository's PR template, if any.
	Template    string
	ExtraPrompt string
}

// PullRequest is a drafted title and body.
type PullRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Drafter generates text. Implementations must be safe to call from any
// goroutine and must honour ctx.
type Drafter interface {
	DraftCommit(ctx context.Context, in CommitInput) (Commit, error)
	DraftPR(ctx context.Context, in PRInput) (PullRequest, error)
}
