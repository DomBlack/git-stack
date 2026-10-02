package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

// TestAIAutoConfig checks stack.ai.auto: create and submit draft with Claude
// without --ai, and --no-ai still switches it off.
func TestAIAutoConfig(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "config", "stack.ai.auto", "true")
	f := fakeGh(t, dir)
	f.On("claude").Reply(`{"type":"result","is_error":false,"structured_output":{"branch":"add-adder","message":"feat: add adder\n\nBody.","title":"feat: add adder","body":"Body."}}`)
	f.On("gh", "pr", "edit").Reply("")

	// create without --ai drafts the name and message.
	gittest.WriteFile(t, dir, "adder.go", "package x")
	out, errOut, err := runWith(t, f, "--cwd", dir, "create", "-a")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, errOut)
	}
	if len(f.CallsTo("claude")) != 1 || !strings.Contains(out, "Created add-adder on main") {
		t.Errorf("auto AI should have drafted the branch: claude calls=%d out=%q", len(f.CallsTo("claude")), out)
	}

	// --no-ai wins over the config.
	f.Reset()
	gittest.WriteFile(t, dir, "b.txt", "b")
	if _, errOut, err := runWith(t, f, "--cwd", dir, "create", "-a", "--no-ai", "-m", "feat b"); err != nil {
		t.Fatalf("create --no-ai: %v\n%s", err, errOut)
	}
	if n := len(f.CallsTo("claude")); n != 0 {
		t.Errorf("--no-ai must not call claude, got %d calls", n)
	}

	// submit without --ai drafts PR text for the new branches.
	f.Reset()
	f.On("gh", "stack", "submit").Reply("")
	if _, errOut, err := runWith(t, f, "--cwd", dir, "submit", "--stack", "--no-edit"); err != nil {
		t.Fatalf("submit: %v\n%s", err, errOut)
	}
	if n := len(f.CallsTo("claude")); n == 0 {
		t.Error("auto AI should draft PR text on submit")
	}
}
