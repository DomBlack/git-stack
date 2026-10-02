package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	if o.Continue || o.Abort {
		if err := a.continueOrAbort(ctx, repo, o.Continue, "git stack modify"); err != nil {
			return ModifyResult{}, err
		}
		branch, _ := a.d.Git.CurrentBranch(ctx, repo)
		return ModifyResult{Branch: branch}, nil
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
	if inProgress, err := a.d.Git.RebaseInProgress(ctx, repo); err != nil {
		return ModifyResult{}, err
	} else if inProgress {
		return ModifyResult{}, stack.New(stack.KindRebaseActive, "a rebase is in progress").
			WithSteps("resolve conflicts, `git add` them, then `git stack modify --continue`", "or `git stack modify --abort`")
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
		return ModifyResult{}, editorError(err, "pass -m <message> or --no-edit")
	}
	res.Commit = a.commitInfo(ctx, repo, sha)

	children := upstack(graph, current)
	if len(children) == 0 || a.d.Restack == nil {
		return res, nil
	}
	n := len(children)
	headline := fmt.Sprintf("Restacking %d %s above %s", n, pluralise(n, "branch", "branches"), res.Branch)
	err = a.progress(ctx, PhaseRestack, headline, func(ctx context.Context) error {
		return a.d.Restack.Restack(ctx, repo, stack.ScopeUpstack)
	})
	if err != nil {
		return res, withConflictSteps(err, "git stack modify")
	}
	res.Restacked = children
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

// continueOrAbort forwards --continue / --abort to the restacker.
func (a *App) continueOrAbort(ctx context.Context, repo git.Repo, cont bool, command string) error {
	if a.d.Restack == nil {
		return stack.New(stack.KindUnsupported, "no restack backend configured")
	}
	if cont {
		return withConflictSteps(a.d.Restack.Continue(ctx, repo), command)
	}
	return a.d.Restack.Abort(ctx, repo)
}

// withConflictSteps decorates a conflict error with the exact next steps
// for the given command (e.g. "git stack modify").
func withConflictSteps(err error, command string) error {
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindConflict {
		return err
	}
	if len(se.NextSteps) > 0 {
		return err
	}
	steps := []string{"resolve the conflicts"}
	if len(se.Files) > 0 {
		steps[0] = "resolve the conflicts in: " + strings.Join(se.Files, ", ")
		steps = append(steps, "git add "+strings.Join(se.Files, " "))
	} else {
		steps = append(steps, "git add <files>")
	}
	steps = append(steps, fmt.Sprintf("%s --continue", command), fmt.Sprintf("or give up with %s --abort", command))
	return se.WithSteps(steps...)
}

// pluralise picks the singular or plural word for n.
func pluralise(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
