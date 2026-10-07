package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/ui"
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

// The summary line is always true to what happened, and is a warning, not
// a success, when sync left something it meant to update.
func TestSyncSummary(t *testing.T) {
	ff := app.TrunkSync{Name: "main", Status: app.TrunkFastForwarded}
	for _, tc := range []struct {
		name        string
		res         app.SyncResult
		want        string
		wantPartial bool
	}{
		{"nothing", app.SyncResult{Trunks: []app.TrunkSync{{Name: "main", Status: app.TrunkUpToDate}}}, "Synced, nothing to do", false},
		{"trunk fast forwarded", app.SyncResult{Trunks: []app.TrunkSync{ff}}, "Synced: main fast forwarded", false},
		{"trunk reset", app.SyncResult{Remote: "origin", Trunks: []app.TrunkSync{{Name: "main", Status: app.TrunkReset}}}, "Synced: main reset to origin/main", false},
		{"everything", app.SyncResult{
			Trunks:    []app.TrunkSync{ff},
			Deleted:   []app.DeletedBranch{{Name: "a"}, {Name: "b"}},
			Updated:   []app.BranchMove{{Name: "c"}},
			Restacked: []app.BranchMove{{Name: "d"}, {Name: "e"}, {Name: "f"}},
		}, "Synced: main fast forwarded, 2 branches deleted, 1 branch updated from the remote, 3 restacked", false},
		{"trunk left by a dirty checkout", app.SyncResult{
			Trunks:     []app.TrunkSync{{Name: "main", Status: app.TrunkDirty}},
			NotUpdated: []app.NotUpdated{{Name: "main", Reason: app.NotUpdatedDirty}},
		}, "Synced, but main was not updated", true},
		{"trunk lock held", app.SyncResult{
			Trunks:     []app.TrunkSync{{Name: "main", Status: app.TrunkNotUpdated}},
			NotUpdated: []app.NotUpdated{{Name: "main", Reason: app.NotUpdatedLocked}},
		}, "Synced, but main was not updated", true},
		{"some done, some left", app.SyncResult{
			Restacked:  []app.BranchMove{{Name: "d"}},
			NotUpdated: []app.NotUpdated{{Name: "main", Reason: app.NotUpdatedUntracked}, {Name: "feat-a", Reason: app.NotUpdatedDirty}},
		}, "Synced: 1 restacked, but main and feat-a were not updated", true},
		{"diverged trunk kept", app.SyncResult{Remote: "origin", Trunks: []app.TrunkSync{{Name: "main", Status: app.TrunkDiverged}}},
			"Synced, but main has diverged from origin/main", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, partial := syncSummary(tc.res)
			if got != tc.want || partial != tc.wantPartial {
				t.Errorf("got %q (partial %v), want %q (partial %v)", got, partial, tc.want, tc.wantPartial)
			}
		})
	}
}

