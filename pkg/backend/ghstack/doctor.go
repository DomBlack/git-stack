package ghstack

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

var _ doctor.Provider = (*Backend)(nil)

// Checks implements doctor.Provider: gh, its login, the gh-stack extension
// and its version, and in a repository gh stack's stack file. repo is nil
// outside a repository.
func (b *Backend) Checks(repo *git.Repo) []doctor.Check {
	checks := []doctor.Check{b.ghCheck()}
	if repo != nil {
		checks = append(checks, b.fileCheck(*repo))
	}
	return checks
}

func (b *Backend) ghCheck() doctor.Check {
	return doctor.Check{
		ID:    "gh",
		Title: "GitHub CLI (gh)",
		Run: func(ctx context.Context) doctor.Finding {
			res, err := b.run.Run(ctx, exec.Cmd{Name: "gh", Args: []string{"--version"}, Env: ghEnv, Unset: ghUnset})
			if err != nil {
				if _, ok := errors.AsType[*exec.NotFoundError](err); ok {
					return doctor.Finding{Status: doctor.Fail, Summary: "gh is not installed",
						Steps: []string{"install it from https://cli.github.com, then run gh auth login"}}
				}
				return doctor.Finding{Status: doctor.Fail, Summary: "gh --version failed", Details: errLines(err)}
			}
			first, _, _ := strings.Cut(res.Out(), "\n")
			return doctor.Finding{Status: doctor.OK, Summary: first}
		},
		Sub: []doctor.Check{b.authCheck(), b.extensionCheck()},
	}
}

func (b *Backend) authCheck() doctor.Check {
	return doctor.Check{
		ID:    "gh.auth",
		Title: "gh is logged in",
		Run: func(ctx context.Context) doctor.Finding {
			res, err := b.run.Run(ctx, exec.Cmd{Name: "gh", Args: []string{"auth", "status"}, Env: ghEnv, Unset: ghUnset})
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return doctor.Finding{Status: doctor.Skipped, Summary: "interrupted"}
				}
				return doctor.Finding{Status: doctor.Fail, Summary: "gh is not logged in",
					Details: errLines(err),
					Steps:   []string{"run gh auth login (gh-stack needs an OAuth login, a personal access token is not enough)"}}
			}
			out := res.Out() + "\n" + res.Err()
			for line := range strings.SplitSeq(out, "\n") {
				line = strings.TrimSpace(strings.TrimLeft(line, "✓- "))
				if strings.HasPrefix(line, "Logged in to ") {
					return doctor.Finding{Status: doctor.OK, Summary: line}
				}
			}
			return doctor.Finding{Status: doctor.OK, Summary: "logged in"}
		},
	}
}

func (b *Backend) extensionCheck() doctor.Check {
	return doctor.Check{
		ID:    "gh-stack",
		Title: "gh-stack extension",
		Run: func(ctx context.Context) doctor.Finding {
			v, err := b.Extension(ctx)
			if err != nil {
				return doctor.Finding{Status: doctor.Fail, Summary: "gh-stack is not installed", Details: errLines(err)}
			}
			if v == "" {
				v = "installed"
			}
			return doctor.Finding{Status: doctor.OK, Summary: "gh-stack " + v}
		},
		Fix: func(ctx context.Context) error {
			preflightOnce.Delete(b)
			return b.ghExtension(ctx, "install", extensionName)
		},
		FixLabel: "gh extension install " + extensionName,
		Sub:      []doctor.Check{b.versionCheck()},
	}
}

