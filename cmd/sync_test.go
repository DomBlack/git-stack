package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestSyncFlags(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)
	gittest.WriteFile(t, dir, "a.txt", "a")
	if _, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "-m", "feat a"); err != nil {
		t.Fatal(err)
	}
	gittest.InitRemote(t, dir)
	sync := func(args ...string) string {
		t.Helper()
		out, errOut, err := runWith(t, f, append([]string{"--cwd", dir, "sync"}, args...)...)
		if err != nil {
			t.Fatalf("sync %v: %v\n%s%s", args, err, out, errOut)
		}
		return out + errOut
	}
	// pushMain commits file on top of origin/main and pushes it.
	pushMain := func(file string) {
		t.Helper()
		gittest.Run(t, dir, "switch", "-q", "--detach", "origin/main")
		gittest.Commit(t, dir, file, file, "remote: "+file)
		gittest.Run(t, dir, "push", "-q", "origin", "HEAD:refs/heads/main")
		gittest.Run(t, dir, "switch", "-q", "feat-a")
	}

	// --no-restack moves trunk but leaves the stack. The forge round trip
	// gets its own headline so a slow one is never a silent pause.
	pushMain("r1.txt")
	if out := sync("--no-restack"); !strings.Contains(out, "main fast forwarded") || strings.Contains(out, "restacked feat-a") ||
		!strings.Contains(out, "Fetching origin...\nChecking pull requests on origin...\n") {
		t.Errorf("--no-restack: %q", out)
	}
	if out := sync(); !strings.Contains(out, "restacked feat-a") {
		t.Errorf("sync: %q", out)
	}
	if got := gittest.Run(t, dir, "log", "--format=%s", "main..feat-a"); got != "feat a" {
		t.Errorf("feat-a after restack = %q", got)
	}

	// -f resets a diverged trunk; without it there is only a warning.
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "local.txt", "l", "local only")
	gittest.Run(t, dir, "switch", "-q", "feat-a")
	pushMain("r2.txt")
	if out := sync("--no-restack"); !strings.Contains(out, "main has diverged") {
		t.Errorf("no -f: %q", out)
	}
	if out := sync("-f", "--no-restack"); !strings.Contains(out, "main reset to origin/main") {
		t.Errorf("-f: %q", out)
	}
	if gittest.Run(t, dir, "rev-parse", "main") != gittest.Run(t, dir, "rev-parse", "origin/main") {
		t.Error("-f should have moved main to origin/main")
	}

	// -d deletes what the policy would keep.
	gittest.Run(t, dir, "config", "stack.sync.prune", "never")
	sync()
	gittest.Run(t, dir, "push", "-q", "origin", "feat-a:main")
	pushMain("r3.txt")
	gittest.Run(t, dir, "switch", "-q", "main")
	if out := sync("--no-restack"); !strings.Contains(out, "stack.sync.prune is never") {
		t.Errorf("never: %q", out)
	}
	if out := sync("-d", "--no-restack"); !strings.Contains(out, "deleted feat-a (in-trunk, was ") {
		t.Errorf("-d: %q", out)
	}
	if gittest.Run(t, dir, "branch", "--list", "feat-a") != "" {
		t.Error("feat-a should be gone")
	}
}
