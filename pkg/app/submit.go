package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/ai"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// PRText is a title and body supplied for a branch.
type PRText struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// SubmitOptions mirrors `gt submit`.
type SubmitOptions struct {
	// Draft / Publish force the state of new PRs. With neither, the
	// configured default applies: ready for review unless stack.submit.default
	// says draft, or ask (which prompts on a terminal and publishes otherwise).
	Draft   bool
	Publish bool
	// NoEdit skips the backend's interactive PR editor.
	NoEdit bool
	// DryRun reports what would happen without pushing.
	DryRun bool
	// UseAI drafts title and body for every new PR.
	UseAI bool
	// Texts supplies title/body per branch (the MCP path). Branches without
	// a PR need an entry unless UseAI is set or the editor can open.
	Texts map[string]PRText
	// Force pushes even when a branch has commits on the remote that aren't
	// in the local branch, overwriting them.
	Force bool
	// Down submits only the current branch and the ones below it, leaving
	// the ones above alone.
	Down bool
	// Branch submits that branch and the ones below it instead of the
	// current branch; it implies Down.
	Branch string
}

// SubmittedPR is one line of the result.
type SubmittedPR struct {
	Branch  string      `json:"branch"`
	Number  int         `json:"number,omitempty"`
	URL     string      `json:"url,omitempty"`
	State   forge.State `json:"state,omitempty"`
	Created bool        `json:"created"`
	// WouldCreate is set in dry runs for branches without a PR.
	WouldCreate bool `json:"wouldCreate,omitempty"`
	// TextUpdated is true when a supplied or drafted title/body was applied.
	TextUpdated bool `json:"textUpdated,omitempty"`
	// BaseFixed is true when the PR's base was moved back onto its parent
	// branch after gh stack had pointed it elsewhere (see fixBases).
	BaseFixed bool `json:"baseFixed,omitempty"`
}

// SubmitResult reports the outcome.
type SubmitResult struct {
	Stack        []string      `json:"stack"`
	PullRequests []SubmittedPR `json:"pullRequests"`
	Draft        bool          `json:"draft"`
	DryRun       bool          `json:"dryRun"`
	// Output is the backend's own output (captured runs only).
	Output string `json:"output,omitempty"`
	// Closed are the pull requests a pop or fold left behind that this
	// submit closed.
	Closed  []ClosedPR `json:"closed,omitempty"`
	Notices []string   `json:"notices,omitempty"`
}

// ClosedPR is a queued pull request a submit closed.
type ClosedPR struct {
	Number int    `json:"number"`
	Branch string `json:"branch"`
	// BranchDeleted is false when the remote branch had to stay because an
	// open pull request is still based on it.
	BranchDeleted bool `json:"branchDeleted"`
}

// Submit pushes the current stack and creates or updates its pull requests.
// With Down or Branch it stops at a branch; when branches above it are left
// out, submitPart does the work instead of gh stack submit.
func (a *App) Submit(ctx context.Context, repo git.Repo, o SubmitOptions) (SubmitResult, error) {
	res, err := a.submit(ctx, repo, o)
	if err == nil && !o.DryRun {
		// GitHub has the stack now; undoing what came before would only
		// make the local branches disagree with it.
		if cerr := a.clearUndo(ctx, repo); cerr != nil {
			a.d.Log.Warn("could not clear the undo history", "err", cerr)
		}
	}
	return res, err
}

