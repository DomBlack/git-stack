package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

// TestEveryCommandAndFlagIsCompletable enforces the autocomplete rule: every
// command has ValidArgsFunction and every non-bool flag has a completion func.
func TestEveryCommandAndFlagIsCompletable(t *testing.T) {
	root := NewRootCmd(testStreams(nil))
	root.InitDefaultHelpCmd()
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.ValidArgsFunction == nil && c.ValidArgs == nil {
			t.Errorf("%s: missing ValidArgsFunction", c.CommandPath())
		}
		check := func(f *pflag.Flag) {
			if f.Value.Type() == "bool" || f.Hidden {
				return
			}
			if _, ok := c.GetFlagCompletionFunc(f.Name); !ok {
				t.Errorf("%s: flag --%s (%s) has no completion func", c.CommandPath(), f.Name, f.Value.Type())
			}
		}
		c.LocalNonPersistentFlags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

func TestCompleteShells(t *testing.T) {
	out := runComplete(t, "completion", "")
	for _, want := range []string{"bash\tBash", "zsh\tZsh", "fish\tFish"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(out), ":4") {
		t.Errorf("expected NoFileComp directive (:4), got:\n%s", out)
	}

	out = runComplete(t, "completion", "f")
	if strings.Contains(out, "bash") || !strings.Contains(out, "fish") {
		t.Errorf("prefix filtering failed:\n%s", out)
	}

	out = runComplete(t, "completion", "bash", "")
	if lines := nonEmpty(out); len(lines) != 1 || lines[0] != ":4" {
		t.Errorf("second positional should offer nothing and no files, got:\n%s", out)
	}
}

func TestCompleteRootListsSubcommandsWithoutFiles(t *testing.T) {
	out := runComplete(t, "")
	if !strings.Contains(out, "completion\t") {
		t.Errorf("subcommands not offered:\n%s", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), ":4") {
		t.Errorf("root completion should not fall back to files:\n%s", out)
	}
}

func TestCompleteCwdOffersDirectories(t *testing.T) {
	out := runComplete(t, "--cwd", "")
	if lines := nonEmpty(out); len(lines) != 1 || lines[0] != ":16" {
		t.Errorf("--cwd should use the FilterDirs directive, got:\n%s", out)
	}
}

func TestVersionAndHelpDoNotError(t *testing.T) {
	var out bytes.Buffer
	root := NewRootCmd(testStreams(&out))
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "git-stack ") {
		t.Errorf("version output = %q", out.String())
	}
}

func runComplete(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	root := NewRootCmd(testStreams(&out))
	root.SetArgs(append([]string{cobra.ShellCompRequestCmd}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("__complete %v: %v", args, err)
	}
	return out.String()
}

func nonEmpty(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func testStreams(out *bytes.Buffer) Streams {
	if out == nil {
		out = &bytes.Buffer{}
	}
	return Streams{In: strings.NewReader(""), Out: out, Err: &bytes.Buffer{}}
}

// twoStackRepo builds main -> a -> b -> c and main -> x -> y with an
// untracked branch, and leaves HEAD on b.
func twoStackRepo(t *testing.T) string {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	for _, b := range []string{"a", "b", "c"} {
		gittest.Run(t, dir, "switch", "-q", "-c", b)
		gittest.Commit(t, dir, b+".txt", b, b)
	}
	gittest.Run(t, dir, "switch", "-q", "-c", "x", "main")
	gittest.Commit(t, dir, "x.txt", "x", "x")
	gittest.Run(t, dir, "switch", "-q", "-c", "y")
	gittest.Commit(t, dir, "y.txt", "y", "y")
	gittest.Run(t, dir, "branch", "loose", "main")
	gittest.Run(t, dir, "switch", "-q", "b")
	meta := `{"schemaVersion":1,"stacks":[
		{"trunk":{"branch":"main"},"branches":[{"branch":"a"},{"branch":"b"},{"branch":"c"}]},
		{"trunk":{"branch":"main"},"branches":[{"branch":"x"},{"branch":"y"}]}]}`
	gitDir := gittest.Run(t, dir, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(gitDir, "gh-stack"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReshapeCompletions(t *testing.T) {
	dir := twoStackRepo(t)
	offered := func(args ...string) string {
		t.Helper()
		return strings.Join(nonEmpty(runComplete(t, append([]string{"--cwd", dir}, args...)...)), "|")
	}

	t.Run("stack order", func(t *testing.T) {
		if got := offered("reorder", ""); got != "a|b|c|:36" {
			t.Errorf("all branches in stack order: %q", got)
		}
		if got := offered("reorder", "c", "a", ""); got != "b|:36" {
			t.Errorf("without the ones already typed: %q", got)
		}
		if got := offered("reorder", "b", "a", "c", ""); got != ":36" {
			t.Errorf("nothing left to offer: %q", got)
		}
	})

	t.Run("move targets", func(t *testing.T) {
		if got := offered("move", "--onto", ""); got != "main\ttrunk|y\ttop of x's stack|:36" {
			t.Errorf("current stack's top left out: %q", got)
		}
		if got := offered("move", "--source", "y", "--onto", ""); got != "main\ttrunk|c\ttop of a's stack|:36" {
			t.Errorf("source stack's top left out: %q", got)
		}
		if got := offered("move", "--onto", "m"); got != "main\ttrunk|:36" {
			t.Errorf("prefix filter: %q", got)
		}
	})

	t.Run("stacked branches", func(t *testing.T) {
		want := "a\tstack: a|b\tstack: a · current|c\tstack: a|x\tstack: x|y\tstack: x|:4"
		if got := offered("move", "--source", ""); got != want {
			t.Errorf("stacked branches only: %q", got)
		}
	})

	t.Run("untracked branches", func(t *testing.T) {
		if got := offered("init", ""); got != "loose|:4" {
			t.Errorf("untracked only: %q", got)
		}
		if got := offered("init", "loose", ""); got != ":4" {
			t.Errorf("already typed: %q", got)
		}
	})

	t.Run("trunks", func(t *testing.T) {
		if got := offered("init", "--base", ""); got != "main|:4" {
			t.Errorf("trunks: %q", got)
		}
	})
}

func TestTrunkCompletionFallsBackToTheDefaultBranch(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	out := runComplete(t, "--cwd", dir, "init", "--base", "")
	if got := strings.Join(nonEmpty(out), "|"); got != "main|:4" {
		t.Errorf("no stack yet: %q", got)
	}
}
