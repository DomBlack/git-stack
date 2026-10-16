package github

import (
	"context"
	"errors"
	"net/url"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// RenameBranch renames a branch through GitHub's branch rename endpoint,
// which moves its open pull request with it and retargets the ones based
// on it.
func (f *Forge) RenameBranch(ctx context.Context, repo git.Repo, from, to string) error {
	path := "repos/{owner}/{repo}/branches/" + url.PathEscape(from) + "/rename"
	_, err := f.gh(ctx, repo, "", "api", "--method", "POST", path, "-f", "new_name="+to)
	if se, ok := errors.AsType[*stack.Error](err); ok && se.Kind == stack.KindAPIFailure {
		msg := apiMessage(err)
		if msg == "" {
			msg = "GitHub refused to rename " + from
		}
		return stack.New(stack.KindAPIFailure, msg).WithDetail(se.Detail).WithCause(err)
	}
	return err
}