// submit is Submit's body.
func (a *App) submit(ctx context.Context, repo git.Repo, o SubmitOptions) (SubmitResult, error) {
	if a.d.Submit == nil {
		return SubmitResult{}, stack.New(stack.KindUnsupported, "no submit backend configured")
	}
	if err := a.noRebaseActive(ctx, repo); err != nil {
		return SubmitResult{}, err
	}
	target := o.Branch
	if target == "" {
		cur, err := a.d.Git.CurrentBranch(ctx, repo)
		if err != nil {
			if errors.Is(err, git.ErrDetached) {
				return SubmitResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch or name one")
			}
			return SubmitResult{}, err
		}
		target = cur
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return SubmitResult{}, err
	}
	s, idx, ok := graph.StackOf(target)
	if !ok {
		if graph.IsTrunk(target) {
			return SubmitResult{}, stack.Newf(stack.KindNotInStack, "%s is a trunk; check out a stacked branch or name one to submit", target)
		}
		return SubmitResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", target)
	}
	scope := s.Branches
	if o.Down || o.Branch != "" {
		scope = s.Branches[:idx+1]
	}
	partial := slices.ContainsFunc(s.Branches[len(scope):], func(b stack.Branch) bool { return !b.Merged() })
	if !slices.ContainsFunc(scope, func(b stack.Branch) bool { return !b.Merged() }) {
		return SubmitResult{}, stack.Newf(stack.KindInvalidArgs, "nothing to submit up to %s; its pull request has merged", target)
	}
	res := SubmitResult{Stack: s.Names(), DryRun: o.DryRun}
	if !o.Force {
		var live []string
		for _, b := range scope {
			if !b.Merged() {
				live = append(live, b.Name)
			}
		}
		if err := a.checkPushedOver(ctx, repo, live); err != nil {
			return SubmitResult{}, err
		}
	}

	// Existing PRs decide which branches are new.
	known, err := a.loadPRs(ctx, repo, PRsFresh)
	if err != nil {
		return SubmitResult{}, err
	}
	before := PRsFor(known)
	var newBranches []string
	for _, b := range scope {
		if b.Merged() {
			continue
		}
		pr, has := before[b.Name]
		entry := SubmittedPR{Branch: b.Name}
		if has && (pr.State == forge.StateOpen || pr.State == forge.StateDraft) {
			entry.Number, entry.URL, entry.State = pr.Number, pr.URL, pr.State
		} else {
			entry.WouldCreate = true
			newBranches = append(newBranches, b.Name)
		}
		res.PullRequests = append(res.PullRequests, entry)
	}

	// Draft or ready for review only matters for PRs that are about to be
	// created, so the question is only asked when there is at least one.
	draft, err := a.submitDraftChoice(o, len(newBranches) > 0)
	if err != nil {
		return SubmitResult{}, err
	}
	res.Draft = draft
	if o.DryRun {
		return res, nil
	}
	dissolved := a.dissolvePending(ctx, repo, graph, s, &res)

	texts := make(map[string]PRText, len(o.Texts))
	for k, v := range o.Texts {
		texts[k] = v
	}
	if o.UseAI {
		for _, name := range newBranches {
			if _, ok := texts[name]; ok {
				continue
			}
			parent, _ := graph.Parent(name)
			pr, err := a.draftPR(ctx, repo, s.Trunk, parent, name)
			if err != nil {
				return SubmitResult{}, err
			}
			texts[name] = PRText{Title: pr.Title, Body: pr.Body}
		}
	}

	editor := !o.NoEdit && len(texts) == 0 && a.d.Prompter != nil && len(newBranches) > 0
	if partial {
		if editor {
			res.Notices = append(res.Notices, "gh stack's editor only opens for a whole stack submit; new PRs take their titles from their commits")
		}
		if err := a.submitPart(ctx, repo, s, scope, texts, draft, &res); err != nil {
			return SubmitResult{}, err
		}
	} else {
		// Route (b): let gh stack push and create everything, then set texts.
		var out stack.SubmitResult
		submit := func(ctx context.Context) error {
			var err error
			out, err = a.d.Submit.Submit(ctx, repo, stack.SubmitOptions{Publish: !draft, Interactive: editor})
			return err
		}
		if editor {
			// gh stack's editor owns the terminal; no headline over it.
			err = submit(ctx)
		} else {
			err = a.progress(ctx, PhaseSubmit, "Submitting stack", submit)
		}
		if err != nil {
			return SubmitResult{}, err
		}
		if !out.Streamed {
			res.Output = out.Output
		}
	}

	// The cache was refreshed moments ago, so ask the forge again explicitly.
	afterPRs, err := a.RefreshPRs(ctx, repo)
	if err != nil {
		a.d.Log.Warn("could not list pull requests after submit", "err", err)
		afterPRs, _, _ = a.CachedPRs(repo)
	}
	after := PRsFor(afterPRs)
	if a.d.Forge == nil && len(texts) > 0 {
		res.Notices = append(res.Notices, "no forge configured; PR titles and bodies were not updated")
	}
	for i := range res.PullRequests {
		e := &res.PullRequests[i]
		pr, ok := after[e.Branch]
		if !ok {
			continue
		}
		e.Number, e.URL, e.State = pr.Number, pr.URL, pr.State
		if e.WouldCreate {
			e.Created, e.WouldCreate = true, false
		}
		text, hasText := texts[e.Branch]
		if !hasText || e.TextUpdated || a.d.Forge == nil {
			continue
		}
		title, body := text.Title, text.Body
		if err := a.d.Forge.UpdatePR(ctx, repo, pr.Number, forge.UpdatePR{Title: &title, Body: &body}); err != nil {
			res.Notices = append(res.Notices, fmt.Sprintf("could not update #%d (%s): %v", pr.Number, e.Branch, err))
			continue
		}
		e.TextUpdated = true
	}
	fixed := a.fixBases(ctx, repo, s, after, &res)
	if fixed {
		// gh stack also keeps GitHub's stack object (the thing the PR page
		// shows as a stack) and appends new PRs to it, but GitHub rejects the
		// append while the chain is broken by a wrong base. Now that the base
		// is right, run the backend once more so it gets another go; it pushes
		// nothing new and only re-links.
		relink := func() error {
			_, err := a.d.Submit.Submit(ctx, repo, stack.SubmitOptions{Publish: !draft})
			return err
		}
		if partial {
			relink = func() error { return a.linkPart(ctx, repo, s, res.PullRequests, &res) }
		}
		if err := relink(); err != nil {
			res.Notices = append(res.Notices, fmt.Sprintf("could not re-link the stack on GitHub after fixing bases: %v", err))
		}
	}
	a.recreateDissolved(ctx, repo, s, dissolved, after, &res)
	a.closePending(ctx, repo, afterPRs, &res)
	if (len(texts) > 0 || fixed) && a.d.Forge != nil {
		// Titles or bases changed; refresh the cache so views show them.
		if _, err := a.RefreshPRs(ctx, repo); err != nil {
			a.d.Log.Debug("refresh after submit", "err", err)
		}
	}
	return res, nil
}

