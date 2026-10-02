package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func boolp(b bool) *bool { return &b }

// repoArg is embedded in every input.
type repoArg struct {
	RepoPath string `json:"repo_path,omitempty" jsonschema:"absolute path inside the git repository; defaults to the client's first root, then the server's working directory"`
}

// --- stack_view ---------------------------------------------------------

type viewInput struct {
	repoArg
	IncludeUntracked bool  `json:"include_untracked,omitempty" jsonschema:"also list local branches that are in no stack"`
	Fresh            *bool `json:"fresh,omitempty" jsonschema:"refresh pull request state from GitHub when the cache is stale (default true); false never touches the network"`
}

type prInfo struct {
	Number int    `json:"number"`
	State  string `json:"state,omitempty" jsonschema:"open, draft, merged, closed, or empty when unknown"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
}

type viewBranch struct {
	Name         string  `json:"name"`
	Parent       string  `json:"parent"`
	Head         string  `json:"head,omitempty"`
	PR           *prInfo `json:"pr,omitempty"`
	NeedsRestack bool    `json:"needs_restack" jsonschema:"true when the parent's tip is not in this branch's history"`
	IsCurrent    bool    `json:"is_current"`
	Merged       bool    `json:"merged"`
	Worktree     string  `json:"worktree,omitempty" jsonschema:"path of another worktree the branch is checked out in"`
}

type viewStack struct {
	Trunk    string       `json:"trunk"`
	Number   int          `json:"number,omitempty" jsonschema:"gh stack's stack number, when known"`
	Branches []viewBranch `json:"branches" jsonschema:"bottom (closest to trunk) to top"`
}

type viewOutput struct {
	CurrentBranch string      `json:"current_branch,omitempty"`
	Trunks        []string    `json:"trunks"`
	Stacks        []viewStack `json:"stacks"`
	Untracked     []string    `json:"untracked,omitempty"`
}

func (s *Server) view(ctx context.Context, req *mcp.CallToolRequest, in viewInput) (*mcp.CallToolResult, viewOutput, error) {
	repo, a, err := s.appFor(ctx, req, in.RepoPath)
	if err != nil {
		return nil, viewOutput{}, wrapErr(err)
	}
	mode := app.PRsFresh
	if in.Fresh != nil && !*in.Fresh {
		mode = app.PRsCached
	}
	v, err := a.View(ctx, repo, app.ViewOptions{IncludeUntracked: in.IncludeUntracked, PRs: mode})
	if err != nil {
		return nil, viewOutput{}, wrapErr(err)
	}
	out := viewOutput{CurrentBranch: v.Current, Trunks: []string{}, Stacks: []viewStack{}}
	rowsByName := map[string]app.Row{}
	for _, r := range v.Rows {
		rowsByName[r.Name] = r
		if r.IsTrunk {
			out.Trunks = append(out.Trunks, r.Name)
		} else if !r.Tracked {
			out.Untracked = append(out.Untracked, r.Name)
		}
	}
	for _, st := range v.Graph.Stacks {
		vs := viewStack{Trunk: st.Trunk, Number: st.Number, Branches: []viewBranch{}}
		parent := st.Trunk
		for _, b := range st.Branches {
			vb := viewBranch{Name: b.Name, Parent: parent, Merged: b.Merged()}
			if r, ok := rowsByName[b.Name]; ok {
				vb.Head, vb.NeedsRestack, vb.IsCurrent, vb.Worktree = r.Head, r.NeedsRestack, r.IsCurrent, r.Worktree
				if r.PR != nil {
					vb.PR = &prInfo{Number: r.PR.Number, State: string(r.PR.State), URL: r.PR.URL, Title: r.PR.Title}
				}
			}
			vs.Branches = append(vs.Branches, vb)
			parent = b.Name
		}
		out.Stacks = append(out.Stacks, vs)
	}
	return nil, out, nil
}

// --- stack_create -------------------------------------------------------

type createInput struct {
	repoArg
	Message string `json:"message" jsonschema:"commit message (subject line, blank line, optional body); required even when nothing is staged so the branch name can be derived"`
	Branch  string `json:"branch,omitempty" jsonschema:"branch name; derived from the message when omitted"`
	Staging string `json:"staging,omitempty" jsonschema:"what to stage before committing: all (git add -A), update (git add -u) or none (default)"`
	UseAI   bool   `json:"use_ai,omitempty" jsonschema:"let git-stack's own AI draft the branch name from the diff (opt-in; you normally write it yourself)"`
}

func (s *Server) create(ctx context.Context, req *mcp.CallToolRequest, in createInput) (*mcp.CallToolResult, app.CreateResult, error) {
	if strings.TrimSpace(in.Message) == "" {
		return nil, app.CreateResult{}, wrapErr(stack.New(stack.KindInvalidArgs, "message is required"))
	}
	staging, err := parseStaging(in.Staging)
	if err != nil {
		return nil, app.CreateResult{}, wrapErr(err)
	}
	repo, a, err := s.appFor(ctx, req, in.RepoPath)
	if err != nil {
		return nil, app.CreateResult{}, wrapErr(err)
	}
	res, err := a.Create(ctx, repo, app.CreateOptions{
		Name: in.Branch, Message: []string{in.Message}, Staging: staging, UseAI: in.UseAI,
	})
	if err != nil {
		return nil, app.CreateResult{}, wrapErr(err)
	}
	return nil, res, nil
}

// --- stack_modify -------------------------------------------------------

type modifyInput struct {
	repoArg
	Mode     string `json:"mode,omitempty" jsonschema:"amend (default) rewrites the branch's commit; commit adds a new one. A branch with no commits of its own always gets a new commit"`
	Staging  string `json:"staging,omitempty" jsonschema:"all, update or none (default)"`
	Message  string `json:"message,omitempty" jsonschema:"new commit message; when amending without it the message is kept"`
	Continue bool   `json:"continue,omitempty" jsonschema:"continue an interrupted restack after resolving conflicts"`
	Abort    bool   `json:"abort,omitempty" jsonschema:"abort an interrupted restack and restore the branches"`
}

func (s *Server) modify(ctx context.Context, req *mcp.CallToolRequest, in modifyInput) (*mcp.CallToolResult, app.ModifyResult, error) {
	staging, err := parseStaging(in.Staging)
	if err != nil {
		return nil, app.ModifyResult{}, wrapErr(err)
	}
	var newCommit bool
	switch in.Mode {
	case "", "amend":
	case "commit":
		newCommit = true
	default:
		return nil, app.ModifyResult{}, wrapErr(stack.Newf(stack.KindInvalidArgs, "mode must be amend or commit, got %q", in.Mode))
	}
	repo, a, err := s.appFor(ctx, req, in.RepoPath)
	if err != nil {
		return nil, app.ModifyResult{}, wrapErr(err)
	}
	o := app.ModifyOptions{NewCommit: newCommit, Staging: staging, Continue: in.Continue, Abort: in.Abort}
	if in.Message != "" {
		o.Message = []string{in.Message}
	}
	res, err := a.Modify(ctx, repo, o)
	if err != nil {
		return nil, app.ModifyResult{}, wrapErr(err)
	}
	return nil, res, nil
}

// --- stack_restack ------------------------------------------------------

type restackInput struct {
	repoArg
	Scope    string `json:"scope,omitempty" jsonschema:"all (default), upstack (this branch and above), downstack (this branch and below) or only"`
	Continue bool   `json:"continue,omitempty"`
	Abort    bool   `json:"abort,omitempty"`
}

func (s *Server) restack(ctx context.Context, req *mcp.CallToolRequest, in restackInput) (*mcp.CallToolResult, app.RestackResult, error) {
	var scope stack.Scope
	switch in.Scope {
	case "", "all":
		scope = stack.ScopeAll
	case "upstack":
		scope = stack.ScopeUpstack
	case "downstack":
		scope = stack.ScopeDownstack
	case "only":
		scope = stack.ScopeOnly
	default:
		return nil, app.RestackResult{}, wrapErr(stack.Newf(stack.KindInvalidArgs, "scope must be all, upstack, downstack or only, got %q", in.Scope))
	}
	repo, a, err := s.appFor(ctx, req, in.RepoPath)
	if err != nil {
		return nil, app.RestackResult{}, wrapErr(err)
	}
	res, err := a.Restack(ctx, repo, app.RestackOptions{Scope: scope, Continue: in.Continue, Abort: in.Abort})
	if err != nil {
		return nil, app.RestackResult{}, wrapErr(err)
	}
	return nil, res, nil
}

// --- stack_navigate -----------------------------------------------------

type navigateInput struct {
	repoArg
	Direction string `json:"direction,omitempty" jsonschema:"up, down, top or bottom (ignored when branch is given)"`
	Steps     int    `json:"steps,omitempty" jsonschema:"levels to move for up/down (default 1)"`
	Branch    string `json:"branch,omitempty" jsonschema:"check out this branch directly"`
}

type navigateOutput struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Moved   int    `json:"moved"`
	Clamped bool   `json:"clamped" jsonschema:"true when the request hit the end of the stack"`
	Message string `json:"message,omitempty"`
}

func (s *Server) navigate(ctx context.Context, req *mcp.CallToolRequest, in navigateInput) (*mcp.CallToolResult, navigateOutput, error) {
	repo, a, err := s.appFor(ctx, req, in.RepoPath)
	if err != nil {
		return nil, navigateOutput{}, wrapErr(err)
	}
	from, err := s.opts.Git.CurrentBranch(ctx, repo)
	if err != nil {
		from = ""
	}
	if in.Branch != "" {
		if err := a.Checkout(ctx, repo, in.Branch); err != nil {
			return nil, navigateOutput{}, wrapErr(err)
		}
		return nil, navigateOutput{From: from, To: in.Branch, Moved: 1}, nil
	}
	var dir stack.Direction
	switch in.Direction {
	case "up":
		dir = stack.Up
	case "down":
		dir = stack.Down
	case "top":
		dir = stack.Top
	case "bottom":
		dir = stack.Bottom
	default:
		return nil, navigateOutput{}, wrapErr(stack.Newf(stack.KindInvalidArgs, "direction must be up, down, top or bottom (or pass branch), got %q", in.Direction))
	}
	res, err := a.Navigate(ctx, repo, stack.NavRequest{From: from, Dir: dir, Steps: in.Steps})
	if err != nil {
		return nil, navigateOutput{}, wrapErr(err)
	}
	return nil, navigateOutput{From: from, To: res.Target, Moved: res.Moved, Clamped: res.Clamped, Message: res.Message}, nil
}

// --- stack_submit -------------------------------------------------------

type submitInput struct {
	repoArg
	Publish      bool                  `json:"publish,omitempty" jsonschema:"create new pull requests ready for review instead of drafts"`
	DryRun       bool                  `json:"dry_run,omitempty" jsonschema:"report what would be submitted without pushing"`
	PullRequests map[string]app.PRText `json:"pull_requests,omitempty" jsonschema:"title and body per branch name; write these yourself for every branch that has no PR yet (see stack_view)"`
	UseAI        bool                  `json:"use_ai,omitempty" jsonschema:"let git-stack's own AI draft missing titles and bodies (opt-in)"`
}

func (s *Server) submit(ctx context.Context, req *mcp.CallToolRequest, in submitInput) (*mcp.CallToolResult, app.SubmitResult, error) {
	repo, a, err := s.appFor(ctx, req, in.RepoPath)
	if err != nil {
		return nil, app.SubmitResult{}, wrapErr(err)
	}
	res, err := a.Submit(ctx, repo, app.SubmitOptions{
		Publish: in.Publish, Draft: !in.Publish, NoEdit: true, DryRun: in.DryRun, UseAI: in.UseAI, Texts: in.PullRequests,
	})
	if err != nil {
		return nil, app.SubmitResult{}, wrapErr(err)
	}
	if !in.DryRun && !in.UseAI {
		for _, pr := range res.PullRequests {
			if pr.Created && !pr.TextUpdated {
				res.Notices = append(res.Notices, fmt.Sprintf("%s: PR #%d was created with an auto-generated title; pass pull_requests[%q] to set a title and body", pr.Branch, pr.Number, pr.Branch))
			}
		}
	}
	return nil, res, nil
}

// --- stack_sync ---------------------------------------------------------

type syncInput struct {
	repoArg
	Prune bool `json:"prune,omitempty" jsonschema:"delete merged branches even when git config stack.sync.prune is ask or never (the default policy, always, deletes them anyway)"`
}

func (s *Server) sync(ctx context.Context, req *mcp.CallToolRequest, in syncInput) (*mcp.CallToolResult, app.SyncResult, error) {
	repo, a, err := s.appFor(ctx, req, in.RepoPath)
	if err != nil {
		return nil, app.SyncResult{}, wrapErr(err)
	}
	res, err := a.Sync(ctx, repo, app.SyncOptions{Prune: in.Prune})
	if err != nil {
		return nil, app.SyncResult{}, wrapErr(err)
	}
	return nil, res, nil
}

// --- helpers ------------------------------------------------------------

func (s *Server) appFor(ctx context.Context, req *mcp.CallToolRequest, repoPath string) (git.Repo, *app.App, error) {
	repo, err := s.repo(ctx, req, repoPath)
	if err != nil {
		return git.Repo{}, nil, err
	}
	a, err := s.opts.NewApp(ctx, repo)
	if err != nil {
		return git.Repo{}, nil, err
	}
	return repo, a, nil
}

func parseStaging(s string) (app.StagingMode, error) {
	switch s {
	case "", "none":
		return app.StageNone, nil
	case "all":
		return app.StageAll, nil
	case "update":
		return app.StageUpdate, nil
	default:
		return app.StageNone, stack.Newf(stack.KindInvalidArgs, "staging must be all, update or none, got %q", s)
	}
}

func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "stack_view",
		Title:       "View stacks",
		Description: "Show every stack in the repository as a tree: trunks, branches with their parent, pull request number/state/URL, whether each branch needs a restack, the current branch and (optionally) untracked branches. Read-only; may refresh PR state from GitHub.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolp(true), IdempotentHint: true},
	}, s.view)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "stack_create",
		Title:       "Create a stacked branch",
		Description: "Create a new branch on top of the current branch, commit staged changes with the given message, and register it in the stack. From the trunk this starts a new stack. Fails with not_at_top when the current branch is not the top of its stack.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolp(false), OpenWorldHint: boolp(false)},
	}, s.create)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "stack_modify",
		Title:       "Amend the current branch and restack",
		Description: "Amend the current branch's commit (or add a new commit with mode: commit) with the staged changes, then rebase every branch above it. On conflicts returns code conflict with the files; resolve, git add, then call again with continue: true.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolp(true), OpenWorldHint: boolp(false)},
	}, s.modify)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "stack_restack",
		Title:       "Restack",
		Description: "Rebase the current stack's branches onto their parents locally (no fetch). Scopes: all, upstack, downstack. On conflicts returns code conflict; resolve, git add, then call again with continue: true or abort: true.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolp(true), OpenWorldHint: boolp(false)},
	}, s.restack)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "stack_navigate",
		Title:       "Navigate the stack",
		Description: "Check out another branch of the stack: direction up/down/top/bottom with optional steps (down from the bottom branch lands on the trunk), or branch to check out a branch directly.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolp(false), IdempotentHint: true, OpenWorldHint: boolp(false)},
	}, s.navigate)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "stack_submit",
		Title:       "Submit the stack",
		Description: "Push every branch of the current stack and create or update chained pull requests on GitHub. New PRs are drafts unless publish is true. Supply pull_requests {branch: {title, body}} for branches without a PR; use dry_run to see the plan first.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolp(true), OpenWorldHint: boolp(true)},
	}, s.submit)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "stack_sync",
		Title:       "Sync the stack",
		Description: "Fetch from the remote, update trunk, restack and push every stack (checked out or not, in any worktree; a stack that is not checked out has a branch checked out in its worktree for the sync and the previous branch restored), deleting merged branches according to stack.sync.prune (always by default; prune forces it).",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolp(true), OpenWorldHint: boolp(true)},
	}, s.sync)
}

var _ = forge.StateOpen
