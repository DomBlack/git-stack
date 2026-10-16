package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestFoldCommand(t *testing.T) {
	dir, run := stackOf3(t)
	out, errOut, err := run("fold")
	if err != nil || !strings.Contains(out, "ok: Folded c into b") {
		t.Errorf("fold: %q %q %v", out, errOut, err)
	}
	if cur := gittest.Run(t, dir, "branch", "--show-current"); cur != "b" {
		t.Errorf("current = %s", cur)
	}
	out, errOut, err = run("fold", "--keep")
	if err != nil || !strings.Contains(out, "ok: Folded a into b") {
		t.Errorf("fold --keep: %q %q %v", out, errOut, err)
	}
	if _, _, err = run("fold"); err == nil || !strings.Contains(err.Error(), "bottom of its stack") {
		t.Errorf("bottom: %v", err)
	}
}
