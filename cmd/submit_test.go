package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestSubmitAndSyncCommands(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)
	gittest.WriteFile(t, dir, "a.txt", "a")
	if _, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "-m", "feat a"); err != nil {
		t.Fatal(err)
	}
	gittest.WriteFile(t, dir, "b.txt", "b")
	if _, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "-m", "feat b"); err != nil {
		t.Fatal(err)
	}

	// Dry run: no gh stack submit call.
	f.Reset()
	out, errOut, err := runWith(t, f, "--cwd", dir, "submit", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "Dry run") || !strings.Contains(out, "feat-a  (new PR, ready for review)") || !strings.Contains(out, "feat-b  (new PR, ready for review)") {
		t.Errorf("dry run output = %q", out)
	}
	for _, c := range f.CallsTo("gh") {
		if len(c.Args) > 1 && c.Args[0] == "stack" && c.Args[1] == "submit" {
			t.Error("dry run ran gh stack submit")
		}
	}

	// Real submit, non-interactive: --auto, ready for review, PRs reported from the forge.
	f.On("gh", "stack", "submit").Do(func(c exec.Cmd) (exec.Result, error) {
		f.On("gh", "pr", "list").Reply(`[{"number":1,"url":"u/1","state":"OPEN","isDraft":true,"headRefName":"feat-a","baseRefName":"main"},
			{"number":2,"url":"u/2","state":"OPEN","isDraft":true,"headRefName":"feat-b","baseRefName":"feat-a"}]`)
		return exec.Result{Stderr: []byte("✓ Created 2 PRs")}, nil
	})
	f.Reset()
	out, errOut, err = runWith(t, f, "--cwd", dir, "ss", "--no-edit")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "feat-a  #1 created u/1") || !strings.Contains(out, "feat-b  #2 created u/2") {
		t.Errorf("submit output = %q", out)
	}
	if !strings.Contains(errOut, "✓ Created 2 PRs") {
		t.Errorf("stderr = %q", errOut)
	}
	var submitArgs string
	for _, c := range f.CallsTo("gh") {
		if len(c.Args) > 1 && c.Args[1] == "submit" {
			submitArgs = strings.Join(c.Args, " ")
			if c.Mode != exec.Capture {
				t.Error("non-interactive submit must be captured")
			}
		}
	}
	// --open is gh stack's "ready for review", the default now.
	if submitArgs != "stack submit --open --auto" {
		t.Errorf("submit args = %q", submitArgs)
	}

	gittest.InitRemote(t, dir)
	f.Reset()
	out, errOut, err = runWith(t, f, "--cwd", dir, "sync")
	if err != nil || out != "ok: Synced, nothing to do\n" {
		t.Errorf("sync: %q %q %v", out, errOut, err)
	}
	for _, c := range f.CallsTo("gh") {
		if len(c.Args) > 1 && c.Args[1] == "sync" {
			t.Errorf("sync must not call gh stack sync: %v", c.Args)
		}
	}
}
