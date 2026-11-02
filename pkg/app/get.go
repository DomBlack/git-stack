package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// GetOptions names what to pull down.
type GetOptions struct {
	// Target is a pull request number (123 or #123), a pull request URL or
	// a branch name.
	Target string
}

// Where Get found its target.
const (
	// GetLocal: the stack was already tracked here; it was only brought up
	// to date with the remote.
	GetLocal = "local"
	// GetStack: the stack was pulled down from GitHub's stacks.
	GetStack = "stack"
	// GetPRs: the pull request is in no stack on GitHub, so it and the
	// open pull requests it is based on were tracked as a new stack.
	GetPRs = "prs"
)

// GetResult reports what Get did.
type GetResult struct {
	// Branch is the branch now checked out.
	Branch string `json:"branch"`
	Trunk  string `json:"trunk"`
	// Branches is the stack, bottom to top.
	Branches []string `json:"branches"`
	Source   string   `json:"source"`
	// Created lists the branches that only existed on the remote before.
	Created []string `json:"created,omitempty"`
	// Updated lists branches fast forwarded to the remote.
	Updated []BranchMove `json:"updated,omitempty"`
	// Behind lists branches that have diverged from the remote, with the
	// number of commits there that aren't here; they were left alone.
	Behind []RemoteAhead `json:"behind,omitempty"`
	// NotUpdated lists branches the remote is ahead of that couldn't be
	// moved.
	NotUpdated []NotUpdated `json:"notUpdated,omitempty"`
	// NeedsRestack lists branches not on the tip of the one below them.
	NeedsRestack []string `json:"needsRestack,omitempty"`
	Notices      []string `json:"notices,omitempty"`
}

// rePRURL matches a pull request's web address and captures its number.
var rePRURL = regexp.MustCompile(`^https?://[^/]+/[^/]+/[^/]+/pull/(\d+)(?:[/?#].*)?$`)

// parseGetTarget splits a target into a pull request number or a branch
// name; exactly one is set.
func parseGetTarget(s string) (int, string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, "", stack.New(stack.KindInvalidArgs, "name a pull request number, URL or branch to get")
	}
	if m := rePRURL.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "#")); err == nil {
		if n <= 0 {
			return 0, "", stack.Newf(stack.KindInvalidArgs, "%s is not a pull request number", s)
		}
		return n, "", nil
	}
	return 0, s, nil
}

// Get pulls a pull request's stack down from the remote and checks out its
// branch: every branch below it in the stack (and above it, when the stack
// is on GitHub) is created locally if it isn't already, tracked, and fast
// forwarded where the remote is ahead. A stack tracked here already is only
// brought up to date. Nothing is rebased or pushed.
func (a *App) Get(ctx context.Context, repo git.Repo, o GetOptions) (GetResult, error) {
	return undoableResult(a, ctx, repo, "git stack get", undoKeep, func(ctx context.Context) (GetResult, error) {
		return a.get(ctx, repo, o)
	})
}

