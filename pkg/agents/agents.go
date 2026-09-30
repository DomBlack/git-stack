// Package agents registers the git-stack MCP server with coding-agent CLIs.
// Each agent's own CLI is used (`claude mcp`, `codex mcp`); their config
// files are never edited directly.
package agents

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// ServerName is the MCP server name registered with every agent.
const ServerName = "git-stack"

// Registration describes what an agent currently has for the server.
type Registration struct {
	// Present is true when a server named ServerName exists.
	Present bool
	// Command is the registered executable path, when it could be read.
	Command string
}

// Agent is one coding-agent CLI.
type Agent interface {
	// Name is the human-readable agent name.
	Name() string
	// Binary is the CLI executable looked up on PATH.
	Binary() string
	// Get reads the current registration.
	Get(ctx context.Context) (Registration, error)
	// Add registers `exe mcp` at user scope.
	Add(ctx context.Context, exe string) error
	// Remove unregisters the server. A missing server is not an error.
	Remove(ctx context.Context) error
}

// All returns every supported agent.
func All(r exec.Runner) []Agent {
	return []Agent{NewClaude(r), NewCodex(r)}
}

// Available reports whether the agent's CLI is on PATH.
func Available(ctx context.Context, r exec.Runner, a Agent) bool {
	_, err := r.Run(ctx, exec.Cmd{Name: a.Binary(), Args: []string{"--version"}})
	var nf *exec.NotFoundError
	return !errors.As(err, &nf)
}

// Ensure makes the registration point at exe: it adds a missing server and
// re-adds one registered with a different path. It reports what it did.
func Ensure(ctx context.Context, a Agent, exe string) (Action, error) {
	reg, err := a.Get(ctx)
	if err != nil {
		return ActionNone, err
	}
	switch {
	case !reg.Present:
		return ActionAdded, a.Add(ctx, exe)
	case reg.Command == "" || reg.Command == exe:
		return ActionUnchanged, nil
	default:
		if err := a.Remove(ctx); err != nil {
			return ActionNone, err
		}
		return ActionReplaced, a.Add(ctx, exe)
	}
}

// Action is the outcome of Ensure.
type Action int

const (
	ActionNone Action = iota
	ActionAdded
	ActionUnchanged
	ActionReplaced
)

func (a Action) String() string {
	switch a {
	case ActionAdded:
		return "registered"
	case ActionUnchanged:
		return "already registered"
	case ActionReplaced:
		return "re-registered with the new path"
	default:
		return "unchanged"
	}
}

var reNoServer = regexp.MustCompile(`(?i)no (?:mcp )?server named|not found`)

// isNotFound reports whether a `get`/`remove` failure means "no such server".
func isNotFound(err error) bool {
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return false
	}
	return reNoServer.MatchString(ee.Result.Err() + " " + ee.Result.Out())
}

// firstPath extracts the first absolute path in s.
func firstPath(s string) string {
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, `",'`)
		if strings.HasPrefix(f, "/") {
			return f
		}
	}
	return ""
}
