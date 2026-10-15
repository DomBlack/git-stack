package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

// The three ways absorb can end: a dry run that only shows the plan, a
// run with nothing it can absorb, and one that rewrites a commit.
func TestAbsorbCommandBranches(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)
	gittest.WriteFile(t, dir, "f.txt", "1\n2\n3\n")
	if _, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "-m", "Add f"); err != nil {
		t.Fatal(err)
	}
	oldTip := gittest.Run(t, dir, "rev-parse", "HEAD")
	absorb := func(args ...string) (string, string, error) {
		t.Helper()
		return runWith(t, f, append([]string{"--cwd", dir, "absorb"}, args...)...)
	}

	// Nothing the stack changed is staged: only a new file.
	gittest.WriteFile(t, dir, "new.txt", "new\n")
	gittest.Run(t, dir, "add", "new.txt")
	out, errOut, err := absorb()
	if err != nil || !strings.Contains(out, "ok: Nothing to absorb\n") || !strings.Contains(out, "new.txt  skipped; new file") {
		t.Errorf("nothing to absorb: %q %q %v", out, errOut, err)
	}
	gittest.Run(t, dir, "reset", "-q", "new.txt")

	// A dry run shows where the change goes and rewrites nothing.
	gittest.WriteFile(t, dir, "f.txt", "1\ntwo\n3\n")
	gittest.Run(t, dir, "add", "f.txt")
	out, errOut, err = absorb("--dry-run")
	if err != nil || !strings.Contains(out, "f.txt:2  into ") || strings.Contains(out, "Absorbed") || errOut != "note: Dry run; nothing was absorbed\n" {
		t.Errorf("dry run: %q %q %v", out, errOut, err)
	}
	if gittest.Run(t, dir, "rev-parse", "HEAD") != oldTip {
		t.Error("a dry run rewrote the commit")
	}

	// The real thing prints the result block and moves the branch.
	out, errOut, err = absorb("--force")
	if err != nil || !strings.Contains(out, "ok: Absorbed 1 hunk into 1 commit\n") || !strings.Contains(out, "rewritten, was "+oldTip[:7]) || errOut != "Absorbing 1 hunk into 1 commit...\n" {
		t.Errorf("absorb: %q %q %v", out, errOut, err)
	}
	if gittest.Run(t, dir, "rev-parse", "HEAD") == oldTip || gittest.Run(t, dir, "show", "HEAD:f.txt") != "1\ntwo\n3" {
		t.Error("the commit was not rewritten with the change")
	}
}
