// Package doctor holds the shape of git-stack's health checks: what a check
// looks at, what it found and how it can put it right. The checks themselves
// live with whatever they check (the backend checks gh and gh-stack, the use
// cases check the stacks) and pkg/app runs them.
package doctor

import (
	"context"

	"github.com/DomBlack/git-stack/pkg/git"
)

// Status is the outcome of one check.
type Status string

const (
	// OK: nothing wrong.
	OK Status = "ok"
	// Warn: it works, but something is worth knowing or tidying.
	Warn Status = "warn"
	// Fail: something git-stack needs is missing or broken.
	Fail Status = "fail"
	// Skipped: not run, because the check it hangs off failed or doesn't
	// apply here (outside a repository, say).
	Skipped Status = "skipped"
	// Fixed: it failed or warned, and --fix put it right.
	Fixed Status = "fixed"
)

// Finding is what a check saw.
type Finding struct {
	Status Status
	// Summary is one line: "gh-stack v0.3.0", "2 branches have no commits".
	Summary string
	// Details are extra lines, one idea each (a branch, a file).
	Details []string
	// Steps are things the user can run to deal with it by hand.
	Steps []string
}

// Check is one thing to look at, with the checks that only make sense once
// it passes.
type Check struct {
	// ID is stable and machine readable ("gh-stack.version").
	ID string
	// Title names what is checked ("gh-stack is up to date").
	Title string
	// Run looks and reports; it must not change anything.
	Run func(ctx context.Context) Finding
	// Fix tries to put right what Run found wrong; nil when that has to be
	// done by hand. FixLabel says what it will do ("gh extension upgrade
	// stack").
	Fix      func(ctx context.Context) error
	FixLabel string
	// Sub run after this check unless it failed.
	Sub []Check
}

// Result is a check that has run.
type Result struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Status  Status   `json:"status"`
	Summary string   `json:"summary,omitempty"`
	Details []string `json:"details,omitempty"`
	Steps   []string `json:"next_steps,omitempty"`
	// Fix is what --fix does (or did) about it, when it can do anything.
	Fix string `json:"fix,omitempty"`
	// FixError is why the fix didn't work, when one was tried and failed.
	FixError string `json:"fix_error,omitempty"`
	// Parent is the ID of the check this one hangs off, in a Flatten list.
	Parent string `json:"parent,omitempty"`
	// Checks are the checks that hang off this one. They are left out of
	// JSON (a schema can't describe the recursion); Flatten lists them
	// with Parent set instead.
	Checks []Result `json:"-"`
}

// Flatten lists every result depth first, each with Parent set and its
// Checks left out.
func Flatten(results []Result) []Result {
	var out []Result
	var walk func(rs []Result, parent string)
	walk = func(rs []Result, parent string) {
		for _, r := range rs {
			sub := r.Checks
			r.Checks, r.Parent = nil, parent
			out = append(out, r)
			walk(sub, r.ID)
		}
	}
	walk(results, "")
	return out
}

// Provider contributes checks. repo is nil outside a repository; checks
// that need one are left out then.
type Provider interface {
	Checks(repo *git.Repo) []Check
}
