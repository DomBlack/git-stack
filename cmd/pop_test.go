package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestPopCommand(t *testing.T) {
	dir, run := stackOf3(t)
	gittest.Run(t, dir, "switch", "-q", "b")
	out, errOut, err := run("pop")
	if err != nil || !strings.Contains(out, "ok: Popped b; its changes are staged on a") || !strings.Contains(out, "Restacked c") {
		t.Errorf("pop: %q %q %v", out, errOut, err)
	}
	if got := gittest.Run(t, dir, "diff", "--cached", "--name-only"); got != "b.txt" {
		t.Errorf("staged = %q", got)
	}
	gittest.Run(t, dir, "commit", "-q", "-m", "b again")
	gittest.Run(t, dir, "switch", "-q", "c")
	if _, _, err = run("pop"); err == nil || !strings.Contains(err.Error(), "c isn't on a yet") {
		t.Errorf("c is no longer on a: %v", err)
	}
}

func TestPopCommandRemovesTheLastBranch(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)
	gittest.WriteFile(t, dir, "a.txt", "a")
	if _, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "-m", "feat: a", "a"); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runWith(t, f, "--cwd", dir, "pop")
	if err != nil || !strings.Contains(out, "ok: Popped a; its changes are staged on main (the stack is gone)") {
		t.Errorf("pop: %q %q %v", out, errOut, err)
	}
}