// submitPart submits only the branches in scope, a run of the stack from
// the bottom up. gh stack submit always pushes every branch, so this pushes
// the branches itself, creates their missing pull requests through the forge,
// and has gh stack link add the pull requests to the stack on GitHub.
func (a *App) submitPart(ctx context.Context, repo git.Repo, s *stack.Stack, scope []stack.Branch, texts map[string]PRText, draft bool, res *SubmitResult) error {
	if a.d.Forge == nil {
		return ErrNoForge
	}
	var live []string
	for _, b := range scope {
		if !b.Merged() {
			live = append(live, b.Name)
		}
	}
	top := live[len(live)-1]
	err := a.progress(ctx, PhaseSubmit, fmt.Sprintf("Submitting %s and the branches below it", top), func(ctx context.Context) error {
		leases := map[string][]git.PushLease{}
		var remotes []string
		for _, name := range live {
			remote, tip, _, err := a.remoteTip(ctx, repo, name)
			if err != nil {
				return err
			}
			if _, seen := leases[remote]; !seen {
				remotes = append(remotes, remote)
			}
			leases[remote] = append(leases[remote], git.PushLease{Branch: name, Expect: tip})
		}
		for _, remote := range remotes {
			if err := a.d.Git.Push(ctx, repo, remote, leases[remote]); err != nil {
				return stack.Newf(stack.KindInvalidArgs, "%v", err).
					WithSteps("run git stack sync to pull in what changed on the remote, then submit again")
			}
		}

		parent := s.Trunk
		for _, name := range live {
			i := slices.IndexFunc(res.PullRequests, func(e SubmittedPR) bool { return e.Branch == name })
			e := &res.PullRequests[i]
			if e.WouldCreate {
				text, hasText := texts[name]
				if !hasText {
					text = a.defaultPRText(ctx, repo, parent, name)
				}
				pr, err := a.d.Forge.CreatePR(ctx, repo, forge.CreatePR{Head: name, Base: parent, Title: text.Title, Body: text.Body, Draft: draft})
				if err != nil {
					return err
				}
				e.Number, e.URL, e.State = pr.Number, pr.URL, pr.State
				e.Created, e.WouldCreate, e.TextUpdated = true, false, hasText
			}
			parent = name
		}
		return a.linkPart(ctx, repo, s, res.PullRequests, res)
	})
	return err
}

