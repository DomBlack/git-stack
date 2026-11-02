package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestGetCommand(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "feat-a")
	gittest.Commit(t, dir, "a.txt", "a", "feat: a")
	gittest.InitRemote(t, dir)
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Run(t, dir, "branch", "-q", "-D", "feat-a")

	f := fakeGh(t, dir)
	f.On("gh", "stack", "checkout").Fail(2, "PR #7 is not part of a stack on GitHub")
	f.On("gh", "pr", "view", "7").Reply(`{"number":7,"url":"u/7","title":"A","state":"OPEN","isDraft":false,"isCrossRepository":false,"headRefName":"feat-a","baseRefName":"main"}`)
	run := func(args ...string) (string, string, error) {
		t.Helper()
		return runWith(t, f, append([]string{"--cwd", dir}, args...)...)
	}

	out, errOut, err := run("get", "#7")
	if err != nil || !strings.Contains(out, "ok: Tracking 1 branch on main and checked out feat-a") ||
		!strings.Contains(out, "isn't in a stack on GitHub") || !strings.Contains(out, "created feat-a from the remote") {
		t.Errorf("get: %q %q %v", out, errOut, err)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "feat-a" {
		t.Errorf("checked out %s", cur)
	}
	out, _, err = run("get", "feat-a")
	if err != nil || !strings.Contains(out, "ok: Checked out feat-a; its stack was already here") {
		t.Errorf("again: %q %v", out, err)
	}
	if _, _, err = run("get"); err == nil {
		t.Error("no target should fail")
	}
}
