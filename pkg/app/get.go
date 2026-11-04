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
	// Branch is the pull request's branch.
	Branch string `json:"branch"`
	// CheckedOut is set once Branch is checked out; when it isn't, the
	// error says why.
	CheckedOut bool   `json:"checkedOut"`
	Trunk      string `json:"trunk"`
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

// rePRURL matches a pull request's web address, capturing its repository
// (host/owner/name) and number.
var rePRURL = regexp.MustCompile(`^https?://([^/]+/[^/]+/[^/]+)/pull/(\d+)(?:[/?#].*)?$`)

// getTarget is a parsed target: a pull request number or a branch name,
// exactly one of them set. Repo is the host/owner/name of a URL's
// repository, "" for anything else.
type getTarget struct {
	number int
	branch string
	repo   string
}

// parseGetTarget parses a target.
func parseGetTarget(s string) (getTarget, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return getTarget{}, stack.New(stack.KindInvalidArgs, "name a pull request number, URL or branch to get")
	}
	var t getTarget
	if m := rePRURL.FindStringSubmatch(s); m != nil {
		t.repo, s = m[1], m[2]
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "#")); err == nil {
		if n <= 0 {
			return getTarget{}, stack.Newf(stack.KindInvalidArgs, "%s is not a pull request number", s)
		}
		t.number = n
		return t, nil
	}
	t.branch = s
	return t, nil
}

