package stack

import (
	"fmt"
	"strings"
)

// Kind classifies an Error so that the CLI can pick an exit code and message
// style and the MCP server can return a machine-readable code.
type Kind int

const (
	KindUnknown Kind = iota
	// KindNotRepo: the directory is not inside a git repository.
	KindNotRepo
	// KindNotInStack: the current branch is not tracked in any stack.
	KindNotInStack
	// KindConflict: a rebase stopped on conflicts; Files and Branch are set.
	KindConflict
	// KindAPIFailure: the forge API call failed.
	KindAPIFailure
	// KindInvalidArgs: the request is invalid for the current state.
	KindInvalidArgs
	// KindDisambiguate: the branch belongs to several stacks.
	KindDisambiguate
	// KindRebaseActive: a git rebase is already in progress.
	KindRebaseActive
	// KindLocked: the backend's stack metadata is locked or stale.
	KindLocked
	// KindStacksUnavailable: stacked PRs are not available for the repository.
	KindStacksUnavailable
	// KindModifyRecovery: the backend has an interrupted modify to recover.
	KindModifyRecovery
	// KindUnsupported: the operation is not possible with the current backend.
	KindUnsupported
	// KindNotInstalled: a required tool is missing.
	KindNotInstalled
	// KindAuthRequired: the user must log in.
	KindAuthRequired
	// KindNotAtTop: the operation needs the top of the stack.
	KindNotAtTop
	// KindInteractionRequired: a prompt, editor or TUI is needed but not available.
	KindInteractionRequired
	// KindPartial: the command did everything it could, but some of it
	// could not be done (the result says what); running it again after
	// following the next steps finishes the job.
	KindPartial
	// KindChecksFailing: a merge was refused because checks failed on
	// pull requests it would land; Checks says which.
	KindChecksFailing
	// KindChecksPending: a merge was refused because checks are still
	// running on pull requests it would land; Checks says which.
	KindChecksPending
	// KindSigningFailed: git could not sign a commit (a locked SSH key, a
	// gpg agent that can't prompt); nothing was committed.
	KindSigningFailed
)

var kindCodes = map[Kind]string{
	KindUnknown:             "unknown",
	KindNotRepo:             "not_repo",
	KindNotInStack:          "not_in_stack",
	KindConflict:            "conflict",
	KindAPIFailure:          "api_failure",
	KindInvalidArgs:         "invalid_args",
	KindDisambiguate:        "disambiguate",
	KindRebaseActive:        "rebase_active",
	KindLocked:              "locked",
	KindStacksUnavailable:   "stacks_unavailable",
	KindModifyRecovery:      "modify_recovery",
	KindUnsupported:         "unsupported",
	KindNotInstalled:        "not_installed",
	KindAuthRequired:        "auth_required",
	KindNotAtTop:            "not_at_top",
	KindInteractionRequired: "interaction_required",
	KindPartial:             "partial",
	KindChecksFailing:       "checks_failing",
	KindChecksPending:       "checks_pending",
	KindSigningFailed:       "signing_failed",
}

// Code is the stable machine-readable name of the kind.
func (k Kind) Code() string {
	if c, ok := kindCodes[k]; ok {
		return c
	}
	return fmt.Sprintf("kind_%d", int(k))
}

func (k Kind) String() string { return k.Code() }

// Error is the user-facing error type shared by every port and use case.
type Error struct {
	Kind Kind
	// Msg is a one-line description.
	Msg string
	// NextSteps are concrete actions, rendered as a list.
	NextSteps []string
	// Files lists conflicted paths for KindConflict.
	Files []string
	// Branch names the branch involved (e.g. the one being rebased).
	Branch string
	// Checks lists the pull requests whose checks stopped a merge, for
	// KindChecksFailing and KindChecksPending.
	Checks []PRChecks
	// Detail carries captured diagnostic output (stderr tail); never stdout.
	Detail string
	// Cause is the underlying error, if any.
	Cause error
}

// PRChecks names the checks holding up one pull request.
type PRChecks struct {
	Number int    `json:"number"`
	Branch string `json:"branch"`
	// Failing checks finished without passing.
	Failing []string `json:"failing,omitempty"`
	// Pending checks are queued or still running.
	Pending []string `json:"pending,omitempty"`
}

// New creates an Error.
func New(kind Kind, msg string) *Error {
	return &Error{Kind: kind, Msg: msg}
}

// Newf creates an Error with a formatted message.
func Newf(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// WithSteps appends next steps.
func (e *Error) WithSteps(steps ...string) *Error {
	e.NextSteps = append(e.NextSteps, steps...)
	return e
}

// WithCause records the underlying error.
func (e *Error) WithCause(err error) *Error {
	e.Cause = err
	return e
}

// WithDetail records diagnostic output.
func (e *Error) WithDetail(detail string) *Error {
	e.Detail = strings.TrimSpace(detail)
	return e
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Cause }

// Is lets errors.Is match on kind: errors.Is(err, &stack.Error{Kind: stack.KindConflict}).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind && (t.Msg == "" || t.Msg == e.Msg)
}
