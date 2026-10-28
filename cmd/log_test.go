package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
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
	aSHA := gittest.Run(t, dir, "rev-parse", "--short=7", "a")
	for _, want := range []string{"○ c\n", "● b\n", "○ a\n", "#41", "■ main", "│  • " + aSHA + " - a\n"} {
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

// TestLogWithNoStacksShowsTrunk checks that a repository with no stacks yet
// still renders its trunk, so there is somewhere to start from.
func TestLogWithNoStacksShowsTrunk(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)

	out, errOut, err := runWith(t, f, "--cwd", dir)
	if err != nil {
		t.Fatalf("bare git stack: %v\n%s", err, errOut)
	}
	if want := "● main"; !strings.HasPrefix(out, want) {
		t.Errorf("log should show the trunk, got %q", out)
	}
	if strings.Count(out, "\n") != 1 || strings.Contains(out, "○") {
		t.Errorf("only the trunk line should be shown:\n%s", out)
	}
	if strings.Contains(errOut, "No stacks") {
		t.Errorf("no warning when the trunk is shown:\n%s", errOut)
	}
}

// TestLogShortAndStack checks that log short shows only the branch names and
// that --stack keeps to the current branch's stack, on log and log short.
func TestLogShortAndStack(t *testing.T) {
	dir := stackRepo(t) // main -> a (#41) -> b -> c, on b
	gittest.Run(t, dir, "switch", "-q", "-c", "other", "main")
	gittest.Commit(t, dir, "other.txt", "other", "other")
	gittest.Run(t, dir, "switch", "-q", "b")
	gitDir := gittest.Run(t, dir, "rev-parse", "--absolute-git-dir")
	meta := `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[
		{"branch":"a","pullRequest":{"number":41,"url":"https://example.com/41"}},{"branch":"b"},{"branch":"c"}]},
		{"trunk":{"branch":"main"},"branches":[{"branch":"other"}]}]}`
	if err := os.WriteFile(filepath.Join(gitDir, "gh-stack"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	f := fakeGh(t, dir)

	out, errOut, err := runWith(t, f, "--cwd", dir, "log", "short")
	if err != nil {
		t.Fatalf("log short: %v\n%s", err, errOut)
	}
	if want := "○ c\n● b\n○ a\n│ ○ other\n├─┘\n■ main\n"; out != want {
		t.Errorf("log short =\n%s\nwant\n%s", out, want)
	}

	for _, args := range [][]string{{"log", "short", "--stack"}, {"log", "-s", "short"}} {
		out, errOut, err = runWith(t, f, append([]string{"--cwd", dir}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, errOut)
		}
		if want := "○ c\n● b\n○ a\n■ main\n"; out != want {
			t.Errorf("%v =\n%s\nwant\n%s", args, out, want)
		}
	}

	out, errOut, err = runWith(t, f, "--cwd", dir, "log", "-s")
	if err != nil {
		t.Fatalf("log -s: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "#41") || strings.Contains(out, "other") {
		t.Errorf("log -s should show the full current stack only:\n%s", out)
	}

	// From the trunk, every stack on it.
	gittest.Run(t, dir, "switch", "-q", "main")
	if out, _, _ = runWith(t, f, "--cwd", dir, "log", "short", "-s"); !strings.Contains(out, "other") || !strings.Contains(out, "○ c") {
		t.Errorf("log short -s on trunk shows its stacks:\n%s", out)
	}

	// An untracked branch has no stack, so everything is shown and we say so.
	gittest.Run(t, dir, "switch", "-q", "loose")
	out, errOut, err = runWith(t, f, "--cwd", dir, "log", "short", "-s")
	if err != nil || !strings.Contains(out, "other") || !strings.Contains(errOut, "loose is in no stack") {
		t.Errorf("log short -s on an untracked branch:\n%s\n%s (%v)", out, errOut, err)
	}

	// Moving trunk on leaves the bottom branches needing a restack.
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "main.txt", "more", "more")
	out, errOut, err = runWith(t, f, "--cwd", dir, "log", "short")
	if err != nil {
		t.Fatalf("log short after trunk moved: %v\n%s", err, errOut)
	}
	if want := "○ c\n○ b\n○ a (needs restack)\n│ ○ other (needs restack)\n├─┘\n● main\n"; out != want {
		t.Errorf("log short after trunk moved =\n%s\nwant\n%s", out, want)
	}
}

// TestLogShortConfig checks that stack.log.short makes log and bare git stack
// short, and that log long still shows everything.
func TestLogShortConfig(t *testing.T) {
	dir := stackRepo(t)
	gittest.Run(t, dir, "config", "stack.log.short", "true")
	f := fakeGh(t, dir)

	short := "○ c\n● b\n○ a\n■ main\n"
	for _, args := range [][]string{nil, {"log"}, {"log", "short"}} {
		out, errOut, err := runWith(t, f, append([]string{"--cwd", dir}, args...)...)
		if err != nil || out != short {
			t.Errorf("%v with stack.log.short =\n%s\nwant\n%s (%v %s)", args, out, short, err, errOut)
		}
	}
	for _, args := range [][]string{{"log", "long"}, {"log", "long", "-s"}} {
		out, errOut, err := runWith(t, f, append([]string{"--cwd", dir}, args...)...)
		if err != nil || !strings.Contains(out, "#41") || !strings.Contains(out, "│  • ") {
			t.Errorf("%v should show the full log:\n%s (%v %s)", args, out, err, errOut)
		}
	}
}
