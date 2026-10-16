package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/DomBlack/git-stack/pkg/ai"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// CreateOptions mirrors `gt create`.
type CreateOptions struct {
	// Name is the explicit branch name (used as-is, no prefix).
	Name string
	// Message paragraphs for the commit; empty opens the editor when
	// something is staged.
	Message []string
	Staging StagingMode
	// UseAI drafts the branch name (and the message when none is given).
	UseAI    bool
	NoVerify bool
	// Insert puts the branch directly above the current one, in the middle
	// of its stack, and restacks the branches that were above onto it.
	Insert bool
}

// CreateResult reports what happened.
type CreateResult struct {
	Branch string      `json:"branch"`
	Parent string      `json:"parent"`
	Commit *CommitInfo `json:"commit,omitempty"`
	// NewStack is true when the branch started a new stack on trunk.
	NewStack bool `json:"newStack"`
	// Inserted is true when the branch went in below other branches of the
	// stack; Restacked lists the ones moved onto it, bottom to top.
	Inserted  bool     `json:"inserted,omitempty"`
	Restacked []string `json:"restacked,omitempty"`
}

// recentSubjectsCount is how many trunk subjects feed the AI prompt.
const recentSubjectsCount = 10

// undoTimeout bounds the undo of a failed create, which runs even when the
// command's own context was cancelled.
const undoTimeout = 30 * time.Second

// Create stages, creates a branch on top of the current one, commits and
// registers the branch with the backend. With Insert on a branch that is
// not the top of its stack, the branch goes in right above the current one
// and the branches above are restacked onto it afterwards.
func (a *App) Create(ctx context.Context, repo git.Repo, o CreateOptions) (CreateResult, error) {
	if a.d.Tracker == nil {
		return CreateResult{}, stack.New(stack.KindUnsupported, "no stack backend configured")
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		if errors.Is(err, git.ErrDetached) {
			return CreateResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a branch first")
		}
		return CreateResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return CreateResult{}, err
	}

	var trunk string
	newStack, insert := false, false
	var snap *reshapeSnapshot
	switch s, i, ok := graph.StackOf(current); {
	case graph.IsTrunk(current) || a.isDefaultBranch(ctx, repo, graph, current):
		if o.Insert {
			return CreateResult{}, stack.Newf(stack.KindInvalidArgs, "%s is a trunk; --insert puts a branch in the middle of a stack", current).
				WithSteps("drop --insert to start a new stack here")
		}
		trunk, newStack = current, true
	case ok && i == len(s.Branches)-1:
		trunk = s.Trunk
	case ok && o.Insert:
		if err := a.noRebaseActive(ctx, repo); err != nil {
			return CreateResult{}, err
		}
		if err := noMergedBranches(s); err != nil {
			return CreateResult{}, err
		}
		trunk, insert = s.Trunk, true
		snap = snapshotOf(graph, []string{s.Bottom()})
	case ok:
		return CreateResult{}, stack.Newf(stack.KindNotAtTop, "%s is not the top of its stack (top is %s)", current, s.Top()).
			WithSteps("run `git stack top` and create the branch there",
				"or pass --insert to put it right above "+current,
				"gh stack can only add branches at the top of a stack")
	default:
		return CreateResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", current).
			WithSteps("check out your trunk (`git stack checkout --trunk`) and run `git stack create` to start a stack",
				fmt.Sprintf("or adopt this branch first: gh stack init %s", current))
	}

	staged, err := a.stage(ctx, repo, o.Staging)
	if err != nil {
		return CreateResult{}, err
	}

	name, message, err := a.resolveCreateName(ctx, repo, o, trunk, staged)
	if err != nil {
		return CreateResult{}, err
	}

	err = a.progress(ctx, PhaseCreate, fmt.Sprintf("Creating branch %s", name), func(ctx context.Context) error {
		switch {
		case newStack:
			return a.d.Tracker.InitStack(ctx, repo, trunk, []string{name})
		case insert:
			return a.insertBranch(ctx, repo, name, current)
		}
		return a.d.Tracker.AddTop(ctx, repo, name)
	})
	if err != nil {
		return CreateResult{}, err
	}
	res := CreateResult{Branch: name, Parent: current, NewStack: newStack, Inserted: insert}
	if !staged {
		// The new branch is the current one's tip, so the branches above
		// are already on it.
		return res, nil
	}
	sha, err := a.d.Git.Commit(ctx, repo, git.CommitOptions{Message: message, NoVerify: o.NoVerify})
	if err != nil {
		return CreateResult{}, a.undoCreate(ctx, repo, name, current, snap, commitError(err, "pass -m <message>"))
	}
	info := a.commitInfo(ctx, repo, sha)
	res.Commit = &info
	if !insert {
		return res, nil
	}
	// The branches above move onto the new commit like after a modify; a
	// conflict stops there, and abort puts them back, leaving the new
	// branch and its commit in the stack.
	n := len(upstack(graph, current))
	headline := fmt.Sprintf("Restacking %d %s above %s", n, pluralise(n, "branch", "branches"), name)
	rr, err := a.restack(ctx, repo, RestackOptions{Scope: stack.ScopeUpstack}, restackCommand, headline)
	res.Restacked = movedNames(rr)
	return res, err
}

