package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestUndoCommand(t *testing.T) {
	dir, run := stackOf3(t)
	out, _, err := run("undo", "--list")
	if err != nil || !strings.Contains(out, "3 commands can be undone") || !strings.Contains(out, "git stack create") {
		t.Errorf("list: %q %v", out, err)
	}
	out, errOut, err := run("undo")
	if err != nil || !strings.Contains(out, "ok: Undid git stack create (just now)") || !strings.Contains(out, "c deleted") ||
		!strings.Contains(out, "your changes from it are staged") {
		t.Errorf("undo: %q %q %v", out, errOut, err)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "b" {
		t.Errorf("current = %s", cur)
	}
	if st := gittest.Run(t, dir, "status", "--short"); st != "A  c.txt" {
		t.Errorf("status = %q", st)
	}
	gittest.Run(t, dir, "commit", "-q", "-m", "mine")
	if _, _, err = run("undo"); err == nil || !strings.Contains(err.Error(), "b moved since git stack create") {
		t.Errorf("drift: %v", err)
	}
	if out, _, err = run("undo", "--force"); err != nil || !strings.Contains(out, "Undid git stack create") {
		t.Errorf("force: %q %v", out, err)
	}
	if _, _, err := run("undo"); err != nil {
		t.Fatal(err)
	}
	out, _, err = run("undo", "--list")
	if err != nil || !strings.Contains(out, "Nothing to undo") {
		t.Errorf("empty list: %q %v", out, err)
	}
	if _, _, err = run("undo"); err == nil || !strings.Contains(err.Error(), "nothing to undo yet") {
		t.Errorf("empty: %v", err)
	}
}
