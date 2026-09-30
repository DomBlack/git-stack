package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestRuntimeIsNonInteractiveWithoutTerminals(t *testing.T) {
	c := &cli{streams: testStreams(nil)}
	rt := c.runtime()
	if rt.Interactive {
		t.Fatal("buffers are not terminals; runtime must be non-interactive")
	}
	if _, err := rt.Runner.Run(context.Background(), exec.Cmd{Name: "true", Mode: exec.Passthrough}); !errors.Is(err, exec.ErrTTYUnavailable) {
		t.Errorf("passthrough without TTY = %v", err)
	}
	if c.runtime() != rt {
		t.Error("runtime should be built once")
	}
	if rt.Log == nil {
		t.Error("logger missing")
	}
}

func TestRuntimeRepoAndConfig(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "config", "stack.ai.model", "opus")

	c := &cli{streams: testStreams(nil), globals: Globals{Cwd: dir, Debug: true}}
	rt := c.runtime()
	ctx := context.Background()
	repo, err := rt.Repo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if repo.TopLevel != want {
		t.Errorf("TopLevel = %q, want %q", repo.TopLevel, want)
	}
	cfg, err := rt.Config(ctx)
	if err != nil || cfg.AIModel != "opus" {
		t.Errorf("Config = %+v, %v", cfg, err)
	}
	if !strings.Contains(c.streams.Err.(*bytes.Buffer).String(), "[exec] git") {
		t.Error("--debug should log subprocesses to stderr")
	}

	outside := &cli{streams: testStreams(nil), globals: Globals{Cwd: t.TempDir()}}
	_, err = outside.runtime().Repo(ctx)
	if !errors.Is(err, &stack.Error{Kind: stack.KindNotRepo}) {
		t.Errorf("outside a repo: %v", err)
	}
}

func TestCompletionRuntimeOnlyRunsGit(t *testing.T) {
	c := &cli{streams: testStreams(nil)}
	rt := c.completionRuntime()
	if rt.Interactive {
		t.Error("completion runtime must never be interactive")
	}
	if _, err := rt.Runner.Run(context.Background(), exec.Cmd{Name: "gh", Args: []string{"pr", "list"}}); err == nil {
		t.Error("completion runtime ran a non-git command")
	}
	if _, err := rt.Runner.Run(context.Background(), exec.Cmd{Name: "git", Mode: exec.Passthrough}); err == nil {
		t.Error("completion runtime allowed passthrough")
	}
	if _, err := rt.Git.ListCmds(context.Background(), "builtins"); err != nil {
		t.Errorf("git should be allowed: %v", err)
	}
}

func TestPrintErrorAndExitCodes(t *testing.T) {
	var buf bytes.Buffer
	err := stack.New(stack.KindConflict, "rebase stopped on feat/b").
		WithDetail("CONFLICT (content): a.go").
		WithSteps("resolve the conflicts and git add the files", "run git stack restack --continue")
	printError(&buf, err)
	want := "error: rebase stopped on feat/b\n  CONFLICT (content): a.go\n  - resolve the conflicts and git add the files\n  - run git stack restack --continue\n"
	if buf.String() != want {
		t.Errorf("printError =\n%s\nwant\n%s", buf.String(), want)
	}
	if exitCode(err) != 3 {
		t.Errorf("conflict exit code = %d", exitCode(err))
	}
	if exitCode(stack.New(stack.KindNotInStack, "x")) != 2 || exitCode(errors.New("x")) != 1 || exitCode(context.Canceled) != 130 {
		t.Error("exit codes")
	}

	buf.Reset()
	printError(&buf, errors.New("plain"))
	if buf.String() != "error: plain\n" {
		t.Errorf("plain error = %q", buf.String())
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("buffer reported as terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if isTerminal(f) {
		t.Error("regular file reported as terminal")
	}
	if errFile(&bytes.Buffer{}) != os.Stderr || errFile(f) != f {
		t.Error("errFile")
	}
}