// insertBranch creates name on parent, checks it out and records it in
// parent's stack right above it, which makes the stack's pull requests
// stale on GitHub: its remote id is cleared and queued for dissolving, as
// a reorder does. Nothing else moves yet; name is parent's tip.
func (a *App) insertBranch(ctx context.Context, repo git.Repo, name, parent string) error {
	if err := a.d.Git.CreateBranch(ctx, repo, name, parent); err != nil {
		return err
	}
	if err := a.d.Git.Switch(ctx, repo, name); err != nil {
		if derr := a.d.Git.DeleteBranch(ctx, repo, name); derr != nil {
			a.d.Log.Debug("undo insert", "branch", name, "err", derr)
		}
		return err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return err
	}
	s, _, ok := graph.StackOf(parent)
	if !ok {
		return stack.Newf(stack.KindNotInStack, "%s is not in a stack", parent)
	}
	bottom := s.Bottom()
	e := reshapeEdit{
		touched: []string{bottom},
		relink:  []string{bottom},
		edit: func(g *stack.Graph) error {
			s, i, ok := g.StackOf(parent)
			if !ok {
				return stack.Newf(stack.KindInvalidArgs, "%s is no longer in a stack", parent)
			}
			s.Branches = slices.Insert(s.Branches, i+1, stack.Branch{Name: name})
			return nil
		},
	}
	if _, err := a.reshape(ctx, repo, createCommand, "", e); err != nil {
		if uerr := errors.Join(a.d.Git.Switch(ctx, repo, parent), a.d.Git.DeleteBranch(ctx, repo, name)); uerr != nil {
			a.d.Log.Debug("undo insert", "branch", name, "err", uerr)
		}
		return err
	}
	return nil
}

const createCommand = "git stack create"

// undoCreate makes a failed create all or nothing: the branch was already
// created, checked out and registered before the commit failed (a locked
// signing key, a hook saying no), so forget it, go back to the parent and
// delete it. snap, when the branch was inserted, is the stack's shape from
// before, put back first. The staged changes survive, both branches point
// at the same commit. The error says to run create again, or, when undoing
// failed too, what was left behind and how to commit on it instead.
func (a *App) undoCreate(ctx context.Context, repo git.Repo, name, parent string, snap *reshapeSnapshot, cause error) error {
	err := asStackError(cause)
	// The commit may have failed because ctx was cancelled; the undo still
	// has to run, so give it its own bounded context.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), undoTimeout)
	defer cancel()
	// Commit can fail after git has actually committed (reading HEAD back
	// under a cancelled context, say). Then the branch holds the commit
	// and must stay: deleting it would orphan the work.
	if tip, ok, terr := a.d.Git.Tip(ctx, repo, name); terr == nil && ok {
		if parentTip, _, perr := a.d.Git.Tip(ctx, repo, parent); perr == nil && tip != parentTip {
			err = err.WithSteps(fmt.Sprintf("%s has a commit after all, so it was kept and is tracked; check `git stack log`", name))
			if snap != nil {
				err = err.WithSteps("git stack restack moves the branches above onto it")
			}
			return err
		}
	}
	undo := func() error {
		if snap != nil {
			if err := a.restoreReshape(ctx, repo, snap); err != nil {
				return fmt.Errorf("put the stack back: %w", err)
			}
		}
		if err := a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
			for i := range g.Stacks {
				g.Stacks[i].Branches = slices.DeleteFunc(g.Stacks[i].Branches, func(b stack.Branch) bool { return b.Name == name })
			}
			return nil
		}); err != nil {
			return fmt.Errorf("forget %s: %w", name, err)
		}
		if err := a.d.Git.Switch(ctx, repo, parent); err != nil {
			return err
		}
		return a.d.Git.DeleteBranch(ctx, repo, name)
	}
	if undoErr := undo(); undoErr != nil {
		a.d.Log.Debug("undo create", "branch", name, "err", undoErr)
		return err.WithSteps(
			fmt.Sprintf("%s was left behind with no commit of its own (undoing it failed: %v)", name, undoErr),
			"check `git stack log`; if it is still tracked, commit on it with `git stack modify -c -m <message>`, otherwise delete it")
	}
	return err.WithSteps("nothing was created; run `git stack create` again once that is sorted")
}

