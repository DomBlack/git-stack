// Package gittest creates isolated temporary repositories for tests.
package gittest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Isolate points git (and anything else) at a throwaway HOME and global
// config so tests never read or write the developer's real configuration.
func Isolate(t testing.TB) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_AUTHOR_DATE", "2026-01-02T03:04:05Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-02T03:04:05Z")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_STACK_NO_INTERACTIVE", "1")
	// Never let a real editor or signing key interfere.
	t.Setenv("GIT_EDITOR", "true")
	Run(t, home, "config", "--global", "commit.gpgsign", "false")
	Run(t, home, "config", "--global", "init.defaultBranch", "main")
}

// Run executes git in dir and fails the test on error. It returns trimmed
// stdout.
func Run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	res, err := exec.New().Run(context.Background(), exec.Cmd{Name: "git", Args: args, Dir: dir})
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, res.Err())
	}
	return res.Out()
}

// RunErr runs git like Run but hands the failure back instead of failing
// the test, for commands that are expected to exit non-zero (a rebase
// that stops on a conflict, say).
func RunErr(t testing.TB, dir string, args ...string) (string, error) {
	t.Helper()
	res, err := exec.New().Run(context.Background(), exec.Cmd{Name: "git", Args: args, Dir: dir})
	return res.Out(), err
}

// InitRepo creates a repository with one commit on main and returns its path.
// Call Isolate first.
func InitRepo(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	Run(t, dir, "init", "-q", "-b", "main")
	WriteFile(t, dir, "README.md", "hello\n")
	Run(t, dir, "add", "README.md")
	Run(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

// WriteFile writes a file under dir, creating parents.
func WriteFile(t testing.TB, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Commit writes name with content and commits it on the current branch.
func Commit(t testing.TB, dir, name, content, message string) string {
	t.Helper()
	WriteFile(t, dir, name, content)
	Run(t, dir, "add", name)
	Run(t, dir, "commit", "-q", "-m", message)
	return Run(t, dir, "rev-parse", "HEAD")
}

// InitRemote creates a bare clone of dir as its origin, pushes every branch
// and sets their upstreams. It returns the bare repository's path. Tests
// move the remote by committing on a detached HEAD and pushing to it.
func InitRemote(t testing.TB, dir string) string {
	t.Helper()
	bare := t.TempDir()
	Run(t, bare, "init", "-q", "--bare", "-b", "main")
	Run(t, dir, "remote", "add", "origin", bare)
	Run(t, dir, "push", "-q", "-u", "origin", "--all")
	return bare
}
