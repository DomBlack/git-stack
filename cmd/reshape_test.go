package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

// stackOf3 makes main -> a -> b -> c through create and leaves HEAD on c.
func stackOf3(t *testing.T) (string, func(args ...string) (string, string, error)) {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)
	for _, n := range []string{"a", "b", "c"} {
		gittest.WriteFile(t, dir, n+".txt", n)
		if _, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "-m", "feat: "+n, n); err != nil {
			t.Fatal(err)
		}
	}
	return dir, func(args ...string) (string, string, error) {
		t.Helper()
		return runWith(t, f, append([]string{"--cwd", dir}, args...)...)
	}
}

func TestReorderCommand(t *testing.T) {
	dir, run := stackOf3(t)
	out, errOut, err := run("reorder", "b", "a", "c")
	if err != nil || !strings.Contains(out, "ok: Reordered the stack: b, a, c") || !strings.Contains(out, "Restacked b, a, c") {
		t.Errorf("reorder: %q %q %v", out, errOut, err)
	}
	if _, err := gittest.RunErr(t, dir, "cat-file", "-e", "b:a.txt"); err == nil {
		t.Error("b still has a's file")
	}
	out, _, err = run("reorder", "b", "a", "c")
	if err != nil || !strings.Contains(out, "Nothing to reorder") {
		t.Errorf("same order: %q %v", out, err)
	}
	if _, _, err = run("reorder"); err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Errorf("no args without a terminal: %v", err)
	}
	if _, _, err = run("reorder", "a", "b"); err == nil || !strings.Contains(err.Error(), "leaves out c") {
		t.Errorf("bad order: %v", err)
	}
}

func TestMoveCommand(t *testing.T) {
	dir, run := stackOf3(t)
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.WriteFile(t, dir, "x.txt", "x")
	if _, _, err := run("create", "-a", "-m", "feat: x", "x"); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := run("move", "--source", "b", "--onto", "x", "--only")
	if err != nil || !strings.Contains(out, "ok: Moved b onto x") || !strings.Contains(out, "c stays on a") || !strings.Contains(out, "Restacked b, c") {
		t.Errorf("move: %q %q %v", out, errOut, err)
	}
	gittest.Run(t, dir, "switch", "-q", "c")
	out, _, err = run("move", "--onto", "main")
	if err != nil || !strings.Contains(out, "ok: Moved c onto main (new stack)") {
		t.Errorf("new stack: %q %v", out, err)
	}
	if _, _, err = run("move", "--onto", "main"); err == nil || !strings.Contains(err.Error(), "already on") {
		t.Errorf("no-op: %v", err)
	}
	if _, _, err = run("move"); err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Errorf("picker without a terminal: %v", err)
	}
}

func TestInitCommand(t *testing.T) {
	dir, run := stackOf3(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "p", "main")
	gittest.Commit(t, dir, "p.txt", "p", "p")
	gittest.Run(t, dir, "switch", "-q", "-c", "q", "main")
	gittest.Commit(t, dir, "q.txt", "q", "q")
	out, errOut, err := run("init", "p", "q")
	if err != nil || !strings.Contains(out, "ok: Tracking p, q on main") || !strings.Contains(errOut, "q is not on p") {
		t.Errorf("init: %q %q %v", out, errOut, err)
	}
	out, _, err = run("log")
	if err != nil || !strings.Contains(out, "● q\n") || !strings.Contains(out, "○ p\n") {
		t.Errorf("log after init: %q %v", out, err)
	}
	if _, _, err = run("init", "a"); err == nil || !strings.Contains(err.Error(), "already in the stack") {
		t.Errorf("tracked branch: %v", err)
	}
}
