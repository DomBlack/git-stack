package agents

import (
	"context"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
)

func argsOf(f *exectest.Fake, name string) []string {
	var out []string
	for _, c := range f.CallsTo(name) {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

func TestClaudeEnsure(t *testing.T) {
	ctx := context.Background()
	exe := "/usr/local/bin/git-stack"

	// Missing → add, with flags before "--".
	f := exectest.New()
	f.On("claude", "mcp", "get").Fail(1, `No MCP server named "git-stack". Configured servers: x`)
	f.On("claude", "mcp", "add").Reply("Added stdio MCP server git-stack")
	act, err := Ensure(ctx, NewClaude(f), exe)
	if err != nil || act != ActionAdded {
		t.Fatalf("add: %v %v", act, err)
	}
	if got := argsOf(f, "claude")[1]; got != "mcp add --scope user git-stack -- /usr/local/bin/git-stack mcp" {
		t.Errorf("add args = %q", got)
	}

	// Same path → unchanged.
	f = exectest.New()
	f.On("claude", "mcp", "get").Reply("git-stack:\n  Scope: User config\n  Status: ✓ Connected\n  Type: stdio\n  Command: /usr/local/bin/git-stack\n  Args: mcp\n")
	if act, err := Ensure(ctx, NewClaude(f), exe); err != nil || act != ActionUnchanged {
		t.Errorf("unchanged: %v %v", act, err)
	}

	// Different path → remove then add.
	f = exectest.New()
	f.On("claude", "mcp", "get").Reply("git-stack:\n  Command: /old/git-stack\n  Args: mcp\n")
	f.On("claude", "mcp", "remove").Reply("")
	f.On("claude", "mcp", "add").Reply("")
	if act, err := Ensure(ctx, NewClaude(f), exe); err != nil || act != ActionReplaced {
		t.Errorf("replaced: %v %v", act, err)
	}
	got := argsOf(f, "claude")
	if got[1] != "mcp remove --scope user git-stack" || !strings.HasPrefix(got[2], "mcp add") {
		t.Errorf("replace calls = %v", got)
	}

	// Other failures propagate.
	f = exectest.New()
	f.On("claude", "mcp", "get").Fail(1, "some other error")
	if _, err := Ensure(ctx, NewClaude(f), exe); err == nil {
		t.Error("expected error")
	}
	f = exectest.New()
	f.On("claude", "mcp", "remove").Fail(1, "No MCP server named git-stack")
	if err := NewClaude(f).Remove(ctx); err != nil {
		t.Errorf("remove missing should be ok: %v", err)
	}
}

func TestCodexEnsureAndAvailable(t *testing.T) {
	ctx := context.Background()
	f := exectest.New()
	f.On("codex", "mcp", "get").Fail(1, "Error: No MCP server named 'git-stack' found.")
	f.On("codex", "mcp", "add").Reply("")
	if act, err := Ensure(ctx, NewCodex(f), "/bin/gs"); err != nil || act != ActionAdded {
		t.Fatalf("add: %v %v", act, err)
	}
	if got := argsOf(f, "codex")[1]; got != "mcp add git-stack -- /bin/gs mcp" {
		t.Errorf("add args = %q", got)
	}

	f = exectest.New()
	f.On("codex", "mcp", "get").Reply(`{"name":"git-stack","enabled":true,"transport":{"type":"stdio","command":"/bin/gs","args":["mcp"]}}`)
	if act, err := Ensure(ctx, NewCodex(f), "/bin/gs"); err != nil || act != ActionUnchanged {
		t.Errorf("unchanged: %v %v", act, err)
	}
	f = exectest.New()
	f.On("codex", "mcp", "get").Reply(`{"transport":{"command":"/other"}}`)
	f.On("codex", "mcp", "remove").Reply("")
	f.On("codex", "mcp", "add").Reply("")
	if act, err := Ensure(ctx, NewCodex(f), "/bin/gs"); err != nil || act != ActionReplaced {
		t.Errorf("replaced: %v %v", act, err)
	}
	if got := argsOf(f, "codex"); got[1] != "mcp remove git-stack" {
		t.Errorf("remove args = %v", got)
	}

	f = exectest.New()
	f.On("codex").NotFound()
	if Available(ctx, f, NewCodex(f)) {
		t.Error("missing codex reported available")
	}
	f = exectest.New()
	f.On("claude", "--version").Reply("2.1.284 (Claude Code)")
	if !Available(ctx, f, NewClaude(f)) {
		t.Error("claude should be available")
	}
	_ = exec.Capture
}
