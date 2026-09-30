package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

var reNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// maxSlugLen bounds generated branch names (without prefix).
const maxSlugLen = 60

// Slug turns free text (a commit subject, an AI suggestion) into a
// kebab-case branch fragment: lowercase, runs of non-alphanumerics become
// one dash, dashes trimmed, truncated at a word boundary.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "/", "-")
	s = reNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxSlugLen {
		s = s[:maxSlugLen]
		if i := strings.LastIndex(s, "-"); i > maxSlugLen/2 {
			s = s[:i]
		}
	}
	return s
}

// derivedBranchName applies the configured prefix, validates the name and
// appends -2, -3, … while a local branch with that name exists.
func (a *App) derivedBranchName(ctx context.Context, repo git.Repo, slug string) (string, error) {
	if slug == "" {
		return "", stack.New(stack.KindInvalidArgs, "could not derive a branch name").
			WithSteps("pass a branch name: git stack create <name>")
	}
	base := a.d.Config.BranchPrefix + slug
	if err := a.validateBranchName(ctx, base); err != nil {
		return "", err
	}
	name := base
	for i := 2; i < 100; i++ {
		exists, err := a.d.Git.BranchExists(ctx, repo, name)
		if err != nil {
			return "", err
		}
		if !exists {
			return name, nil
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return "", stack.Newf(stack.KindInvalidArgs, "too many branches named like %s", base)
}

// validateBranchName runs git check-ref-format.
func (a *App) validateBranchName(ctx context.Context, name string) error {
	if err := a.d.Git.CheckRefFormat(ctx, name); err != nil {
		return stack.Newf(stack.KindInvalidArgs, "%q is not a valid branch name", name).WithCause(err)
	}
	return nil
}
