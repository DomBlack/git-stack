package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/shell"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/version"
)

// DoctorOptions controls Doctor.
type DoctorOptions struct {
	// Fix runs the fix of every check that failed or warned and has one,
	// then checks again.
	Fix bool
	// Extra are checks this run adds to the ones from Deps.Doctors (the
	// CLI adds git-stack's own aliases and release).
	Extra []doctor.Provider
}

// DoctorResult is every check, in the order they ran, each with the checks
// that hang off it.
type DoctorResult struct {
	// Checks is the tree, for rendering; List is the same checks flat,
	// each naming its parent, for JSON.
	Checks []doctor.Result `json:"-"`
	List   []doctor.Result `json:"checks"`
	// Failed and Warned count the checks left failing or warning; Fixed
	// those --fix put right.
	Failed int `json:"failed"`
	Warned int `json:"warned"`
	Fixed  int `json:"fixed"`
	// Fixable counts the checks still failing or warning that --fix could
	// try (zero after a --fix run unless a fix failed).
	Fixable int `json:"fixable"`
}

// Doctor checks that git-stack has what it needs (git, gh, gh-stack and its
// version) and, inside a repository, that its stacks are in good shape. With
// Fix it tries to put right what it can and checks again. repo is nil
// outside a repository. Problems are in the result, not the error; the
// error is only for being interrupted.
func (a *App) Doctor(ctx context.Context, repo *git.Repo, o DoctorOptions) (DoctorResult, error) {
	checks := []doctor.Check{a.gitCheck()}
	for _, p := range slices.Concat(a.d.Doctors, o.Extra) {
		checks = append(checks, p.Checks(repo)...)
	}
	if repo != nil {
		checks = append(checks, a.stackChecks(*repo))
	}

	var results []doctor.Result
	tried := map[string]error{}
	// A fix can let a check further down run for the first time (gh-stack
	// installed, so its version can be looked at), so this goes round until
	// there is nothing new to fix.
	for {
		err := a.progress(ctx, PhaseDoctor, "Checking git-stack's setup", func(ctx context.Context) error {
			results = runChecks(ctx, checks)
			return ctx.Err()
		})
		if err != nil {
			return DoctorResult{}, err
		}
		if !o.Fix {
			break
		}
		var todo []doctor.Check
		walkFixable(checks, results, func(c doctor.Check) {
			if _, done := tried[c.ID]; !done {
				todo = append(todo, c)
			}
		})
		if len(todo) == 0 {
			break
		}
		for _, c := range todo {
			err := a.progress(ctx, PhaseDoctor, "Fixing: "+c.FixLabel, func(ctx context.Context) error {
				return c.Fix(ctx)
			})
			if ctx.Err() != nil {
				return DoctorResult{}, ctx.Err()
			}
			tried[c.ID] = err
		}
	}

	res := DoctorResult{Checks: results}
	markFixes(res.Checks, tried, &res)
	res.List = doctor.Flatten(res.Checks)
	return res, nil
}

// runChecks runs each check and, unless it failed, the ones under it.
func runChecks(ctx context.Context, checks []doctor.Check) []doctor.Result {
	out := make([]doctor.Result, 0, len(checks))
	for _, c := range checks {
		r := doctor.Result{ID: c.ID, Title: c.Title}
		if ctx.Err() != nil {
			r.Status = doctor.Skipped
			out = append(out, r)
			continue
		}
		f := c.Run(ctx)
		r.Status, r.Summary, r.Details, r.Steps = f.Status, f.Summary, f.Details, f.Steps
		if c.Fix != nil && (f.Status == doctor.Fail || f.Status == doctor.Warn) {
			r.Fix = c.FixLabel
		}
		if f.Status == doctor.Fail {
			r.Checks = skipped(c.Sub, c.Title)
		} else {
			r.Checks = runChecks(ctx, c.Sub)
		}
		out = append(out, r)
	}
	return out
}

