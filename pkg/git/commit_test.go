package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

// TestCommitWithLockedSSHKeyFailsFast signs with a passphrase protected key
// that is not in any agent, the setup behind the MCP hang: ssh-keygen must
// fail at once rather than wait for a passphrase, and the failure must come
// back as a SigningError naming the key.
func TestCommitWithLockedSSHKeyFailsFast(t *testing.T) {
	gittest.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	dir := gittest.InitRepo(t)
	key := filepath.Join(t.TempDir(), "id_ed25519")
	res, err := exec.New().Run(context.Background(), exec.Cmd{
		Name: "ssh-keygen", Args: []string{"-q", "-t", "ed25519", "-N", "secret", "-C", "test", "-f", key},
	})
	if _, missing := errors.AsType[*exec.NotFoundError](err); missing {
		t.Skip("ssh-keygen not installed")
	}
	if err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, res.Err())
	}
	gittest.Run(t, dir, "config", "gpg.format", "ssh")
	gittest.Run(t, dir, "config", "user.signingkey", key+".pub")
	gittest.Run(t, dir, "config", "commit.gpgsign", "true")
	gittest.WriteFile(t, dir, "a.txt", "a\n")
	gittest.Run(t, dir, "add", "a.txt")

	c := newClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	repo, _ := c.Discover(ctx, dir)
	start := time.Now()
	_, err = c.Commit(ctx, repo, git.CommitOptions{Message: []string{"signed"}})
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("git commit waited on the passphrase prompt instead of failing")
	}
	se, ok := errors.AsType[*git.SigningError](err)
	if !ok {
		t.Fatalf("err = %v (%T), want *git.SigningError", err, err)
	}
	if se.Format != "ssh" || se.Key != key+".pub" {
		t.Errorf("SigningError = %+v, want format ssh and key %s.pub", se, key)
	}
	if !strings.Contains(se.Detail, "failed to write commit object") {
		t.Errorf("Detail = %q, want git's stderr", se.Detail)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("commit took %s", time.Since(start))
	}
	if staged, _ := c.HasStagedChanges(ctx, repo); !staged {
		t.Error("the staged changes should still be staged")
	}
}

// TestCommitObjectWriteFailureIsNotSigning makes git print "failed to write
// commit object" for a reason that has nothing to do with signing (a read
// only object store); that must stay an ordinary error.
func TestCommitObjectWriteFailureIsNotSigning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read only directory")
	}
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.WriteFile(t, dir, "a.txt", "a\n")
	gittest.Run(t, dir, "add", "a.txt")
	objects := filepath.Join(dir, ".git", "objects")
	if err := os.Chmod(objects, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(objects, 0o755) })

	c := newClient()
	ctx := context.Background()
	repo, _ := c.Discover(ctx, dir)
	_, err := c.Commit(ctx, repo, git.CommitOptions{Message: []string{"x"}})
	if err == nil {
		t.Fatal("the commit should fail with a read only object store")
	}
	if _, ok := errors.AsType[*git.SigningError](err); ok {
		t.Fatalf("err = %v, classified as a signing failure", err)
	}
	if !strings.Contains(err.Error(), "git commit:") {
		t.Errorf("err = %v, want git's message", err)
	}
}

// TestCommitBareGpgsignKeyIsSigning: git reads `[commit] gpgsign` with no
// value as true, so a signing failure under it must still be recognised.
func TestCommitBareGpgsignKeyIsSigning(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[commit]\n\tgpgsign\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	gittest.Run(t, dir, "config", "gpg.format", "ssh")
	gittest.Run(t, dir, "config", "gpg.ssh.program", "false")
	gittest.Run(t, dir, "config", "user.signingkey", "keys/id_ed25519.pub")
	gittest.WriteFile(t, dir, "keys/id_ed25519.pub", "ssh-ed25519 AAAAC3Nza test\n")
	gittest.WriteFile(t, dir, "a.txt", "a\n")
	gittest.Run(t, dir, "add", "a.txt")

	c := newClient()
	ctx := context.Background()
	repo, _ := c.Discover(ctx, dir)
	_, err = c.Commit(ctx, repo, git.CommitOptions{Message: []string{"x"}})
	se, ok := errors.AsType[*git.SigningError](err)
	if !ok {
		t.Fatalf("err = %v (%T), want *git.SigningError", err, err)
	}
	// A relative key file is resolved against the repository.
	if want := filepath.Join(repo.TopLevel, "keys", "id_ed25519.pub"); se.Key != want {
		t.Errorf("Key = %q, want %q", se.Key, want)
	}
}