// linkPart adds the submitted pull requests to the stack on GitHub. A single
// pull request with no stack yet has nothing to join. A failure is only a
// notice: the pull requests exist, and the next submit links them again.
func (a *App) linkPart(ctx context.Context, repo git.Repo, s *stack.Stack, prs []SubmittedPR, res *SubmitResult) error {
	var numbers []int
	for _, e := range prs {
		if e.Number > 0 {
			numbers = append(numbers, e.Number)
		}
	}
	if s.Number == 0 && len(numbers) < 2 {
		return nil
	}
	out, err := a.d.Submit.Link(ctx, repo, stack.LinkOptions{Stack: s.Number, Trunk: s.Trunk, PRs: numbers})
	if err != nil {
		res.Notices = append(res.Notices, fmt.Sprintf("could not add the pull requests to the stack on GitHub: %v", err))
		return nil
	}
	if !out.Streamed && out.Output != "" {
		res.Output = strings.TrimSpace(res.Output + "\n" + out.Output)
	}
	return nil
}

// defaultPRText is the title and body of a new pull request nobody wrote
// one for: a single commit's subject and body, or else the branch name with
// its dashes as spaces. The repository's PR template replaces the body.
func (a *App) defaultPRText(ctx context.Context, repo git.Repo, parent, branch string) PRText {
	text := PRText{Title: strings.NewReplacer("-", " ", "_", " ").Replace(branch)}
	if msgs, err := a.d.Git.Messages(ctx, repo, parent, branch); err == nil && len(msgs) == 1 {
		subject, body, _ := strings.Cut(strings.TrimSpace(msgs[0]), "\n")
		text = PRText{Title: subject, Body: strings.TrimSpace(body)}
	}
	if template, ok := FindPRTemplate(repo.TopLevel); ok {
		text.Body = template
	}
	return text
}

// fixBases puts every open PR's base back on its parent branch. gh stack
// treats a PR that is sitting in a merge queue like a merged one: it skips
// the branch and bases the next PR on the first branch below that is neither
// merged nor queued, usually trunk, so a brand new PR shows the whole stack's
// diff until the queue drains. The parent is the right base until the parent
// has actually merged, so we correct it here and say so. Returns true when a
// base was changed.
func (a *App) fixBases(ctx context.Context, repo git.Repo, s *stack.Stack, after map[string]forge.PullRequest, res *SubmitResult) bool {
	if a.d.Forge == nil {
		return false
	}
	merged := func(b stack.Branch) bool {
		pr, has := after[b.Name]
		return b.Merged() || (has && pr.State == forge.StateMerged)
	}
	fixed := false
	for i := range res.PullRequests {
		e := &res.PullRequests[i]
		pr, ok := after[e.Branch]
		if !ok || (pr.State != forge.StateOpen && pr.State != forge.StateDraft) {
			continue
		}
		idx := s.Index(e.Branch)
		if idx < 0 {
			continue
		}
		want := s.Trunk
		for j := idx - 1; j >= 0; j-- {
			if !merged(s.Branches[j]) {
				want = s.Branches[j].Name
				break
			}
		}
		if pr.Base == "" || pr.Base == want {
			continue
		}
		base := want
		if err := a.d.Forge.UpdatePR(ctx, repo, pr.Number, forge.UpdatePR{Base: &base}); err != nil {
			res.Notices = append(res.Notices, fmt.Sprintf("#%d (%s) is based on %s rather than %s and the base could not be changed: %v", pr.Number, e.Branch, pr.Base, want, err))
			continue
		}
		e.BaseFixed = true
		fixed = true
		res.Notices = append(res.Notices, fmt.Sprintf("moved the base of #%d (%s) from %s back to %s; gh stack skips branches whose PRs are queued for merge and would have shown the whole stack's diff", pr.Number, e.Branch, pr.Base, want))
	}
	return fixed
}

