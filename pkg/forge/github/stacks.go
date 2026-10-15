package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// createStackJSON is the request body of GitHub's create stack endpoint.
type createStackJSON struct {
	PullRequests []int `json:"pull_requests"`
}

// stackJSON is the part of GitHub's stack object we read.
type stackJSON struct {
	ID     int64 `json:"id"`
	Number int   `json:"number"`
}

// CreateStack groups pull requests into a new stack on GitHub, bottom to
// top. The id is kept as its decimal string, the form gh stack records.
func (f *Forge) CreateStack(ctx context.Context, repo git.Repo, numbers []int) (forge.StackRef, error) {
	body, err := json.Marshal(createStackJSON{PullRequests: numbers})
	if err != nil {
		return forge.StackRef{}, err
	}
	res, err := f.gh(ctx, repo, string(body), "api", "--method", "POST", "repos/{owner}/{repo}/stacks", "--input", "-")
	if err != nil {
		return forge.StackRef{}, stackRefused(err)
	}
	var s stackJSON
	if err := json.Unmarshal(res.Stdout, &s); err != nil {
		return forge.StackRef{}, fmt.Errorf("parse the create stack response: %w", err)
	}
	return forge.StackRef{ID: strconv.FormatInt(s.ID, 10), Number: s.Number}, nil
}

// stackRefused turns gh's error for a rejected create (a pull request
// already stacked, a broken chain, too few pull requests) into one whose
// message is GitHub's own reason, as it gave it.
func stackRefused(err error) error {
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindAPIFailure {
		return err
	}
	msg := apiMessage(err)
	if msg == "" {
		msg = "GitHub refused to create the stack"
	}
	return stack.New(stack.KindAPIFailure, msg).WithDetail(se.Detail).WithCause(err)
}
