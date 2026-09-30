package app

import (
	"context"
	"errors"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// RestackOptions mirrors `gt restack`.
type RestackOptions struct {
	Scope    stack.Scope
	Continue bool
	Abort    bool
}

// RestackResult reports what was rebased.
type RestackResult struct {
	Scope stack.Scope `json:"scope"`
	// Branches that the scope covered, bottom to top.
	Branches []string `json:"branches"`
	// BottomBehindTrunk is true when the stack's bottom branch does not
	// contain the local trunk tip: the local-only restack never rebases
	// onto trunk, so `git stack sync` is needed for that.
	BottomBehindTrunk bool   `json:"bottomBehindTrunk"`
	Trunk             string `json:"trunk"`
	Bottom            string `json:"bottom"`
}

// Restack rebases the current stack locally (no fetch).
func (a *App) Restack(ctx context.Context, repo git.Repo, o RestackOptions) (RestackResult, error) {
	if a.d.Restack == nil {
		return RestackResult{}, stack.New(stack.KindUnsupported, "no restack backend configured")
	}
	if o.Continue || o.Abort {
		return RestackResult{Scope: o.Scope}, a.continueOrAbort(ctx, repo, o.Continue, "git stack restack")
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		if errors.Is(err, git.ErrDetached) {
			return RestackResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch first")
		}
		return RestackResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return RestackResult{}, err
	}
	s, i, ok := graph.StackOf(current)
	if !ok {
		if graph.IsTrunk(current) {
			return RestackResult{}, stack.Newf(stack.KindNotInStack, "%s is a trunk; check out a stacked branch to restack it", current)
		}
		return RestackResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", current)
	}
	res := RestackResult{Scope: o.Scope, Trunk: s.Trunk, Bottom: s.Bottom()}
	names := s.Names()
	switch o.Scope {
	case stack.ScopeUpstack:
		res.Branches = names[i:]
	case stack.ScopeDownstack:
		res.Branches = names[:i+1]
	case stack.ScopeOnly:
		res.Branches = names[i : i+1]
	default:
		res.Branches = names
	}
	if err := a.d.Restack.Restack(ctx, repo, o.Scope); err != nil {
		return res, withConflictSteps(err, "git stack restack")
	}
	behind, err := a.d.Git.IsAncestor(ctx, repo, s.Trunk, s.Bottom())
	if err == nil {
		res.BottomBehindTrunk = !behind
	}
	return res, nil
}