// submitDraftChoice resolves draft vs publish from flags, config and, when
// the config says ask, ask is true and a prompter exists, a prompt. With
// nothing to ask about (no new PRs) or no terminal, ask means publish.
func (a *App) submitDraftChoice(o SubmitOptions, ask bool) (bool, error) {
	switch {
	case o.Draft && o.Publish:
		return false, stack.New(stack.KindInvalidArgs, "--draft and --publish are mutually exclusive")
	case o.Draft:
		return true, nil
	case o.Publish:
		return false, nil
	}
	switch a.d.Config.SubmitDefault {
	case config.SubmitDraft:
		return true, nil
	case config.SubmitPublish:
		return false, nil
	}
	if a.d.Prompter == nil || !ask {
		return false, nil
	}
	i, err := a.d.Prompter.Select("Create new pull requests as", []string{"ready for review", "draft"})
	if err != nil {
		return false, err
	}
	return i == 1, nil
}

// draftPR builds the AI context for one branch and drafts its PR text.
func (a *App) draftPR(ctx context.Context, repo git.Repo, trunk, parent, branch string) (ai.PullRequest, error) {
	if a.d.AI == nil {
		return ai.PullRequest{}, stack.New(stack.KindUnsupported, "no AI drafter configured").
			WithSteps("drop --ai and use the editor, or pass titles and bodies")
	}
	diff, err := a.d.Git.DiffRange(ctx, repo, parent, branch)
	if err != nil {
		return ai.PullRequest{}, err
	}
	diff, truncated := ai.TruncateDiff(diff, ai.MaxDiffBytes)
	commits, err := a.d.Git.Messages(ctx, repo, parent, branch)
	if err != nil {
		return ai.PullRequest{}, err
	}
	subjects, err := a.d.Git.Subjects(ctx, repo, trunk, recentSubjectsCount)
	if err != nil {
		a.d.Log.Debug("recent subjects", "err", err)
	}
	template, _ := FindPRTemplate(repo.TopLevel)
	in := ai.PRInput{
		Branch: branch, Parent: parent, Diff: diff, DiffTruncated: truncated,
		Commits: commits, RecentSubjects: subjects, Template: template, ExtraPrompt: a.d.Config.AIExtraPrompt,
	}
	var out ai.PullRequest
	err = a.progress(ctx, PhaseAI, fmt.Sprintf("Drafting pull request for %s with Claude", branch), func(ctx context.Context) error {
		var err error
		out, err = a.d.AI.DraftPR(ctx, in)
		return err
	})
	return out, err
}

