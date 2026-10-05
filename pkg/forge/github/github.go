// Package github implements forge.Forge with the gh CLI.
package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Forge talks to GitHub through gh.
type Forge struct {
	run exec.Runner
	// poll is the wait between status checks of an asynchronous merge.
	poll time.Duration
}

// New returns a Forge using r to run gh.
func New(r exec.Runner) *Forge {
	return &Forge{run: r, poll: time.Second}
}

var _ forge.Forge = (*Forge)(nil)

// listLimit caps the PRs fetched per call.
const listLimit = 500

var ghEnv = []string{"GH_NO_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "NO_COLOR=1", "CLICOLOR=0"}

var ghUnset = []string{"GH_FORCE_TTY", "CLICOLOR_FORCE"}

func (f *Forge) gh(ctx context.Context, repo git.Repo, stdin string, args ...string) (exec.Result, error) {
	c := exec.Cmd{Name: "gh", Args: args, Dir: repo.TopLevel, Env: ghEnv, Unset: ghUnset}
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	res, err := f.run.Run(ctx, c)
	if err != nil {
		return res, mapError(err)
	}
	return res, nil
}

var reAuth = regexp.MustCompile(`(?i)gh auth login|not logged in|authentication required|HTTP 401`)

func mapError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if _, ok := errors.AsType[*exec.NotFoundError](err); ok {
		return stack.New(stack.KindNotInstalled, "gh (GitHub CLI) is not installed").
			WithSteps("install it: https://cli.github.com", "then: gh auth login").WithCause(err)
	}
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return err
	}
	stderr := ee.Result.Err()
	if reAuth.MatchString(stderr) {
		return stack.New(stack.KindAuthRequired, "gh is not logged in").
			WithSteps("run `gh auth login`").WithDetail(stderr).WithCause(err)
	}
	return stack.New(stack.KindAPIFailure, "GitHub request failed").WithDetail(stderr).WithCause(err)
}

// prJSON is gh's --json shape for the fields we ask for.
type prJSON struct {
	Number            int       `json:"number"`
	URL               string    `json:"url"`
	Title             string    `json:"title"`
	State             string    `json:"state"`
	IsDraft           bool      `json:"isDraft"`
	IsCrossRepository bool      `json:"isCrossRepository"`
	HeadRefName       string    `json:"headRefName"`
	BaseRefName       string    `json:"baseRefName"`
	UpdatedAt         time.Time `json:"updatedAt"`
	HeadRefOid        string    `json:"headRefOid"`
	MergeCommit       *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

const prFields = "number,url,title,state,isDraft,isCrossRepository,headRefName,baseRefName,updatedAt,headRefOid,mergeCommit"

func (p prJSON) toForge() forge.PullRequest {
	pr := forge.PullRequest{
		Number: p.Number, URL: p.URL, Title: p.Title,
		Head: p.HeadRefName, Base: p.BaseRefName, UpdatedAt: p.UpdatedAt,
	}
	pr.HeadSHA = p.HeadRefOid
	if p.MergeCommit != nil {
		pr.MergeCommit = p.MergeCommit.OID
	}
	switch strings.ToUpper(p.State) {
	case "MERGED":
		pr.State = forge.StateMerged
	case "CLOSED":
		pr.State = forge.StateClosed
	default:
		if p.IsDraft {
			pr.State = forge.StateDraft
		} else {
			pr.State = forge.StateOpen
		}
	}
	return pr
}

// ListPRs returns every pull request of the repository whose head branch
// lives in this repository (forks are skipped), newest first as gh orders.
func (f *Forge) ListPRs(ctx context.Context, repo git.Repo) ([]forge.PullRequest, error) {
	res, err := f.gh(ctx, repo, "", "pr", "list", "--state", "all", "--limit", strconv.Itoa(listLimit), "--json", prFields)
	if err != nil {
		return nil, err
	}
	var raw []prJSON
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	out := make([]forge.PullRequest, 0, len(raw))
	for _, p := range raw {
		if p.IsCrossRepository {
			continue
		}
		out = append(out, p.toForge())
	}
	return out, nil
}

// CreatePR opens a pull request and returns it.
func (f *Forge) CreatePR(ctx context.Context, repo git.Repo, in forge.CreatePR) (forge.PullRequest, error) {
	args := []string{"pr", "create", "--head", in.Head, "--base", in.Base, "--title", in.Title, "--body-file", "-"}
	if in.Draft {
		args = append(args, "--draft")
	}
	if _, err := f.gh(ctx, repo, in.Body+"\n", args...); err != nil {
		return forge.PullRequest{}, err
	}
	return f.view(ctx, repo, in.Head)
}

func (f *Forge) view(ctx context.Context, repo git.Repo, selector string) (forge.PullRequest, error) {
	res, err := f.gh(ctx, repo, "", "pr", "view", selector, "--json", prFields)
	if err != nil {
		return forge.PullRequest{}, err
	}
	var p prJSON
	if err := json.Unmarshal(res.Stdout, &p); err != nil {
		return forge.PullRequest{}, fmt.Errorf("parse gh pr view output: %w", err)
	}
	return p.toForge(), nil
}

// UpdatePR edits title, body and base, and can mark a draft ready.
func (f *Forge) UpdatePR(ctx context.Context, repo git.Repo, number int, in forge.UpdatePR) error {
	num := strconv.Itoa(number)
	args := []string{"pr", "edit", num}
	stdin := ""
	if in.Title != nil {
		args = append(args, "--title", *in.Title)
	}
	if in.Body != nil {
		args = append(args, "--body-file", "-")
		stdin = *in.Body + "\n"
	}
	if in.Base != nil {
		args = append(args, "--base", *in.Base)
	}
	if len(args) > 3 {
		if _, err := f.gh(ctx, repo, stdin, args...); err != nil {
			return err
		}
	}
	if in.Ready != nil && *in.Ready {
		if _, err := f.gh(ctx, repo, "", "pr", "ready", num); err != nil {
			return err
		}
	}
	return nil
}
