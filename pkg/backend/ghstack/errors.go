package ghstack

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// gh-stack exit codes (github.com/github/gh-stack cmd/utils.go).
const (
	exitNotInStack        = 2
	exitConflict          = 3
	exitAPIFailure        = 4
	exitInvalidArgs       = 5
	exitDisambiguate      = 6
	exitRebaseActive      = 7
	exitLocked            = 8
	exitStacksUnavailable = 9
	exitModifyRecovery    = 10
)

var (
	reAuth       = regexp.MustCompile(`(?i)gh auth login|not logged in|authentication required|HTTP 401`)
	reConflictIn = regexp.MustCompile(`(?im)^\s*(?:CONFLICT \([^)]*\):\s*(?:Merge conflict in )?|both modified:\s+|Conflicts?:\s*$\n?\s*)(\S.*?)\s*$`)
)

// mapError converts a gh failure into a *stack.Error with next steps.
func mapError(args []string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if nf, ok := errors.AsType[*exec.NotFoundError](err); ok {
		return stack.Newf(stack.KindNotInstalled, "%s is not installed", nf.Name).
			WithSteps("install the GitHub CLI: https://cli.github.com", "then: gh extension install github/gh-stack").
			WithCause(err)
	}
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return err
	}
	stderr := ee.Result.Err()
	sub := "gh stack"
	if len(args) > 0 {
		sub += " " + args[0]
	}
	if reAuth.MatchString(stderr) {
		return stack.New(stack.KindAuthRequired, "gh is not logged in").
			WithSteps("run `gh auth login` (gh-stack needs OAuth, a personal access token is not enough)").
			WithDetail(stderr).WithCause(err)
	}
	var se *stack.Error
	switch ee.Result.ExitCode {
	case exitNotInStack:
		se = stack.New(stack.KindNotInStack, "the current branch is not in a stack").
			WithSteps("run `git stack create` from trunk to start one", "or `git stack checkout` to pick a stacked branch")
	case exitConflict:
		se = stack.New(stack.KindConflict, "rebase stopped on conflicts")
		se.Files = conflictedFiles(stderr)
	case exitAPIFailure:
		se = stack.New(stack.KindAPIFailure, "GitHub API request failed")
	case exitInvalidArgs:
		se = stack.Newf(stack.KindInvalidArgs, "%s rejected the request", sub)
	case exitDisambiguate:
		se = stack.New(stack.KindDisambiguate, "the branch belongs to several stacks").
			WithSteps("check out a branch inside the stack you mean, then retry")
	case exitRebaseActive:
		se = stack.New(stack.KindRebaseActive, "a git rebase is already in progress").
			WithSteps("finish it with `git stack restack --continue`", "or abandon it with `git stack restack --abort`")
	case exitLocked:
		se = stack.New(stack.KindLocked, "the stack metadata is locked or was changed by another process").
			WithSteps("wait for the other gh-stack process to finish and retry")
	case exitStacksUnavailable:
		se = stack.New(stack.KindStacksUnavailable, "stacked pull requests are not available for this repository").
			WithSteps("stacked PRs are a repository setting on GitHub; ask an admin to enable them",
				"gh-stack also needs OAuth login (`gh auth login`), not a personal access token")
	case exitModifyRecovery:
		se = stack.New(stack.KindModifyRecovery, "gh-stack has an interrupted modify to recover").
			WithSteps("run `gh stack modify --continue` or `gh stack modify --abort`")
	default:
		se = stack.Newf(stack.KindUnknown, "%s failed", sub)
	}
	return se.WithDetail(stderr).WithCause(err)
}

// conflictedFiles extracts file paths from git/gh-stack conflict output.
func conflictedFiles(stderr string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reConflictIn.FindAllStringSubmatch(stderr, -1) {
		f := strings.TrimSpace(m[1])
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
