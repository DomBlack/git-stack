// Package exectest provides a scripted exec.Runner for unit tests.
package exectest

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Call records one invocation of the fake runner.
type Call struct {
	Name  string
	Args  []string
	Dir   string
	Mode  exec.Mode
	Stdin string
}

// String renders the call as a command line.
func (c Call) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

type rule struct {
	name   string
	prefix []string
	fn     func(exec.Cmd) (exec.Result, error)
}

// Fake is an exec.Runner that replays scripted results and records calls.
// Rules are matched by executable name and argument prefix; the most recently
// added matching rule wins.
type Fake struct {
	mu    sync.Mutex
	calls []Call
	rules []rule

	// Fallback handles commands no rule matches. By default it fails the
	// call with an "unexpected command" error.
	Fallback func(exec.Cmd) (exec.Result, error)
}

// New returns an empty Fake.
func New() *Fake {
	return &Fake{}
}

// Stub configures the reply for one rule.
type Stub struct {
	f *Fake
	i int
}

// On registers a rule for name whose arguments start with argPrefix.
func (f *Fake) On(name string, argPrefix ...string) *Stub {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{name: name, prefix: argPrefix, fn: func(exec.Cmd) (exec.Result, error) {
		return exec.Result{}, nil
	}})
	return &Stub{f: f, i: len(f.rules) - 1}
}

// Reply makes the rule succeed with the given stdout.
func (s *Stub) Reply(stdout string) *Stub {
	return s.Do(func(exec.Cmd) (exec.Result, error) {
		return exec.Result{Stdout: []byte(stdout)}, nil
	})
}

// Fail makes the rule exit with code and the given stderr, returned as an
// *exec.ExitError like the real runner.
func (s *Stub) Fail(code int, stderr string) *Stub {
	return s.Do(func(c exec.Cmd) (exec.Result, error) {
		res := exec.Result{Stderr: []byte(stderr), ExitCode: code}
		return res, &exec.ExitError{Cmd: c, Result: res}
	})
}

// NotFound makes the rule report a missing executable.
func (s *Stub) NotFound() *Stub {
	return s.Do(func(c exec.Cmd) (exec.Result, error) {
		return exec.Result{ExitCode: -1}, &exec.NotFoundError{Name: c.Name}
	})
}

// Do installs an arbitrary handler.
func (s *Stub) Do(fn func(exec.Cmd) (exec.Result, error)) *Stub {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.rules[s.i].fn = fn
	return s
}

// Run implements exec.Runner.
func (f *Fake) Run(_ context.Context, c exec.Cmd) (exec.Result, error) {
	call := Call{Name: c.Name, Args: slices.Clone(c.Args), Dir: c.Dir, Mode: c.Mode}
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		call.Stdin = string(b)
	}

	f.mu.Lock()
	f.calls = append(f.calls, call)
	var fn func(exec.Cmd) (exec.Result, error)
	for i := len(f.rules) - 1; i >= 0; i-- {
		r := f.rules[i]
		if r.name == c.Name && hasPrefix(c.Args, r.prefix) {
			fn = r.fn
			break
		}
	}
	fallback := f.Fallback
	f.mu.Unlock()

	if fn != nil {
		return fn(c)
	}
	if fallback != nil {
		return fallback(c)
	}
	return exec.Result{ExitCode: -1}, fmt.Errorf("exectest: unexpected command %q", c.String())
}

// Calls returns every recorded call.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// CallsTo returns the recorded calls for the given executable.
func (f *Fake) CallsTo(name string) []Call {
	var out []Call
	for _, c := range f.Calls() {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// Reset forgets recorded calls but keeps the rules.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func hasPrefix(args, prefix []string) bool {
	if len(prefix) > len(args) {
		return false
	}
	return slices.Equal(args[:len(prefix)], prefix)
}
