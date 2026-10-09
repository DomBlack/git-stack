package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
}

// CreateResult reports what happened.
type CreateResult struct {
	Branch string      `json:"branch"`
	Parent string      `json:"parent"`
	Commit *CommitInfo `json:"commit,omitempty"`
	// NewStack is true when the branch started a new stack on trunk.
	NewStack bool `json:"newStack"`
}

// recentSubjectsCount is how many trunk subjects feed the AI prompt.
const recentSubjectsCount = 10

// Create stages, creates a branch on top of the current one, commits and
// registers the branch with the backend.
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
	newStack := false
	switch s, i, ok := graph.StackOf(current); {
	case graph.IsTrunk(current) || a.isDefaultBranch(ctx, repo, graph, current):
		trunk, newStack = current, true
	case ok && i == len(s.Branches)-1:
		trunk = s.Trunk
	case ok:
		return CreateResult{}, stack.Newf(stack.KindNotAtTop, "%s is not the top of its stack (top is %s)", current, s.Top()).
			WithSteps("run `git stack top` and create the branch there",
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
		if newStack {
			return a.d.Tracker.InitStack(ctx, repo, trunk, []string{name})
		}
		return a.d.Tracker.AddTop(ctx, repo, name)
	})
	if err != nil {
		return CreateResult{}, err
	}
	res := CreateResult{Branch: name, Parent: current, NewStack: newStack}
	if !staged {
		return res, nil
	}
	sha, err := a.d.Git.Commit(ctx, repo, git.CommitOptions{Message: message, NoVerify: o.NoVerify})
	if err != nil {
		return res, commitError(err, "pass -m <message> (the branch was created; commit with git commit)")
	}
	info := a.commitInfo(ctx, repo, sha)
	res.Commit = &info
	return res, nil
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
