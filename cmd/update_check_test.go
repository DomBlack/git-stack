package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/update"
	"github.com/DomBlack/git-stack/pkg/version"
)

// TestBackgroundUpdateCheck runs a command with a fake release server and
// checks the notice lands on stderr after the command's output, that the
// next run answers from the cache, and that the commands which must stay
// quiet do.
func TestBackgroundUpdateCheck(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	srv := releaseServer(t, "v9.9.9", "new binary")
	checker := &update.Checker{
		Client: &update.Client{HTTP: srv.Client(), BaseURL: srv.URL, Repo: "DomBlack/git-stack", UserAgent: "test"},
		Store:  cache.At(t.TempDir()),
	}
	run := func(f *exectest.Fake, args ...string) (*cli, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		c := &cli{
			streams:       Streams{In: strings.NewReader(""), Out: &out, Err: &errOut},
			newRunner:     func(...exec.Option) exec.Runner { return f },
			updateChecker: checker,
			current:       version.Info{Version: "v1.0.0", Release: true},
		}
		root := newRootCmd(c)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, errOut.String())
		}
		if c.check != nil {
			select {
			case <-c.check.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("background check did not finish")
			}
		}
		c.updateNotice()
		return c, errOut.String()
	}

	// First run learns about the release and says so once the command is done.
	c, errOut := run(fakeGh(t, dir), "--cwd", dir, "log")
	if c.check == nil || !strings.Contains(errOut, "note: git-stack v9.9.9 is out (you have v1.0.0); run git stack update") {
		t.Errorf("first run stderr = %q", errOut)
	}
	// The next run answers from the cache without going back to GitHub.
	srv.Close()
	if _, errOut = run(fakeGh(t, dir), "--cwd", dir, "log"); !strings.Contains(errOut, "v9.9.9") {
		t.Errorf("cached run stderr = %q", errOut)
	}
	// Machine facing commands and --quiet never start a check.
	for _, args := range [][]string{{"version"}, {"completion", "fish"}, {"--cwd", dir, "--quiet", "log"}} {
		if c, errOut := run(fakeGh(t, dir), args...); c.check != nil || strings.Contains(errOut, "v9.9.9") {
			t.Errorf("%v should not check for updates: %q", args, errOut)
		}
	}
	t.Setenv(noUpdateCheckEnv, "1")
	if c, _ := run(fakeGh(t, dir), "--cwd", dir, "log"); c.check != nil {
		t.Error("GIT_STACK_NO_UPDATE_CHECK should switch the check off")
	}
}
