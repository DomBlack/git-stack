package app

import (
	"context"
	"errors"
	"fmt"

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
	// configured default applies (ask interactively, else draft).
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
	// UpdateOnly is accepted for parity but unsupported by gh stack.
	UpdateOnly bool
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
	Output  string   `json:"output,omitempty"`
	Notices []string `json:"notices,omitempty"`
}

// Submit pushes the current stack and creates or updates its pull requests.
func (a *App) Submit(ctx context.Context, repo git.Repo, o SubmitOptions) (SubmitResult, error) {
	if a.d.Submit == nil {
		return SubmitResult{}, stack.New(stack.KindUnsupported, "no submit backend configured")
	}
	if o.UpdateOnly {
		return SubmitResult{}, stack.New(stack.KindUnsupported, "gh stack always creates PRs for every branch; --update-only is not available").
			WithSteps("submit the whole stack, or push a single branch with git push")
	}
	current, err := a.d.Git.CurrentBranch(ctx, repo)
	if err != nil {
		if errors.Is(err, git.ErrDetached) {
			return SubmitResult{}, stack.New(stack.KindInvalidArgs, "HEAD is detached; check out a stacked branch first")
		}
		return SubmitResult{}, err
	}
	graph, err := a.d.Meta.Load(ctx, repo)
	if err != nil {
		return SubmitResult{}, err
	}
	s, _, ok := graph.StackOf(current)
	if !ok {
		if graph.IsTrunk(current) {
			return SubmitResult{}, stack.Newf(stack.KindNotInStack, "%s is a trunk; check out a stacked branch to submit its stack", current)
		}
		return SubmitResult{}, stack.Newf(stack.KindNotInStack, "%s is not in a stack", current)
	}
	res := SubmitResult{Stack: s.Names(), DryRun: o.DryRun}

	// Existing PRs decide which branches are new.
	before := PRsFor(a.loadPRs(ctx, repo, PRsFresh))
	var newBranches []string
	for _, b := range s.Branches {
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

	// Route (b): let gh stack push and create everything, then set texts.
	interactive := !o.NoEdit && len(texts) == 0 && a.d.Prompter != nil && len(newBranches) > 0
	var out stack.SubmitResult
	submit := func(ctx context.Context) error {
		var err error
		out, err = a.d.Submit.Submit(ctx, repo, stack.SubmitOptions{Publish: !draft, Interactive: interactive})
		return err
	}
	if interactive {
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
		e.Created = e.WouldCreate
		e.WouldCreate = false
		text, hasText := texts[e.Branch]
		if !hasText || a.d.Forge == nil {
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
		if _, err := a.d.Submit.Submit(ctx, repo, stack.SubmitOptions{Publish: !draft}); err != nil {
			res.Notices = append(res.Notices, fmt.Sprintf("could not re-link the stack on GitHub after fixing bases: %v", err))
		}
	}
	if (len(texts) > 0 || fixed) && a.d.Forge != nil {
		// Titles or bases changed; refresh the cache so views show them.
		if _, err := a.RefreshPRs(ctx, repo); err != nil {
			a.d.Log.Debug("refresh after submit", "err", err)
		}
	}
	return res, nil
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
// ask is true and a prompter exists, a prompt. With nothing to ask about
// (no new PRs) the configured default is used silently.
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
		return true, nil
	}
	i, err := a.d.Prompter.Select("Create new pull requests as", []string{"draft", "ready for review"})
	if err != nil {
		return false, err
	}
	return i == 0, nil
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