// checkURLRepo refuses a pull request URL for another repository than the
// one we're in: its number means a different pull request here. When the
// forge can't tell which repository this is, the URL is taken on trust.
func (a *App) checkURLRepo(ctx context.Context, repo git.Repo, t getTarget) error {
	if t.repo == "" || a.d.Forge == nil {
		return nil
	}
	here, err := a.d.Forge.PullRequestURL(ctx, repo, t.number)
	if err != nil || here == "" {
		return nil
	}
	m := rePRURL.FindStringSubmatch(here)
	if m == nil || strings.EqualFold(m[1], t.repo) {
		return nil
	}
	return stack.Newf(stack.KindInvalidArgs, "that pull request is in %s, not this repository (%s)", t.repo, m[1]).
		WithSteps("run git stack get from a clone of " + t.repo)
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
	t, err := parseGetTarget(o.Target)
	if err != nil {
		return res, err
	}
	if err := a.checkURLRepo(ctx, repo, t); err != nil {
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

	target, pr, err := a.trackedTarget(ctx, repo, st.graph, t)
	if err != nil {
		return res, err
	}
	if target != "" {
		res.Source = GetLocal
	} else {
		// Refuse what can't be got before gh stack sees it: it would take a
		// merged pull request whose branch is still there, in a stack with
		// open ones.
		if pr != nil {
			if err := gettable(*pr); err != nil {
				return res, err
			}
		}
		if target, err = a.importTarget(ctx, repo, st.graph, t, pr); err != nil {
			return res, err
		}
		if target != "" {
			res.Source = GetStack
		} else {
			res.Source = GetPRs
			if target, err = a.getPRs(ctx, st, t, pr); err != nil {
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
	res.Branch, res.Trunk, res.Branches = target, only.Trunk, only.Names()
	for _, name := range append([]string{only.Trunk}, res.Branches...) {
		if _, ok := before[name]; !ok {
			res.Created = append(res.Created, name)
		}
	}
	if res.NeedsRestack, err = a.getNeedsRestack(ctx, repo, only); err != nil {
		return res, err
	}

	// From here on the branches have moved, so a failure is partial: the
	// result says what was done, and undo can take it back.
	var parts, steps []string
	if err := a.Checkout(ctx, repo, target); err != nil {
		parts = append(parts, fmt.Sprintf("%s was not checked out: %s", target, errMessage(err)))
		if se, ok := errors.AsType[*stack.Error](err); ok && len(se.NextSteps) > 0 {
			steps = append(steps, se.NextSteps...)
		}
		steps = append(steps, "then git stack checkout "+target)
	} else {
		res.CheckedOut = true
	}
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

// errMessage is an error's message without a *stack.Error's steps.
func errMessage(err error) string {
	if se, ok := errors.AsType[*stack.Error](err); ok {
		return se.Msg
	}
	return err.Error()
}

// getNeedsRestack lists the branches of s that aren't on the local tip of
// the one below them (trunk for the bottom one), as they are once get has
// fast forwarded what it could. Merged branches and ones missing locally
// are skipped, and so is a branch above one that is missing.
func (a *App) getNeedsRestack(ctx context.Context, repo git.Repo, s stack.Stack) ([]string, error) {
	local, err := a.localBranches(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []string
	parent := s.Trunk
	for _, b := range s.Branches {
		if b.Merged() {
			continue
		}
		pb, pok := local[parent]
		lb, ok := local[b.Name]
		if pok && ok && !a.isAncestor(ctx, repo, pb.Head, lb.Head) {
			out = append(out, b.Name)
		}
		parent = b.Name
	}
	return out, nil
}

// trackedTarget is the branch the target names when it is already in a
// stack here, else "". A pull request number nothing here records is looked
// up on the forge, since a stack git-stack made itself has no PR records
// until gh stack submits it; the pull request found (nil when there was no
// need, or no forge) is returned for getPRs.
func (a *App) trackedTarget(ctx context.Context, repo git.Repo, graph *stack.Graph, t getTarget) (string, *forge.PullRequest, error) {
	if t.number == 0 {
		if _, _, ok := graph.StackOf(t.branch); ok {
			return t.branch, nil, nil
		}
		return "", nil, nil
	}
	for _, s := range graph.Stacks {
		for _, b := range s.Branches {
			if b.PR != nil && b.PR.Number == t.number {
				return b.Name, nil, nil
			}
		}
	}
	if a.d.Forge == nil {
		return "", nil, nil
	}
	var pr forge.PullRequest
	err := a.progress(ctx, PhaseRefresh, "Looking up "+getLabel(t), func(ctx context.Context) error {
		var err error
		pr, err = a.d.Forge.PR(ctx, repo, t.number)
		return err
	})
	if se, ok := errors.AsType[*stack.Error](err); ok && se.Kind == stack.KindInvalidArgs {
		// No such pull request; gh stack may still know the number as a
		// stack's.
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if !pr.Fork {
		if _, _, ok := graph.StackOf(pr.Head); ok {
			return pr.Head, nil, nil
		}
	}
	return "", &pr, nil
}

// importTarget pulls the target's stack down from GitHub and returns the
// branch it checked out, or "" when the target is in no stack there (or
// the repository doesn't have stacks), for getPRs to handle. gh stack
// creates the branches and saves the stack before it checks the branch
// out, so when it fails after that the target is taken from the stack it
// saved: get carries on, and its own checkout reports the failure as a
// partial result that undo can take back. before is the graph from before
// the import; pr is the pull request when trackedTarget looked it up.
func (a *App) importTarget(ctx context.Context, repo git.Repo, before *stack.Graph, t getTarget, pr *forge.PullRequest) (string, error) {
	if a.d.Tracker == nil {
		return "", nil
	}
	arg := t.branch
	if t.number > 0 {
		arg = strconv.Itoa(t.number)
	}
	err := a.progress(ctx, PhaseSync, "Looking for "+getLabel(t)+" in GitHub's stacks", func(ctx context.Context) error {
		return a.d.Tracker.ImportStack(ctx, repo, arg)
	})
	if se, ok := errors.AsType[*stack.Error](err); ok && (se.Kind == stack.KindNotInStack || se.Kind == stack.KindStacksUnavailable) {
		a.d.Log.Debug("not in a stack on GitHub", "target", arg, "err", err)
		return "", nil
	}
	if err != nil {
		if target := a.importedTarget(ctx, repo, before, t, pr); target != "" {
			a.d.Log.Debug("gh stack failed after importing the stack", "target", target, "err", err)
			return target, nil
		}
		return "", err
	}
	return a.d.Git.CurrentBranch(ctx, repo)
}

// importedTarget is the branch a failed import was getting when it saved a
// new stack holding it (one none of whose branches was tracked before),
// else "". A stack number names the stack's top open branch, the one gh
// stack checks out.
func (a *App) importedTarget(ctx context.Context, repo git.Repo, before *stack.Graph, t getTarget, pr *forge.PullRequest) string {
	after, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return ""
	}
	for _, s := range after.Stacks {
		if slices.ContainsFunc(s.Branches, func(b stack.Branch) bool {
			_, _, ok := before.StackOf(b.Name)
			return ok
		}) {
			continue
		}
		switch {
		case pr != nil && s.Index(pr.Head) >= 0:
			return pr.Head
		case t.number == 0 && s.Index(t.branch) >= 0:
			return t.branch
		case pr == nil && t.number > 0 && s.Number == t.number:
			for _, b := range slices.Backward(s.Branches) {
				if !b.Merged() {
					return b.Name
				}
			}
		}
	}
	return ""
}

// getPRs tracks a pull request that is in no stack on GitHub, together
// with the open pull requests it is based on, as a new stack on the first
// base that has none. top is the pull request when trackedTarget already
// looked it up. It returns the pull request's branch.
func (a *App) getPRs(ctx context.Context, st *syncState, t getTarget, top *forge.PullRequest) (string, error) {
	if a.d.Forge == nil {
		return "", ErrNoForge
	}
	repo := st.repo
	if top == nil {
		var pr forge.PullRequest
		err := a.progress(ctx, PhaseRefresh, "Looking up "+getLabel(t), func(ctx context.Context) error {
			if t.number > 0 {
				var err error
				pr, err = a.d.Forge.PR(ctx, repo, t.number)
				return err
			}
			found, ok, err := a.d.Forge.OpenPRFor(ctx, repo, t.branch)
			if err != nil {
				return err
			}
			if !ok {
				return stack.Newf(stack.KindInvalidArgs, "%s has no open pull request and isn't in a stack on GitHub", t.branch).
					WithSteps("check the name, or pass the pull request's number or URL")
			}
			pr = found
			return nil
		})
		if err != nil {
			return "", err
		}
		top = &pr
	}
	if err := gettable(*top); err != nil {
		return "", err
	}

	// Walk down through the open pull requests it is based on.
	chain := []forge.PullRequest{*top}
	for len(chain) < 100 {
		base := chain[0].Base
		if st.graph.IsTrunk(base) || slices.Contains(st.trunks, base) {
			break
		}
		if s, _, ok := st.graph.StackOf(base); ok {
			return "", stack.Newf(stack.KindUnsupported, "#%d is based on %s, which is already in the stack starting at %s", chain[0].Number, base, s.Bottom()).
				WithSteps("git stack get only adds pull requests as a stack of their own; ask for the pull request to be added to that stack on GitHub")
		}
		var pr forge.PullRequest
		var ok bool
		err := a.progress(ctx, PhaseRefresh, "Looking up the pull request for "+base, func(ctx context.Context) error {
			var err error
			pr, ok, err = a.d.Forge.OpenPRFor(ctx, repo, base)
			return err
		})
		if err != nil {
			return "", err
		}
		if !ok || slices.ContainsFunc(chain, func(c forge.PullRequest) bool { return c.Head == pr.Head }) {
			break
		}
		chain = slices.Insert(chain, 0, pr)
	}

	trunk := chain[0].Base
	names := make([]string, len(chain))
	for i, pr := range chain {
		names[i] = pr.Head
	}
	// Check every branch is on the remote before creating any, so a missing
	// one leaves nothing behind.
	all := append([]string{trunk}, names...)
	var missing []string
	for _, name := range all {
		if _, ok := st.local[name]; ok {
			continue
		}
		_, ok, err := a.d.Git.Tip(ctx, repo, "refs/remotes/"+st.remote+"/"+name)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", stack.Newf(stack.KindInvalidArgs, "%s isn't on %s", name, st.remote).
				WithSteps("it may have been deleted; check the pull request on GitHub")
		}
		missing = append(missing, name)
	}
	var created []string
	err := func() error {
		for _, name := range missing {
			if err := a.d.Git.CreateTrackingBranch(ctx, repo, name, st.remote); err != nil {
				return err
			}
			created = append(created, name)
		}
		if err := a.loadCheckouts(ctx, st); err != nil {
			return err
		}
		_, err := a.initStack(ctx, repo, InitOptions{Branches: names, Base: trunk})
		return err
	}()
	if err != nil {
		// Nothing is tracked yet, so this run's branches can simply go. The
		// context may be what failed, so don't let it stop the cleanup.
		for _, name := range created {
			if derr := a.d.Git.DeleteBranch(context.WithoutCancel(ctx), repo, name); derr != nil {
				a.d.Log.Warn("could not delete a branch git stack get created", "branch", name, "err", derr)
			}
		}
		return "", err
	}
	return top.Head, nil
}

// gettable refuses a pull request get can't pull down: one from a fork, or
// one that is closed or merged.
func gettable(pr forge.PullRequest) error {
	if pr.Fork {
		return stack.Newf(stack.KindUnsupported, "#%d comes from a fork; git stack get only pulls branches from this repository", pr.Number).
			WithSteps(fmt.Sprintf("gh pr checkout %d checks it out without tracking it", pr.Number))
	}
	if !prOpen(pr) {
		return stack.Newf(stack.KindInvalidArgs, "#%d is %s, so there's nothing to get", pr.Number, pr.State)
	}
	return nil
}

// prOpen reports whether a pull request can still be stacked on.
func prOpen(pr forge.PullRequest) bool {
	return pr.State == forge.StateOpen || pr.State == forge.StateDraft
}

// getLabel names the target in progress lines.
func getLabel(t getTarget) string {
	if t.number > 0 {
		return fmt.Sprintf("#%d", t.number)
	}
	return t.branch
}
