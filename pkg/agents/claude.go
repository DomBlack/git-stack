package agents

import (
	"context"
	"regexp"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Claude registers with Claude Code (`claude mcp`, user scope, stored in
// ~/.claude.json by the CLI itself).
type Claude struct {
	run exec.Runner
}

// NewClaude returns the Claude Code agent.
func NewClaude(r exec.Runner) *Claude { return &Claude{run: r} }

func (c *Claude) Name() string   { return "Claude Code" }
func (c *Claude) Binary() string { return "claude" }

var reClaudeCommand = regexp.MustCompile(`(?im)^\s*Command:\s*(.+?)\s*$`)

// Get runs `claude mcp get git-stack`. Its output is human-readable; the
// command line is on a "Command:" line.
func (c *Claude) Get(ctx context.Context) (Registration, error) {
	res, err := c.run.Run(ctx, exec.Cmd{Name: "claude", Args: []string{"mcp", "get", ServerName}})
	if err != nil {
		if isNotFound(err) {
			return Registration{}, nil
		}
		return Registration{}, err
	}
	reg := Registration{Present: true}
	if m := reClaudeCommand.FindStringSubmatch(res.Out()); m != nil {
		reg.Command = firstPath(m[1])
	}
	return reg, nil
}

// Add runs `claude mcp add --scope user git-stack -- <exe> mcp`. Flags go
// before the "--"; everything after it is our command line.
func (c *Claude) Add(ctx context.Context, exe string) error {
	_, err := c.run.Run(ctx, exec.Cmd{Name: "claude", Args: []string{"mcp", "add", "--scope", "user", ServerName, "--", exe, "mcp"}})
	return err
}

// Remove runs `claude mcp remove --scope user git-stack`.
func (c *Claude) Remove(ctx context.Context) error {
	_, err := c.run.Run(ctx, exec.Cmd{Name: "claude", Args: []string{"mcp", "remove", "--scope", "user", ServerName}})
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}
