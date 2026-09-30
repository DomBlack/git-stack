package agents

import (
	"context"
	"encoding/json/v2"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Codex registers with OpenAI Codex (`codex mcp`, which writes
// [mcp_servers.git-stack] to ~/.codex/config.toml).
type Codex struct {
	run exec.Runner
}

// NewCodex returns the Codex agent.
func NewCodex(r exec.Runner) *Codex { return &Codex{run: r} }

func (c *Codex) Name() string   { return "Codex" }
func (c *Codex) Binary() string { return "codex" }

type codexServer struct {
	Transport struct {
		Command string `json:"command"`
	} `json:"transport"`
}

// Get runs `codex mcp get git-stack --json`.
func (c *Codex) Get(ctx context.Context) (Registration, error) {
	res, err := c.run.Run(ctx, exec.Cmd{Name: "codex", Args: []string{"mcp", "get", ServerName, "--json"}})
	if err != nil {
		if isNotFound(err) {
			return Registration{}, nil
		}
		return Registration{}, err
	}
	reg := Registration{Present: true}
	var s codexServer
	if err := json.Unmarshal(res.Stdout, &s); err == nil {
		reg.Command = s.Transport.Command
	}
	return reg, nil
}

// Add runs `codex mcp add git-stack -- <exe> mcp`.
func (c *Codex) Add(ctx context.Context, exe string) error {
	_, err := c.run.Run(ctx, exec.Cmd{Name: "codex", Args: []string{"mcp", "add", ServerName, "--", exe, "mcp"}})
	return err
}

// Remove runs `codex mcp remove git-stack`.
func (c *Codex) Remove(ctx context.Context) error {
	_, err := c.run.Run(ctx, exec.Cmd{Name: "codex", Args: []string{"mcp", "remove", ServerName}})
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}