// What a sync prints, in order: the summary, then the details under it.
// A failed sync prints the details and leaves the verdict to the error.
func TestReportSync(t *testing.T) {
	full := "14ba05688e8bb83932f27db282928a4faf7d7d6a"
	res := app.SyncResult{Remote: "origin", Trunks: []app.TrunkSync{{Name: "main", To: full, Status: app.TrunkFastForwarded}}}
	var out, errOut bytes.Buffer
	rep := ui.NewReporter(strings.NewReader(""), &out, &errOut, ui.ReporterOptions{})
	if err := reportSync(rep, res, nil); err != nil {
		t.Fatal(err)
	}
	if want := "ok: Synced: main fast forwarded\n  main fast forwarded to 14ba056\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}

	out.Reset()
	failed := stack.New(stack.KindPartial, "main was not updated")
	if err := reportSync(rep, app.SyncResult{NotUpdated: []app.NotUpdated{{Name: "main", Reason: app.NotUpdatedLocked}}}, failed); !errors.Is(err, failed) {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(out.String(), "ok:") || strings.Contains(out.String(), "nothing to do") {
		t.Errorf("a failed sync must not claim success: %q", out.String())
	}
}

func TestFileList(t *testing.T) {
	for _, tc := range []struct {
		files []string
		more  int
		want  string
	}{
		{[]string{"a.go"}, 0, "a.go"},
		{[]string{"a.go", "b c.go"}, 0, "a.go and b c.go"},
		{[]string{"a.go", "b.go", "c.go"}, 0, "a.go, b.go and c.go"},
		{[]string{"a.go", "b.go", "c.go", "d.go", "e.go"}, 0, "a.go, b.go, c.go and 2 more"},
		{[]string{"a.go", "b.go", "c.go", "d.go"}, 16, "a.go, b.go, c.go and 17 more"}, // capped in the result already
	} {
		if got := fileList(tc.files, tc.more); got != tc.want {
			t.Errorf("fileList(%q, %d) = %q, want %q", tc.files, tc.more, got, tc.want)
		}
	}
}

// A branch left because the user's files are in the way: the summary, then
// straight under it which files, where, and what to run from here.
func TestReportSyncExplainsBlockedBranches(t *testing.T) {
	t.Setenv("HOME", "/home/dom")
	wt := "/home/dom/src/monorepo"
	five := []string{"a.go", "b.go", "c.go", "d.go", "e.go"}
	for _, tc := range []struct {
		name string
		res  app.SyncResult
		want string
	}{
		{"untracked file", app.SyncResult{
			Trunks:     []app.TrunkSync{{Name: "main", Status: app.TrunkDirty}},
			NotUpdated: []app.NotUpdated{{Name: "main", Worktree: wt, Reason: app.NotUpdatedUntracked, Untracked: []string{"notes.txt"}}},
		}, `note: Synced, but main was not updated
  main: untracked files in ~/src/monorepo are in the way: notes.txt
  - move or delete them, or git -C ~/src/monorepo stash -u, then git stack sync again
`},
		{"changed files", app.SyncResult{
			Trunks:     []app.TrunkSync{{Name: "main", Status: app.TrunkDirty}},
			NotUpdated: []app.NotUpdated{{Name: "main", Worktree: wt, Reason: app.NotUpdatedDirty, Changed: []string{"go.mod", "go.sum"}}},
		}, `note: Synced, but main was not updated
  main: uncommitted changes in ~/src/monorepo would be overwritten: go.mod and go.sum
  - commit them, or git -C ~/src/monorepo stash, then git stack sync again
`},
		{"both, five each, two branches", app.SyncResult{
			Restacked: []app.BranchMove{{Name: "feat-b"}},
			NotUpdated: []app.NotUpdated{
				{Name: "main", Worktree: wt, Reason: app.NotUpdatedDirty, Changed: five, Untracked: five},
				{Name: "feat-a", Worktree: "/srv/my checkout", Reason: app.NotUpdatedUntracked, Untracked: five},
			},
		}, `note: Synced: 1 restacked, but main and feat-a were not updated
  main: uncommitted changes in ~/src/monorepo would be overwritten: a.go, b.go, c.go and 2 more
  main: untracked files in ~/src/monorepo are in the way: a.go, b.go, c.go and 2 more
  - commit or move them, or git -C ~/src/monorepo stash -u, then git stack sync again
  feat-a: untracked files in /srv/my checkout are in the way: a.go, b.go, c.go and 2 more
  - move or delete them, or git -C '/srv/my checkout' stash -u, then git stack sync again
  restacked feat-b
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			rep := ui.NewReporter(strings.NewReader(""), &out, &errOut, ui.ReporterOptions{})
			if err := reportSync(rep, tc.res, nil); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.want {
				t.Errorf("got\n%s\nwant\n%s", out.String(), tc.want)
			}
			if errOut.Len() != 0 {
				t.Errorf("nothing about these belongs on stderr: %q", errOut.String())
			}
		})
	}
}

func TestFileListQuotesOddNames(t *testing.T) {
	if got := fileList([]string{"ok.go", "a\nb.go", "\x1b]2;x\a"}, 0); got != `ok.go, "a\nb.go" and "\x1b]2;x\a"` {
		t.Errorf("fileList quoted = %s", got)
	}
}

// A checkout whose path has a newline in it comes through git's worktree
// and branch listings whole, and the warning shows it quoted rather than
// split over two lines (or two commands, if pasted).
func TestSyncWarnsAboutAWorktreeWithANewlineInItsPath(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)
	gittest.WriteFile(t, dir, "a.txt", "a")
	if _, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "-m", "feat a"); err != nil {
		t.Fatal(err)
	}
	gittest.InitRemote(t, dir)
	base, err := filepath.EvalSymlinks(t.TempDir()) // git reports the real path (/private/var on macOS)
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(base, "main\nrm -rf x")
	gittest.Run(t, dir, "worktree", "add", "-q", wt, "main")
	// The remote changes README.md, which is edited in the odd checkout.
	gittest.Run(t, dir, "switch", "-q", "--detach", "origin/main")
	gittest.Commit(t, dir, "README.md", "remote\n", "remote: readme")
	gittest.Run(t, dir, "push", "-q", "origin", "HEAD:refs/heads/main")
	gittest.Run(t, dir, "switch", "-q", "feat-a")
	gittest.WriteFile(t, wt, "README.md", "local\n")

	out, errOut, err := runWith(t, f, "--cwd", dir, "sync", "--no-restack")
	if err != nil {
		t.Fatalf("sync: %v\n%s%s", err, out, errOut)
	}
	quoted := strconv.Quote(wt)
	for _, want := range []string{
		"note: Synced, but main was not updated\n",
		"  main: uncommitted changes in " + quoted + " would be overwritten: README.md\n",
		// No git -C: pasted, the newline would split it into two commands.
		"  - commit or stash them in the checkout at " + quoted + ", then git stack sync again\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "git -C") {
		t.Errorf("no pasteable command for a path that can't be pasted:\n%s", out)
	}
	if strings.Contains(out+errOut, "\nrm -rf x") {
		t.Errorf("the raw newline reached the output:\n%s%s", out, errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "README.md")); string(b) != "local\n" {
		t.Error("the local edit must be untouched")
	}
}
