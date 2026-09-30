package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