// dissolvePending works through the stacks a reorder or move left stale
// on GitHub: each is dissolved so it can be created again from the local
// order, and the dissolved entries are returned for recreateDissolved. A
// failure keeps the entry for the next submit and says so.
func (a *App) dissolvePending(ctx context.Context, repo git.Repo, graph *stack.Graph, s *stack.Stack, res *SubmitResult) []remoteStack {
	p, err := loadPendingRemote(repo)
	if err != nil {
		res.Notices = append(res.Notices, fmt.Sprintf("could not read %s (%v); delete it to forget the stacks waiting to be dissolved", remoteStacksPath(repo), err))
		return nil
	}
	if p == nil {
		return nil
	}
	var keep, done []remoteStack
	for _, r := range p.Dissolve {
		if err := a.d.Submit.Unstack(ctx, repo, r.Number); err != nil {
			keep = append(keep, r)
			res.Notices = append(res.Notices, fmt.Sprintf("could not dissolve stack #%d on GitHub (%v); the pull requests of %s's stack keep their old grouping until it can be", r.Number, err, r.Stack))
			continue
		}
		done = append(done, r)
		res.Notices = append(res.Notices, dissolvedNotice(graph, s, r, a.d.Forge != nil))
	}
	p.Dissolve = keep
	if err := savePendingRemote(repo, p); err != nil {
		res.Notices = append(res.Notices, fmt.Sprintf("could not update the list of stacks to dissolve: %v", err))
	}
	return done
}

// recreateDissolved creates again on GitHub each stack this submit
// dissolved that is not the one being submitted (the backend creates that
// one): a move's source stack, say, or a reordered stack submitted from
// elsewhere. Without this they would stay ungrouped until someone
// submitted from them. The new id and number are recorded on the stack.
// Every outcome is a notice; a failure leaves the next submit from that
// stack to create it.
func (a *App) recreateDissolved(ctx context.Context, repo git.Repo, s *stack.Stack, dissolved []remoteStack, after map[string]forge.PullRequest, res *SubmitResult) {
	if a.d.Forge == nil || len(dissolved) == 0 {
		return
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		a.d.Log.Warn("could not load the stacks to create them again on GitHub", "err", err)
		return
	}
	var seen []string
	for _, r := range dissolved {
		if slices.Contains(s.Names(), r.Stack) || slices.Contains(seen, r.Stack) {
			continue
		}
		own, i, ok := graph.StackOf(r.Stack)
		if !ok || i != 0 || own.ID != "" {
			continue
		}
		seen = append(seen, r.Stack)
		numbers, gap := stackPRNumbers(own, after)
		if gap != "" {
			res.Notices = append(res.Notices, fmt.Sprintf("%s has no open pull request, so %s's stack was not created on GitHub; submit from it once it has one", gap, r.Stack))
			continue
		}
		if len(numbers) < 2 {
			res.Notices = append(res.Notices, fmt.Sprintf("%s's stack has fewer than two pull requests, so there is no stack to create on GitHub", r.Stack))
			continue
		}
		ref, err := a.d.Forge.CreateStack(ctx, repo, numbers)
		if err != nil {
			res.Notices = append(res.Notices, fmt.Sprintf("could not create %s's stack on GitHub again: %v. Submit from it to try again", r.Stack, err))
			continue
		}
		bottom := r.Stack
		err = a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
			for j := range g.Stacks {
				if g.Stacks[j].Bottom() == bottom {
					g.Stacks[j].ID, g.Stacks[j].Number = ref.ID, ref.Number
				}
			}
			return nil
		})
		if err != nil {
			res.Notices = append(res.Notices, fmt.Sprintf("created stack #%d on GitHub for %s's stack but could not record it (%v)", ref.Number, r.Stack, err))
			continue
		}
		res.Notices = append(res.Notices, fmt.Sprintf("created stack #%d on GitHub again for %s's stack", ref.Number, r.Stack))
	}
}

// stackPRNumbers lists the pull requests of s bottom to top for creating
// it on GitHub, which needs an unbroken chain. Merged branches at the
// bottom are left out; the first branch above them without an open or
// draft pull request is returned as gap, since the chain would break there.
func stackPRNumbers(s *stack.Stack, after map[string]forge.PullRequest) (numbers []int, gap string) {
	bottom := true
	for _, b := range s.Branches {
		pr, has := after[b.Name]
		if bottom && (b.Merged() || (has && pr.State == forge.StateMerged)) {
			continue
		}
		bottom = false
		if !has || (pr.State != forge.StateOpen && pr.State != forge.StateDraft) {
			return nil, b.Name
		}
		numbers = append(numbers, pr.Number)
	}
	return numbers, ""
}