// skipped marks checks that couldn't run because the one above failed.
func skipped(checks []doctor.Check, parent string) []doctor.Result {
	out := make([]doctor.Result, 0, len(checks))
	for _, c := range checks {
		out = append(out, doctor.Result{ID: c.ID, Title: c.Title, Status: doctor.Skipped,
			Summary: "skipped; needs " + parent, Checks: skipped(c.Sub, parent)})
	}
	return out
}

// walkFixable calls fn with every check whose result failed or warned and
// that has a fix.
func walkFixable(checks []doctor.Check, results []doctor.Result, fn func(doctor.Check)) {
	for i, c := range checks {
		if i >= len(results) {
			return
		}
		r := results[i]
		if c.Fix != nil && (r.Status == doctor.Fail || r.Status == doctor.Warn) {
			fn(c)
		}
		walkFixable(c.Sub, r.Checks, fn)
	}
}

// markFixes turns the checks a fix put right into Fixed, notes why a fix
// that didn't work failed, and counts what is left.
func markFixes(results []doctor.Result, tried map[string]error, res *DoctorResult) {
	for i := range results {
		r := &results[i]
		if err, ok := tried[r.ID]; ok {
			switch {
			case err != nil:
				r.FixError = errText(err)
			case r.Status == doctor.OK:
				r.Status = doctor.Fixed
			default:
				r.FixError = "the fix ran but the check still doesn't pass"
			}
		}
		switch r.Status {
		case doctor.Fail:
			res.Failed++
		case doctor.Warn:
			res.Warned++
		case doctor.Fixed:
			res.Fixed++
		}
		if r.Fix != "" && r.FixError == "" && (r.Status == doctor.Fail || r.Status == doctor.Warn) {
			res.Fixable++
		}
		markFixes(r.Checks, tried, res)
	}
}

func errText(err error) string {
	if se, ok := errors.AsType[*stack.Error](err); ok {
		return se.Msg
	}
	return err.Error()
}

// gitCheck looks at git itself.
func (a *App) gitCheck() doctor.Check {
	return doctor.Check{
		ID:    "git",
		Title: "git",
		Run: func(ctx context.Context) doctor.Finding {
			v, err := a.d.Git.Version(ctx)
			if err != nil {
				return doctor.Finding{Status: doctor.Fail, Summary: "can't run git", Details: []string{err.Error()},
					Steps: []string{"install git " + git.MinVersion + " or later"}}
			}
			if v == "" {
				return doctor.Finding{Status: doctor.Warn, Summary: "can't tell which version of git this is; git-stack needs " + git.MinVersion + " or later"}
			}
			if version.Compare("v"+v, "v"+git.MinVersion) < 0 {
				return doctor.Finding{Status: doctor.Fail, Summary: fmt.Sprintf("git %s is older than %s, which git-stack needs", v, git.MinVersion),
					Steps: []string{"upgrade git"}}
			}
			return doctor.Finding{Status: doctor.OK, Summary: "git " + v}
		},
	}
}

// stackChecks looks at the stacks themselves.
func (a *App) stackChecks(repo git.Repo) doctor.Check {
	var graph *stack.Graph
	return doctor.Check{
		ID:    "stacks",
		Title: "stacks",
		Run: func(ctx context.Context) doctor.Finding {
			g, err := a.d.Meta.Load(ctx, repo)
			if err != nil {
				return doctor.Finding{Status: doctor.Fail, Summary: "can't read the stacks", Details: []string{errText(err)}}
			}
			graph = g
			n := 0
			for _, s := range g.Stacks {
				n += len(s.Branches)
			}
			return doctor.Finding{Status: doctor.OK, Summary: fmt.Sprintf("%d %s in %d %s",
				n, pluralise(n, "branch", "branches"), len(g.Stacks), pluralise(len(g.Stacks), "stack", "stacks"))}
		},
		Sub: []doctor.Check{
			{
				ID:    "stacks.restack",
				Title: "no restack is waiting to be finished",
				Run: func(ctx context.Context) doctor.Finding {
					err := a.noRebaseActive(ctx, repo)
					if err == nil {
						return doctor.Finding{Status: doctor.OK, Summary: "nothing in progress"}
					}
					if se, ok := errors.AsType[*stack.Error](err); ok {
						return doctor.Finding{Status: doctor.Warn, Summary: se.Msg, Steps: se.NextSteps}
					}
					return doctor.Finding{Status: doctor.Fail, Summary: err.Error()}
				},
			},
			a.missingCheck(repo, &graph),
			a.emptyCheck(repo, &graph),
		},
	}
}

