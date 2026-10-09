package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// ModifyOptions mirrors `gt modify`.
type ModifyOptions struct {
	// NewCommit creates a commit instead of amending (-c).
	NewCommit bool
	Staging   StagingMode
	Message   []string
	// Edit opens the editor when amending (-e); ignored with NewCommit.
	Edit        bool
	ResetAuthor bool
	NoVerify    bool
	// Continue / Abort resume or abandon an interrupted restack.
	Continue bool
	Abort    bool
}

// ModifyResult reports what happened.
type ModifyResult struct {
	Branch string     `json:"branch"`
	Commit CommitInfo `json:"commit"`
	// Amended is false when a new commit was created.
	Amended bool `json:"amended"`
	// ForcedNewCommit is true when the branch had no commits of its own, so
	// amending would have rewritten the parent's commit.
	ForcedNewCommit bool `json:"forcedNewCommit"`
	// Restacked lists the branches above that were rebased.
	Restacked []string `json:"restacked"`
}

// Modify amends (or adds to) the current branch and restacks its
// descendants.
func (a *App) Modify(ctx context.Context, repo git.Repo, o ModifyOptions) (ModifyResult, error) {
	switch {
	case o.Continue:
		res, err := a.restackContinue(ctx, repo, false)
		return ModifyResult{Branch: branchAfter(ctx, a, repo), Restacked: movedNames(res)}, err
	case o.Abort:
		_, err := a.restackAbort(ctx, repo)
		return ModifyResult{Branch: branchAfter(ctx, a, repo)}, err
	}
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return ModifyResult{}, err
	}

	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		if errors.Is(err, git.ErrDetached) {
			return ModifyResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch first")
		}
		return ModifyResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return ModifyResult{}, err
	}
	if graph.IsTrunk(current) {
		return ModifyResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; modify works on stacked branches", current)
	}
	parent, ok := graph.Parent(current)
	if !ok {
		return ModifyResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", current).
			WithSteps("use plain git commit --amend, or adopt the branch: gh stack init " + current)
	}

	staged, err := a.stage(ctx, repo, o.Staging)
	if err != nil {
		return ModifyResult{}, err
	}
	if !staged && !o.Edit && len(o.Message) == 0 {
		return ModifyResult{}, stack.New(stack.KindInvalidArgs, "nothing to modify: no staged changes").
			WithSteps("stage changes (or use -a / -u)", "or -e to edit the commit message, -m to replace it")
	}

	own, err := a.d.Git.CountCommits(ctx, repo, parent, "HEAD")
	if err != nil {
		return ModifyResult{}, err
	}
	res := ModifyResult{Branch: current}
	amend := !o.NewCommit
	if amend && own == 0 {
		amend = false
		res.ForcedNewCommit = true
		if !staged {
			return ModifyResult{}, stack.Newf(stack.KindInvalidArgs, "%s has no commits of its own to amend", current).
				WithSteps("stage changes to create its first commit")
		}
	}
	res.Amended = amend

	co := git.CommitOptions{Message: o.Message, Amend: amend, NoVerify: o.NoVerify, ResetAuthor: o.ResetAuthor && amend}
	if amend {
		co.Edit = o.Edit
		co.NoEdit = !o.Edit && len(o.Message) == 0
	}
	sha, err := a.d.Git.Commit(ctx, repo, co)
	if err != nil {
		err = commitError(err, "pass -m <message> or --no-edit")
		if errors.Is(err, &stack.Error{Kind: stack.KindSigningFailed}) {
			err = asStackError(err).WithSteps("then run `git stack modify` again; the branch is untouched")
		}
		return ModifyResult{}, err
	}
	res.Commit = a.commitInfo(ctx, repo, sha)

	children := upstack(graph, current)
	if len(children) == 0 {
		return res, nil
	}
	n := len(children)
	headline := fmt.Sprintf("Restacking %d %s above %s", n, pluralise(n, "branch", "branches"), res.Branch)
	rr, err := a.restack(ctx, repo, RestackOptions{Scope: stack.ScopeUpstack}, "git stack modify", headline)
	if err != nil {
		return res, err
	}
	res.Restacked = movedNames(rr)
	return res, nil
}

// upstack lists every branch above name in its stack.
func upstack(graph *stack.Graph, name string) []string {
	s, i, ok := graph.StackOf(name)
	if !ok || i+1 >= len(s.Branches) {
		return nil
	}
	return s.Names()[i+1:]
}

// movedNames lists the branches a restack moved.
func movedNames(res RestackResult) []string {
	out := make([]string, 0, len(res.Moved))
	for _, m := range res.Moved {
		out = append(out, m.Name)
	}
	return out
}

// branchAfter is the current branch, or "" when HEAD is detached.
func branchAfter(ctx context.Context, a *App, repo git.Repo) string {
	b, _ := a.d.Git.CurrentBranch(ctx, repo)
	return b
}

// pluralise picks the singular or plural word for n.
func pluralise(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
