package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// mergeJSON is the body of GitHub's asynchronous merge endpoints.
type mergeJSON struct {
	Status  string `json:"status"` // pending, merged, enqueued, failed
	Details struct {
		Message string `json:"message"`
		UUID    string `json:"uuid"`
		SHA     string `json:"sha"`
	} `json:"details"`
}

// MergeStack merges a pull request and every open pull request below it in
// its stack through GitHub's asynchronous merge endpoint, the only one that
// accepts stacked pull requests, and polls until it settles.
func (f *Forge) MergeStack(ctx context.Context, repo git.Repo, number int, method forge.MergeMethod) (forge.MergeOutcome, error) {
	path := fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/merge-async", number)
	args := []string{"api", "--method", "PUT", path}
	if method != forge.MergeDefault {
		args = append(args, "-f", "merge_method="+string(method))
	}
	res, err := f.gh(ctx, repo, "", args...)
	if err != nil {
		return forge.MergeOutcome{}, mergeRefused(err)
	}
	var m mergeJSON
	if err := json.Unmarshal(res.Stdout, &m); err != nil {
		return forge.MergeOutcome{}, fmt.Errorf("parse the merge response: %w", err)
	}
	for m.Status == "pending" {
		select {
		case <-ctx.Done():
			return forge.MergeOutcome{}, ctx.Err()
		case <-time.After(f.poll):
		}
		res, err := f.gh(ctx, repo, "", "api", path+"/"+m.Details.UUID)
		if err != nil {
			return forge.MergeOutcome{}, err
		}
		if err := json.Unmarshal(res.Stdout, &m); err != nil {
			return forge.MergeOutcome{}, fmt.Errorf("parse the merge status: %w", err)
		}
	}
	out := forge.MergeOutcome{SHA: m.Details.SHA, Message: m.Details.Message}
	switch m.Status {
	case "merged":
		out.Status = forge.MergeMerged
	case "enqueued":
		out.Status = forge.MergeEnqueued
	default:
		msg := m.Details.Message
		if msg == "" {
			msg = "GitHub reported " + m.Status
		}
		return forge.MergeOutcome{}, stack.Newf(stack.KindAPIFailure, "GitHub did not merge the stack: %s", msg).
			WithSteps("fix what it reports and run git stack merge again")
	}
	return out, nil
}

// mergeRefused turns gh's error for a rejected merge request (a draft, a
// closed PR, a pending merge, failed rules) into one that says what to do.
func mergeRefused(err error) error {
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindAPIFailure {
		return err
	}
	detail := se.Detail
	// gh api prints the JSON body on stdout and "gh: <message> (HTTP 4xx)" on stderr.
	msg := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(detail, "\n", 2)[0], "gh:"))
	if msg == "" {
		msg = "GitHub refused the merge"
	}
	return stack.Newf(stack.KindAPIFailure, "GitHub refused to merge the stack: %s", msg).
		WithDetail(detail).WithCause(err).
		WithSteps("every pull request below must be open and ready for review; git stack submit --publish marks drafts ready",
			"check branch protection and required checks on the pull requests")
}