// dissolvedNotice words a successful dissolve by where the entry's stack
// lives now. With a forge, another stack's notice is followed by
// recreateDissolved's; without one, it says how to group the stack again.
func dissolvedNotice(graph *stack.Graph, s *stack.Stack, r remoteStack, hasForge bool) string {
	if slices.Contains(s.Names(), r.Stack) {
		return fmt.Sprintf("dissolved stack #%d on GitHub; it is created again in its new order below", r.Number)
	}
	if own, i, ok := graph.StackOf(r.Stack); ok && i == 0 {
		if !hasForge {
			return fmt.Sprintf("dissolved stack #%d on GitHub because its pull requests were regrouped by a reorder or move; submit from %s to group them again", r.Number, own.Bottom())
		}
		return fmt.Sprintf("dissolved stack #%d on GitHub because its pull requests were regrouped by a reorder or move", r.Number)
	}
	return fmt.Sprintf("dissolved stack #%d on GitHub; its pull requests now belong to the stack they were moved to", r.Number)
}

// closePending works through the pull requests a pop or fold queued: each
// is closed with its comment and then its remote branch deleted. It runs
// after the backend submit because that is what moves the pull requests
// that were based on the removed branch onto its replacement, and GitHub
// closes every open pull request based on a branch that is deleted; so a
// branch something is still based on is kept, and its delete stays queued.
func (a *App) closePending(ctx context.Context, repo git.Repo, prs []forge.PullRequest, res *SubmitResult) {
	p, err := loadPendingRemote(repo)
	if err != nil {
		res.Notices = append(res.Notices, fmt.Sprintf("could not read %s (%v); delete it to forget the pull requests waiting to be closed", remoteStacksPath(repo), err))
		return
	}
	if p == nil || len(p.Close) == 0 {
		return
	}
	if a.d.Forge == nil {
		res.Notices = append(res.Notices, fmt.Sprintf("no forge configured, so %s left behind by a pop or fold stayed open", pluralise(len(p.Close), "a pull request", "pull requests")))
		return
	}
	var keep []pendingClose
	for _, c := range p.Close {
		if !c.Closed {
			if err := a.d.Forge.ClosePR(ctx, repo, c.Number, c.Comment); err != nil {
				keep = append(keep, c)
				res.Notices = append(res.Notices, fmt.Sprintf("could not close #%d (%s): %v; the next submit tries again", c.Number, c.Branch, err))
				continue
			}
			c.Closed = true
		}
		closed := ClosedPR{Number: c.Number, Branch: c.Branch}
		if i := slices.IndexFunc(prs, func(pr forge.PullRequest) bool {
			return pr.Base == c.Branch && pr.Number != c.Number && (pr.State == forge.StateOpen || pr.State == forge.StateDraft)
		}); i >= 0 {
			keep = append(keep, c)
			res.Notices = append(res.Notices, fmt.Sprintf("kept %s on GitHub because #%d is still based on it; the next submit deletes it once that has moved", c.Branch, prs[i].Number))
		} else if err := a.d.Git.DeleteRemoteBranch(ctx, repo, a.d.Git.RemoteFor(ctx, repo, c.Branch), c.Branch, c.Tip); err != nil {
			// Gone already is fine; anything else (it moved on the remote,
			// say) is left for whoever owns it.
			if !strings.Contains(err.Error(), "remote ref does not exist") {
				res.Notices = append(res.Notices, fmt.Sprintf("closed #%d but could not delete %s on GitHub: %v", c.Number, c.Branch, err))
			}
		} else {
			closed.BranchDeleted = true
		}
		res.Closed = append(res.Closed, closed)
	}
	p.Close = keep
	if err := savePendingRemote(repo, p); err != nil {
		res.Notices = append(res.Notices, fmt.Sprintf("could not update the list of pull requests to close: %v", err))
	}
}
