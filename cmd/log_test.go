package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
)

// TestBareGitStackShowsTheLog checks that running the binary with no
// subcommand renders the stacks gt log style, and that `log` is the same.
func TestBareGitStackShowsTheLog(t *testing.T) {
	dir := stackRepo(t) // main -> a (#41) -> b -> c, metadata on disk
	f := exectest.New()
	real := exec.New()
	f.Fallback = func(c exec.Cmd) (exec.Result, error) { return real.Run(context.Background(), c) }
	f.On("gh", "extension", "list").Reply("gh stack\tgithub/gh-stack\tv0.9.0\n")
	f.On("gh", "pr", "list").Reply("[]")

	out, errOut, err := runWith(t, f, "--cwd", dir)
	if err != nil {
		t.Fatalf("bare git stack: %v\n%s", err, errOut)
	}
	for _, want := range []string{"○ c\n", "● b\n", "○ a\n", "#41", "■ main"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q:\n%s", want, out)
		}
	}
	// Top of the stack first, trunk last.
	if strings.Index(out, "○ c") > strings.Index(out, "● b") || strings.Index(out, "● b") > strings.Index(out, "■ main") {
		t.Errorf("order should be top to trunk:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("piped output must have no escape codes:\n%q", out)
	}

	logOut, _, err := runWith(t, f, "--cwd", dir, "log")
	if err != nil || logOut != out {
		t.Errorf("git stack log should match bare git stack:\n%s\nvs\n%s (%v)", logOut, out, err)
	}
}