// get is Get's body.
func (a *App) get(ctx context.Context, repo git.Repo, o GetOptions) (GetResult, error) {
	var res GetResult
	number, branch, err := parseGetTarget(o.Target)
	if err != nil {
		return res, err
	}
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return res, err
	}
	before, err := a.localBranches(ctx, repo)
	if err != nil {
		return res, err
	}
	st, err := a.gatherSync(ctx, repo)
	if err != nil {
		return res, err
	}
	err = a.progress(ctx, PhaseSync, "Fetching "+st.remote, func(ctx context.Context) error {
		return a.d.Git.Fetch(ctx, repo, st.remote)
	})
	if err != nil {
		return res, stack.Newf(stack.KindAPIFailure, "could not fetch from %s", st.remote).
			WithDetail(err.Error()).WithCause(err).
			WithSteps("check the remote and your network, then run git stack get again")
	}

	target := a.trackedTarget(repo, st.graph, number, branch)
	if target != "" {
		res.Source = GetLocal
	} else {
		if target, err = a.importTarget(ctx, repo, number, branch); err != nil {
			return res, err
		}
		if target != "" {
			res.Source = GetStack
		} else {
			res.Source = GetPRs
			if target, err = a.getPRs(ctx, st, number, branch, &res); err != nil {
				return res, err
			}
		}
	}

	// Bring the stack up to date with the remote, the way sync does.
	if st.graph, err = a.d.Meta.Load(ctx, repo); err != nil {
		return res, err
	}
	s, _, ok := st.graph.StackOf(target)
	if !ok {
		return res, fmt.Errorf("%s is not in a stack after getting it", target)
	}
	only := *s
	st.graph = stack.NewGraph([]stack.Stack{only})
	if err := a.loadCheckouts(ctx, st); err != nil {
		return res, err
	}
	var sres SyncResult
	if err := a.syncRemote(ctx, st, &sres); err != nil {
		return res, err
	}
	res.Updated, res.Behind, res.NotUpdated = sres.Updated, sres.Behind, sres.NotUpdated
	res.Notices = append(res.Notices, sres.Notices...)

	if err := a.Checkout(ctx, repo, target); err != nil {
		return res, err
	}
	res.Branch, res.Trunk, res.Branches = target, only.Trunk, only.Names()
	for _, name := range append([]string{only.Trunk}, res.Branches...) {
		if _, ok := before[name]; !ok {
			res.Created = append(res.Created, name)
		}
	}

	var parts, steps []string
	for _, n := range res.NotUpdated {
		if n.Failed() {
			part, step := n.explain()
			parts = append(parts, part)
			steps = append(steps, step...)
		}
	}
	if len(parts) > 0 {
		return res, stack.Newf(stack.KindPartial, "%s; everything else is up to date", strings.Join(parts, "; ")).WithSteps(steps...)
	}
	return res, nil
}

// trackedTarget is the branch the target names when it is already in a
// stack here, else "".
func (a *App) trackedTarget(repo git.Repo, graph *stack.Graph, number int, branch string) string {
	if number == 0 {
		if _, _, ok := graph.StackOf(branch); ok {
			return branch
		}
		return ""
	}
	for _, s := range graph.Stacks {
		for _, b := range s.Branches {
			if b.PR != nil && b.PR.Number == number {
				return b.Name
			}
		}
	}
	// A stack git-stack made itself has no PR records until gh stack
	// submits it, so fall back to what the forge last said.
	prs, _, _ := a.CachedPRs(repo)
	for _, pr := range prs {
		if pr.Number == number && !pr.Fork {
			if _, _, ok := graph.StackOf(pr.Head); ok {
				return pr.Head
			}
		}
	}
	return ""
}

