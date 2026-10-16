package cmd

import (
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestUntrackCommand(t *testing.T) {
	dir, run := stackOf3(t)
	out, errOut, err := run("untrack", "a", "--only")
	if err != nil || !strings.Contains(out, "ok: Untracked a") || !strings.Contains(out, "Restacked b, c onto main") || !strings.Contains(out, "git stack init a tracks it again") {
		t.Errorf("untrack --only: %q %q %v", out, errOut, err)
	}
	if _, err := gittest.RunErr(t, dir, "cat-file", "-e", "b:a.txt"); err == nil {
		t.Error("b still has a's file")
	}
	out, errOut, err = run("untrack", "b")
	if err != nil || !strings.Contains(out, "ok: Untracked b, c") || !strings.Contains(out, "git stack init b c tracks them again") {
		t.Errorf("untrack: %q %q %v", out, errOut, err)
	}
	if _, _, err = run("untrack"); err == nil || !strings.Contains(err.Error(), "c is not in a stack") {
		t.Errorf("untracked: %v", err)
	}
	if _, _, err = run("untrack", "main"); err == nil || !strings.Contains(err.Error(), "trunk") {
		t.Errorf("trunk: %v", err)
	}
}