// missingCheck finds tracked branches that no longer exist locally (deleted
// by hand); sync would drop them too, this does it without the network.
func (a *App) missingCheck(repo git.Repo, graph **stack.Graph) doctor.Check {
	missing := func(ctx context.Context) ([]string, error) {
		branches, err := a.d.Git.Branches(ctx, repo)
		if err != nil {
			return nil, err
		}
		have := map[string]bool{}
		for _, b := range branches {
			have[b.Name] = true
		}
		var out []string
		for _, s := range (*graph).Stacks {
			for _, b := range s.Branches {
				if !have[b.Name] {
					out = append(out, b.Name)
				}
			}
		}
		return out, nil
	}
	return doctor.Check{
		ID:    "stacks.missing",
		Title: "every tracked branch exists",
		Run: func(ctx context.Context) doctor.Finding {
			names, err := missing(ctx)
			if err != nil {
				return doctor.Finding{Status: doctor.Fail, Summary: "can't list branches", Details: []string{err.Error()}}
			}
			if len(names) == 0 {
				return doctor.Finding{Status: doctor.OK, Summary: "they all do"}
			}
			return doctor.Finding{Status: doctor.Warn,
				Summary: fmt.Sprintf("%d tracked %s no longer %s", len(names), pluralise(len(names), "branch", "branches"), pluralise(len(names), "exists", "exist")),
				Details: names, Steps: []string{"git stack sync drops them too"}}
		},
		Fix: func(ctx context.Context) error {
			names, err := missing(ctx)
			if err != nil {
				return err
			}
			return a.d.Meta.Update(ctx, repo, func(g *stack.Graph) error {
				for i := range g.Stacks {
					g.Stacks[i].Branches = slices.DeleteFunc(g.Stacks[i].Branches, func(b stack.Branch) bool { return slices.Contains(names, b.Name) })
				}
				return nil
			})
		},
		FixLabel: "stop tracking them",
	}
}

// emptyCheck finds branches with no commits of their own under other
// branches, which can't get a pull request for the ones above to stack on.
func (a *App) emptyCheck(repo git.Repo, graph **stack.Graph) doctor.Check {
	return doctor.Check{
		ID:    "stacks.empty",
		Title: "no empty branch in the middle of a stack",
		Run: func(ctx context.Context) doctor.Finding {
			f := doctor.Finding{Status: doctor.OK, Summary: "none"}
			for _, s := range (*graph).Stacks {
				var empty []string
				parent := s.Trunk
				for i, b := range s.Branches {
					n, err := a.d.Git.CountCommits(ctx, repo, parent, b.Name)
					parent = b.Name
					if err == nil && n == 0 && !b.Merged() && i < len(s.Branches)-1 {
						empty = append(empty, b.Name)
					}
				}
				if len(empty) == 0 {
					continue
				}
				f.Status = doctor.Warn
				order := slices.DeleteFunc(s.Names(), func(n string) bool { return slices.Contains(empty, n) })
				order = append(order, empty...)
				for i, n := range order {
					order[i], _ = shell.Arg(n)
				}
				f.Details = append(f.Details, fmt.Sprintf("%s in %s's stack", joinNames(empty), s.Bottom()))
				bottom, _ := shell.Arg(s.Bottom())
				f.Steps = append(f.Steps, fmt.Sprintf("git stack checkout %s, then git stack reorder %s", bottom, strings.Join(order, " ")))
			}
			if f.Status == doctor.Warn {
				f.Summary = "a branch with no commits of its own sits under others; submit stops there"
			}
			return f
		},
	}
}
