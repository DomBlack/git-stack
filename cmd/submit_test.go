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
	if !strings.Contains(out, "Dry run") || !strings.Contains(out, "feat-a  (new PR, draft)") || !strings.Contains(out, "feat-b  (new PR, draft)") {
		t.Errorf("dry run output = %q", out)
	}
	if !strings.Contains(errOut, "note: gh stack submits the whole stack") {
		t.Errorf("stack notice missing: %q", errOut)
	}
	for _, c := range f.CallsTo("gh") {
		if len(c.Args) > 1 && c.Args[0] == "stack" && c.Args[1] == "submit" {
			t.Error("dry run ran gh stack submit")
		}
	}

	// Real submit, non-interactive: --auto, drafts, PRs reported from the forge.
	f.On("gh", "stack", "submit").Do(func(c exec.Cmd) (exec.Result, error) {
		f.On("gh", "pr", "list").Reply(`[{"number":1,"url":"u/1","state":"OPEN","isDraft":true,"headRefName":"feat-a","baseRefName":"main"},
			{"number":2,"url":"u/2","state":"OPEN","isDraft":true,"headRefName":"feat-b","baseRefName":"feat-a"}]`)
		return exec.Result{Stderr: []byte("✓ Created 2 PRs")}, nil
	})
	f.Reset()
	out, errOut, err = runWith(t, f, "--cwd", dir, "ss", "--stack", "--no-edit")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "feat-a  #1 created u/1") || !strings.Contains(out, "feat-b  #2 created u/2") {
		t.Errorf("submit output = %q", out)
	}
	if strings.Contains(errOut, "note: gh stack submits") || !strings.Contains(errOut, "✓ Created 2 PRs") {
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
	if submitArgs != "stack submit --auto" {
		t.Errorf("submit args = %q", submitArgs)
	}
	if _, _, err := runWith(t, f, "--cwd", dir, "submit", "-u"); err == nil || !strings.Contains(err.Error(), "update-only") {
		t.Errorf("--update-only: %v", err)
	}

	f.On("gh", "stack", "sync").Do(func(exec.Cmd) (exec.Result, error) {
		return exec.Result{Stderr: []byte("Stack synced")}, nil
	})
	f.Reset()
	out, errOut, err = runWith(t, f, "--cwd", dir, "sync", "-f", "--all")
	if err != nil || out != "Synced.\n" || !strings.Contains(errOut, "Stack synced") || !strings.Contains(errOut, "--all was ignored") {
		t.Errorf("sync: %q %q %v", out, errOut, err)
	}
	var syncArgs string
	for _, c := range f.CallsTo("gh") {
		if len(c.Args) > 1 && c.Args[1] == "sync" {
			syncArgs = strings.Join(c.Args, " ")
		}
	}
	if syncArgs != "stack sync --prune" {
		t.Errorf("sync args = %q", syncArgs)
	}
	if _, _, err := runWith(t, f, "--cwd", dir, "sync", "--no-restack"); err == nil {
		t.Error("--no-restack should be rejected")
	}
}