func (b *Backend) versionCheck() doctor.Check {
	return doctor.Check{
		ID:    "gh-stack.version",
		Title: "gh-stack is new enough",
		Run: func(ctx context.Context) doctor.Finding {
			v, err := b.Extension(ctx)
			if err != nil {
				return doctor.Finding{Status: doctor.Fail, Summary: "gh-stack is not installed", Details: errLines(err)}
			}
			switch {
			case TooOld(v):
				return doctor.Finding{Status: doctor.Fail,
					Summary: fmt.Sprintf("gh-stack %s is older than %s, which git-stack needs", v, MinVersion)}
			case !strings.HasPrefix(v, "v"):
				return doctor.Finding{Status: doctor.Warn,
					Summary: fmt.Sprintf("gh-stack %q is a local build, so its version can't be checked; git-stack needs %s or later", v, MinVersion)}
			}
			return doctor.Finding{Status: doctor.OK, Summary: fmt.Sprintf("%s (needs %s or later)", v, MinVersion)}
		},
		Fix: func(ctx context.Context) error {
			preflightOnce.Delete(b)
			return b.ghExtension(ctx, "upgrade", "stack")
		},
		FixLabel: "gh extension upgrade stack",
	}
}

// ghExtension runs `gh extension <args>` captured, for doctor's fixes.
func (b *Backend) ghExtension(ctx context.Context, args ...string) error {
	_, err := b.run.Run(ctx, exec.Cmd{Name: "gh", Args: append([]string{"extension"}, args...), Env: ghEnv, Unset: ghUnset})
	if err != nil {
		return mapError([]string{"extension"}, err)
	}
	return nil
}

func (b *Backend) fileCheck(repo git.Repo) doctor.Check {
	return doctor.Check{
		ID:    "stack-file",
		Title: "gh stack's stack file",
		Run: func(context.Context) doctor.Finding {
			c, err := readCatalog(repo)
			if err != nil {
				return doctor.Finding{Status: doctor.Fail, Summary: "can't read the stack file", Details: errLines(err),
					Steps: []string{"fix or move aside " + catalogPath(repo) + ", then run git stack doctor again"}}
			}
			n := len(c.graph.Stacks)
			if n == 1 {
				return doctor.Finding{Status: doctor.OK, Summary: "1 stack in " + catalogPath(repo)}
			}
			return doctor.Finding{Status: doctor.OK, Summary: fmt.Sprintf("%d stacks in %s", n, catalogPath(repo))}
		},
		Sub: []doctor.Check{b.layoutCheck(repo)},
	}
}

// layoutCheck looks for per-worktree files left from gh-stack 0.2, the
// thing that makes gh stack 0.3 stop with "cannot migrate stack catalogs".
func (b *Backend) layoutCheck(repo git.Repo) doctor.Check {
	return doctor.Check{
		ID:    "stack-file.shared",
		Title: "stacks are in the shared file",
		Run: func(context.Context) doctor.Finding {
			if MigrationPending(repo) {
				return doctor.Finding{Status: doctor.Fail,
					Summary: "gh stack is part way through moving stacks into the shared file",
					Details: []string{filepath.Join(catalogDir(repo), migrationJournal)},
					Steps:   []string{"run gh stack view to let it finish"}}
			}
			legacy := LegacyFiles(repo)
			if len(legacy) == 0 {
				return doctor.Finding{Status: doctor.OK, Summary: "every worktree uses " + catalogPath(repo)}
			}
			f := doctor.Finding{Status: doctor.Fail,
				Summary: fmt.Sprintf("%d %s from gh-stack 0.2 still kept per worktree", len(legacy), plural(len(legacy), "stack file", "stack files")),
				Details: legacy}
			if err := checkMovable(repo, legacy); err != nil {
				if se, ok := errors.AsType[*stack.Error](err); ok {
					f.Details = append(f.Details, se.Msg)
					f.Steps = se.NextSteps
				}
			}
			return f
		},
		Fix: func(ctx context.Context) error {
			_, err := b.Migrate(ctx, repo)
			return err
		},
		FixLabel: "move them into the shared file, keeping each as a .pre-worktree-migration backup",
	}
}

// errLines is an error as detail lines: its message, then any detail.
func errLines(err error) []string {
	if se, ok := errors.AsType[*stack.Error](err); ok {
		lines := []string{se.Msg}
		if d := strings.TrimSpace(se.Detail); d != "" {
			lines = append(lines, strings.Split(d, "\n")...)
		}
		return lines
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		if msg := strings.TrimSpace(ee.Result.Err()); msg != "" {
			return strings.Split(msg, "\n")
		}
	}
	return []string{err.Error()}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
