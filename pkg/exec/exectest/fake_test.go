package exectest_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
)

func TestFakeMatchesLongestRecentRule(t *testing.T) {
	f := exectest.New()
	f.On("gh", "stack").Reply("generic")
	f.On("gh", "stack", "view").Reply("view")

	res, err := f.Run(context.Background(), exec.Cmd{Name: "gh", Args: []string{"stack", "view", "--json"}})
	if err != nil || res.Out() != "view" {
		t.Fatalf("got %q, %v; want view", res.Out(), err)
	}
	res, err = f.Run(context.Background(), exec.Cmd{Name: "gh", Args: []string{"stack", "add"}})
	if err != nil || res.Out() != "generic" {
		t.Fatalf("got %q, %v; want generic", res.Out(), err)
	}
	if n := len(f.CallsTo("gh")); n != 2 {
		t.Errorf("recorded %d calls, want 2", n)
	}
}

func TestFakeFailAndUnexpected(t *testing.T) {
	f := exectest.New()
	f.On("gh", "stack", "rebase").Fail(3, "conflict in a.go")

	_, err := f.Run(context.Background(), exec.Cmd{Name: "gh", Args: []string{"stack", "rebase"}, Stdin: strings.NewReader("x")})
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok || ee.Result.ExitCode != 3 || ee.Result.Err() != "conflict in a.go" {
		t.Fatalf("got %v", err)
	}
	if f.Calls()[0].Stdin != "x" {
		t.Errorf("stdin not recorded")
	}

	_, err = f.Run(context.Background(), exec.Cmd{Name: "claude"})
	if err == nil || !strings.Contains(err.Error(), "unexpected command") {
		t.Fatalf("unexpected command should fail, got %v", err)
	}
}
