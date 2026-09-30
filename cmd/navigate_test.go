package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

// stackRepo builds main -> a -> b -> c with a gh-stack metadata file and an
// untracked branch, and returns the repo path (HEAD on b).
func stackRepo(t *testing.T) string {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	for _, b := range []string{"a", "b", "c"} {
		gittest.Run(t, dir, "switch", "-q", "-c", b)
		gittest.Commit(t, dir, b+".txt", b, b)
	}
	gittest.Run(t, dir, "branch", "loose", "main")
	gittest.Run(t, dir, "switch", "-q", "b")
	gitDir := gittest.Run(t, dir, "rev-parse", "--absolute-git-dir")
	meta := `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[
		{"branch":"a","pullRequest":{"number":41,"url":"https://example.com/41"}},{"branch":"b"},{"branch":"c"}]}]}`
	if err := os.WriteFile(filepath.Join(gitDir, "gh-stack"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := NewRootCmd(testStreams(&out))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestNavigationCommands(t *testing.T) {
	dir := stackRepo(t)
	cur := func() string { return gittest.Run(t, dir, "branch", "--show-current") }

	out, err := run(t, "--cwd", dir, "up")
	if err != nil || out != "Checked out c.\n" || cur() != "c" {
		t.Fatalf("up: %q %v (on %s)", out, err, cur())
	}
	out, err = run(t, "--cwd", dir, "up")
	if err != nil || !strings.Contains(out, "Already at the top") || cur() != "c" {
		t.Fatalf("up at top: %q %v", out, err)
	}
	out, err = run(t, "--cwd", dir, "down", "2")
	if err != nil || out != "Checked out a.\n" || cur() != "a" {
		t.Fatalf("down 2: %q %v", out, err)
	}
	out, err = run(t, "--cwd", dir, "down")
	if err != nil || out != "Checked out main.\n" || cur() != "main" {
		t.Fatalf("down to trunk: %q %v", out, err)
	}
	out, err = run(t, "--cwd", dir, "d")
	if err != nil || !strings.Contains(out, "bottom most") {
		t.Fatalf("down from trunk: %q %v", out, err)
	}
	out, err = run(t, "--cwd", dir, "top")
	if err != nil || out != "Checked out c.\n" {
		t.Fatalf("top: %q %v", out, err)
	}
	out, err = run(t, "--cwd", dir, "bottom", "-q")
	if err != nil || out != "" || cur() != "a" {
		t.Fatalf("bottom -q: %q %v", out, err)
	}
	out, err = run(t, "--cwd", dir, "u", "-n", "2")
	if err != nil || out != "Checked out c.\n" {
		t.Fatalf("up -n 2: %q %v", out, err)
	}
	if _, err = run(t, "--cwd", dir, "up", "zero"); err == nil {
		t.Error("non-numeric steps should fail")
	}
	if _, err = run(t, "--cwd", dir, "up", "--steps", "0"); err == nil {
		t.Error("zero steps should fail")
	}

	gittest.Run(t, dir, "switch", "-q", "loose")
	if _, err = run(t, "--cwd", dir, "up"); err == nil || !strings.Contains(err.Error(), "not in a stack") {
		t.Errorf("untracked branch: %v", err)
	}
}

func TestCheckoutCommand(t *testing.T) {
	dir := stackRepo(t)
	out, err := run(t, "--cwd", dir, "checkout", "c")
	if err != nil || out != "Checked out c.\n" {
		t.Fatalf("checkout c: %q %v", out, err)
	}
	out, err = run(t, "--cwd", dir, "co", "--trunk")
	if err != nil || out != "Checked out main.\n" {
		t.Fatalf("checkout --trunk: %q %v", out, err)
	}
	if _, err = run(t, "--cwd", dir, "checkout", "nope"); err == nil {
		t.Error("unknown branch should fail")
	}
	_, err = run(t, "--cwd", dir, "checkout")
	if err == nil || !strings.Contains(err.Error(), "no branch given and no terminal for the picker") {
		t.Errorf("no-arg non-interactive: %v", err)
	}
}

func TestBranchAndStepCompletions(t *testing.T) {
	dir := stackRepo(t)

	out := runComplete(t, "--cwd", dir, "checkout", "")
	lines := nonEmpty(out)
	want := []string{
		"a\t#41 · stack: a",
		"b\tstack: a · current",
		"c\tstack: a",
		"main\ttrunk",
		"loose\tuntracked",
		":36",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("checkout completions =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}

	out = runComplete(t, "--cwd", dir, "checkout", "l")
	if lines := nonEmpty(out); len(lines) != 2 || !strings.HasPrefix(lines[0], "loose") {
		t.Errorf("prefix filter: %v", lines)
	}

	out = runComplete(t, "--cwd", dir, "up", "")
	if lines := nonEmpty(out); strings.Join(lines, "|") != "1\tc|:36" {
		t.Errorf("up steps from b: %v", lines)
	}
	out = runComplete(t, "--cwd", dir, "down", "")
	if lines := nonEmpty(out); strings.Join(lines, "|") != "1\ta|2\tmain|:36" {
		t.Errorf("down steps from b: %v", lines)
	}
	out = runComplete(t, "--cwd", dir, "down", "--steps", "")
	if lines := nonEmpty(out); strings.Join(lines, "|") != "1\ta|2\tmain|:36" {
		t.Errorf("--steps flag: %v", lines)
	}
	out = runComplete(t, "--cwd", dir, "up", "--to", "")
	if lines := nonEmpty(out); strings.Join(lines, "|") != "c\tstack: a|:36" {
		t.Errorf("--to from b: %v", lines)
	}
	out = runComplete(t, "--cwd", dir, "up", "1", "")
	if lines := nonEmpty(out); strings.Join(lines, "|") != ":4" {
		t.Errorf("second positional: %v", lines)
	}

	// Outside a repository completion degrades to nothing, never to files.
	out = runComplete(t, "--cwd", t.TempDir(), "checkout", "")
	if lines := nonEmpty(out); strings.Join(lines, "|") != ":4" {
		t.Errorf("outside repo: %v", lines)
	}
}
