package cmd

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/ui"
)

// prFake runs git for real and stands in for gh and for both browser
// openers, so no test can ever open a real browser.
func prFake(openErr bool) *exectest.Fake {
	f := exectest.New()
	real := exec.New()
	f.Fallback = func(c exec.Cmd) (exec.Result, error) { return real.Run(context.Background(), c) }
	f.On("gh", "extension", "list").Reply("gh stack\tgithub/gh-stack\tv0.9.0\n")
	f.On("gh", "pr", "list").Reply("[]")
	for _, name := range []string{"open", "xdg-open"} {
		f.On(name).Do(func(c exec.Cmd) (exec.Result, error) {
			if openErr {
				res := exec.Result{ExitCode: 3, Stderr: []byte("no method available for opening")}
				return res, &exec.ExitError{Cmd: c, Result: res}
			}
			return exec.Result{}, nil
		})
	}
	return f
}

// opened lists the URLs handed to a browser opener.
func opened(f *exectest.Fake) []string {
	var out []string
	for _, name := range []string{"open", "xdg-open"} {
		for _, c := range f.CallsTo(name) {
			out = append(out, strings.Join(c.Args, " "))
		}
	}
	return out
}

// runInteractive runs the CLI as if stdin and stdout were a terminal.
func runInteractive(t *testing.T, f *exectest.Fake, dir string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	streams := Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}
	c := &cli{streams: streams, newRunner: func(...exec.Option) exec.Runner { return f }}
	c.rt = &Runtime{
		Globals: Globals{Cwd: dir}, Streams: streams, Interactive: true,
		Runner: f, Git: git.New(f), Log: slog.New(slog.DiscardHandler),
		Report: ui.NewReporter(streams.In, &out, &errOut, ui.ReporterOptions{}),
	}
	c.rtOnce.Do(func() {})
	root := newRootCmd(c)
	root.SetArgs(append([]string{"--cwd", dir}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestPRCommandPrintsWithoutATerminal(t *testing.T) {
	dir := stackRepo(t) // main -> a (#41) -> b -> c, on b
	f := prFake(false)
	for _, args := range [][]string{{"pr", "a"}, {"pr", "--print", "a"}} {
		out, errOut, err := runWith(t, f, append([]string{"--cwd", dir}, args...)...)
		if err != nil || out != "https://example.com/41\n" {
			t.Errorf("%v: %q %q %v", args, out, errOut, err)
		}
	}
	if got := opened(f); len(got) != 0 {
		t.Errorf("nothing should open without a terminal: %v", got)
	}
}

func TestPRCommandOpensTheBrowser(t *testing.T) {
	dir := stackRepo(t)
	f := prFake(false)
	out, errOut, err := runInteractive(t, f, dir, "pr", "a")
	if err != nil || out != "ok: Opened #41 (a) in your browser\n" {
		t.Errorf("pr a: %q %q %v", out, errOut, err)
	}
	if got := opened(f); len(got) != 1 || got[0] != "https://example.com/41" {
		t.Errorf("opened %v", got)
	}

	out, _, err = runInteractive(t, f, dir, "pr", "--print", "a")
	if err != nil || out != "https://example.com/41\n" || len(opened(f)) != 1 {
		t.Errorf("--print should only print: %q %v %v", out, err, opened(f))
	}
}

func TestPRCommandPrintsWhenTheBrowserWontOpen(t *testing.T) {
	dir := stackRepo(t)
	f := prFake(true)
	out, errOut, err := runInteractive(t, f, dir, "pr", "a")
	if err != nil || out != "https://example.com/41\n" || !strings.Contains(errOut, "note: Couldn't open your browser") {
		t.Errorf("pr a: %q %q %v", out, errOut, err)
	}
}

func TestPRCommandErrors(t *testing.T) {
	dir := stackRepo(t)
	f := prFake(false)
	// b has no PR anywhere, the forge included.
	_, errOut, err := runWith(t, f, "--cwd", dir, "pr")
	if err == nil || err.Error() != "b has no pull request yet" {
		t.Errorf("pr on b: %v %q", err, errOut)
	}
	if _, _, err := runWith(t, f, "--cwd", dir, "pr", "main"); err == nil || !strings.Contains(err.Error(), "main is a trunk") {
		t.Errorf("pr main: %v", err)
	}
	if _, _, err := runWith(t, f, "--cwd", dir, "pr", "loose"); err == nil || err.Error() != "loose is not in a stack" {
		t.Errorf("pr loose: %v", err)
	}
	if got := opened(f); len(got) != 0 {
		t.Errorf("opened %v", got)
	}
}