// importTarget pulls the target's stack down from GitHub and returns the
// branch it checked out, or "" when the target is in no stack there (or
// the repository doesn't have stacks), for getPRs to handle.
func (a *App) importTarget(ctx context.Context, repo git.Repo, number int, branch string) (string, error) {
	if a.d.Tracker == nil {
		return "", nil
	}
	arg := branch
	if number > 0 {
		arg = strconv.Itoa(number)
	}
	err := a.progress(ctx, PhaseSync, "Looking for "+getLabel(number, branch)+" in GitHub's stacks", func(ctx context.Context) error {
		return a.d.Tracker.ImportStack(ctx, repo, arg)
	})
	if se, ok := errors.AsType[*stack.Error](err); ok && (se.Kind == stack.KindNotInStack || se.Kind == stack.KindStacksUnavailable) {
		a.d.Log.Debug("not in a stack on GitHub", "target", arg, "err", err)
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return a.d.Git.CurrentBranch(ctx, repo)
}

// getPRs tracks a pull request that is in no stack on GitHub, together
// with the open pull requests it is based on, as a new stack on the first
// base that has none. It returns the pull request's branch.
func (a *App) getPRs(ctx context.Context, st *syncState, number int, branch string, res *GetResult) (string, error) {
	if a.d.Forge == nil {
		return "", ErrNoForge
	}
	repo := st.repo
	var byHead map[string]forge.PullRequest
	heads := func() error {
		if byHead != nil {
			return nil
		}
		prs, err := a.RefreshPRs(ctx, repo)
		if err != nil {
			return err
		}
		byHead = PRsFor(prs)
		return nil
	}

	var top forge.PullRequest
	err := a.progress(ctx, PhaseRefresh, "Looking up "+getLabel(number, branch), func(ctx context.Context) error {
		if number > 0 {
			var err error
			top, err = a.d.Forge.PR(ctx, repo, number)
			return err
		}
		if err := heads(); err != nil {
			return err
		}
		pr, ok := byHead[branch]
		if !ok || !prOpen(pr) {
			return stack.Newf(stack.KindInvalidArgs, "%s has no open pull request and isn't in a stack on GitHub", branch).
				WithSteps("check the name, or pass the pull request's number or URL")
		}
		top = pr
		return nil
	})
	if err != nil {
		return "", err
	}
	if top.Fork {
		return "", stack.Newf(stack.KindUnsupported, "#%d comes from a fork; git stack get only pulls branches from this repository", top.Number).
			WithSteps(fmt.Sprintf("gh pr checkout %d checks it out without tracking it", top.Number))
	}
	if !prOpen(top) {
		return "", stack.Newf(stack.KindInvalidArgs, "#%d is %s, so there's nothing to get", top.Number, top.State)
	}

	// Walk down through the open pull requests it is based on.
	chain := []forge.PullRequest{top}
	for len(chain) < 100 {
		base := chain[0].Base
		if st.graph.IsTrunk(base) || slices.Contains(st.trunks, base) {
			break
		}
		if s, _, ok := st.graph.StackOf(base); ok {
			return "", stack.Newf(stack.KindUnsupported, "#%d is based on %s, which is already in the stack starting at %s", chain[0].Number, base, s.Bottom()).
				WithSteps("git stack get only adds pull requests as a stack of their own; ask for the pull request to be added to that stack on GitHub")
		}
		if err := heads(); err != nil {
			return "", err
		}
		pr, ok := byHead[base]
		if !ok || !prOpen(pr) || slices.ContainsFunc(chain, func(c forge.PullRequest) bool { return c.Head == pr.Head }) {
			break
		}
		chain = slices.Insert(chain, 0, pr)
	}

	trunk := chain[0].Base
	names := make([]string, len(chain))
	for i, pr := range chain {
		names[i] = pr.Head
	}
	for _, name := range append([]string{trunk}, names...) {
		if err := a.ensureLocal(ctx, st, name); err != nil {
			return "", err
		}
	}
	ir, err := a.initStack(ctx, repo, InitOptions{Branches: names, Base: trunk})
	if err != nil {
		return "", err
	}
	res.NeedsRestack = ir.NeedsRestack
	return top.Head, nil
}

// ensureLocal creates name from the remote's copy when there's no local
// branch of that name; one that exists is left for syncRemote to bring up
// to date.
func (a *App) ensureLocal(ctx context.Context, st *syncState, name string) error {
	if _, ok := st.local[name]; ok {
		return nil
	}
	if _, ok, err := a.d.Git.Tip(ctx, st.repo, "refs/remotes/"+st.remote+"/"+name); err != nil {
		return err
	} else if !ok {
		return stack.Newf(stack.KindInvalidArgs, "%s isn't on %s", name, st.remote).
			WithSteps("it may have been deleted; check the pull request on GitHub")
	}
	if err := a.d.Git.CreateTrackingBranch(ctx, st.repo, name, st.remote); err != nil {
		return err
	}
	return a.loadCheckouts(ctx, st)
}

// prOpen reports whether a pull request can still be stacked on.
func prOpen(pr forge.PullRequest) bool {
	return pr.State == forge.StateOpen || pr.State == forge.StateDraft
}

// getLabel names the target in progress lines.
func getLabel(number int, branch string) string {
	if number > 0 {
		return fmt.Sprintf("#%d", number)
	}
	return branch
}
