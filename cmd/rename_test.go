package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestRenameCommand(t *testing.T) {
	dir, run := stackOf3(t)
	out, errOut, err := run("rename", "c2")
	if err != nil || !strings.Contains(out, "ok: Renamed c to c2") {
		t.Errorf("rename: %q %q %v", out, errOut, err)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "c2" {
		t.Errorf("current = %s", cur)
	}
	if _, _, err = run("rename"); err == nil {
		t.Error("rename needs a name")
	}
	if _, _, err = run("rename", "a"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("taken: %v", err)
	}
}

func TestRenameCommandOnGitHub(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)
	gittest.WriteFile(t, dir, "a.txt", "a")
	if _, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "-m", "feat: a", "a"); err != nil {
		t.Fatal(err)
	}
	gittest.InitRemote(t, dir)
	f.On("gh", "api", "--method", "POST").Reply(`{"name":"a2"}`)
	out, errOut, err := runWith(t, f, "--cwd", dir, "rename", "a2")
	if err != nil || !strings.Contains(out, "ok: Renamed a to a2 here and on origin") {
		t.Fatalf("rename: %q %q %v", out, errOut, err)
	}
	var called bool
	for _, c := range f.CallsTo("gh") {
		called = called || strings.Join(c.Args, " ") == "api --method POST repos/{owner}/{repo}/branches/a/rename -f new_name=a2"
	}
	if !called {
		t.Error("GitHub's rename was not called")
	}

	out, errOut, err = runWith(t, f, "--cwd", dir, "rename", "--local", "a3")
	if err != nil || !strings.Contains(out, "ok: Renamed a2 to a3") || strings.Contains(out, "on origin") {
		t.Errorf("rename --local: %q %q %v", out, errOut, err)
	}
}