// asStackError returns err as a *stack.Error, wrapping anything else as
// KindUnknown so next steps can be attached.
func asStackError(err error) *stack.Error {
	if se, ok := errors.AsType[*stack.Error](err); ok {
		return se
	}
	return stack.New(stack.KindUnknown, err.Error()).WithCause(err)
}

// isDefaultBranch reports whether name is the repository's default branch
// while no stack has declared a trunk yet (the first stack starts here).
func (a *App) isDefaultBranch(ctx context.Context, repo git.Repo, graph *stack.Graph, name string) bool {
	if len(graph.Trunks) > 0 {
		return false
	}
	def, ok, err := a.d.Git.DefaultBranch(ctx, repo)
	return err == nil && ok && def == name
}

// resolveCreateName decides the branch name and commit message.
func (a *App) resolveCreateName(ctx context.Context, repo git.Repo, o CreateOptions, trunk string, staged bool) (name string, message []string, err error) {
	message = o.Message
	if o.Name != "" {
		if err := a.validateBranchName(ctx, o.Name); err != nil {
			return "", nil, err
		}
		exists, err := a.d.Git.BranchExists(ctx, repo, o.Name)
		if err != nil {
			return "", nil, err
		}
		if exists {
			return "", nil, stack.Newf(stack.KindInvalidArgs, "branch %s already exists", o.Name)
		}
		return o.Name, message, nil
	}
	if o.UseAI {
		if !staged {
			return "", nil, stack.New(stack.KindInvalidArgs, "--ai needs staged changes to describe").
				WithSteps("stage changes (or use -a / -u), or pass a branch name")
		}
		draft, err := a.draftCommit(ctx, repo, trunk, strings.Join(o.Message, "\n\n"))
		if err != nil {
			return "", nil, err
		}
		name, err := a.derivedBranchName(ctx, repo, Slug(draft.BranchName))
		if err != nil {
			return "", nil, err
		}
		if len(message) == 0 {
			message = []string{draft.Message}
		}
		return name, message, nil
	}
	if len(o.Message) > 0 {
		subject, _, _ := strings.Cut(o.Message[0], "\n")
		name, err := a.derivedBranchName(ctx, repo, Slug(subject))
		return name, message, err
	}
	return "", nil, stack.New(stack.KindInvalidArgs, "no branch name given").
		WithSteps("git stack create <name>", "git stack create -m <message>  (derives the name)", "git stack create --ai")
}

// draftCommit gathers the AI context and calls the Drafter, showing progress
// when a Progress hook is configured.
func (a *App) draftCommit(ctx context.Context, repo git.Repo, trunk, fixedMessage string) (ai.Commit, error) {
	if a.d.AI == nil {
		return ai.Commit{}, stack.New(stack.KindUnsupported, "no AI drafter configured").
			WithSteps("pass a branch name or -m instead of --ai")
	}
	diff, err := a.d.Git.StagedDiff(ctx, repo)
	if err != nil {
		return ai.Commit{}, err
	}
	diff, truncated := ai.TruncateDiff(diff, ai.MaxDiffBytes)
	subjects, err := a.d.Git.Subjects(ctx, repo, trunk, recentSubjectsCount)
	if err != nil {
		a.d.Log.Debug("recent subjects", "err", err)
	}
	branches, err := a.d.Git.Branches(ctx, repo)
	if err != nil {
		return ai.Commit{}, err
	}
	taken := make([]string, 0, len(branches))
	for _, b := range branches {
		taken = append(taken, b.Name)
	}
	in := ai.CommitInput{
		Diff: diff, DiffTruncated: truncated, Message: fixedMessage,
		RecentSubjects: subjects, BranchPrefix: a.d.Config.BranchPrefix,
		TakenBranches: taken, ExtraPrompt: a.d.Config.AIExtraPrompt,
	}
	var out ai.Commit
	err = a.progress(ctx, PhaseAI, "Drafting branch name and commit message with Claude", func(ctx context.Context) error {
		var err error
		out, err = a.d.AI.DraftCommit(ctx, in)
		return err
	})
	return out, err
}

// progress runs fn under the Progress hook when one is configured.
func (a *App) progress(ctx context.Context, phase Phase, message string, fn func(ctx context.Context) error) error {
	if a.d.Progress == nil {
		return fn(ctx)
	}
	return a.d.Progress(ctx, phase, message, fn)
}
