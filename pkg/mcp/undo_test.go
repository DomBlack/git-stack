package mcp

import (
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestUndoTool(t *testing.T) {
	h := newHarness(t) // on b
	defer h.assertNoStdout()
	if te := h.toolErr("stack_undo", nil); te.Code != "invalid_args" {
		t.Errorf("nothing to undo = %+v", te)
	}
	before := gittest.Run(t, h.dir, "rev-parse", "b")
	gittest.WriteFile(t, h.dir, "b.txt", "b2")
	gittest.Run(t, h.dir, "add", "b.txt")
	h.call("stack_modify", map[string]any{}, &app.ModifyResult{})

	var list app.UndoResult
	h.call("stack_undo", map[string]any{"list": true}, &list)
	if len(list.Entries) != 1 || list.Entries[0].Command != "git stack modify" || len(list.Entries[0].Restores) == 0 {
		t.Fatalf("list = %+v", list)
	}

	// A commit on top since: refused, then forced.
	gittest.Commit(t, h.dir, "later.txt", "later", "later")
	te := h.toolErr("stack_undo", nil)
	if te.Code != "invalid_args" || len(te.NextSteps) == 0 || te.NextSteps[0] != "stack_undo with force: true throws away what changed on them since (your uncommitted changes stay)" {
		t.Errorf("drift = %+v", te)
	}
	var res app.UndoResult
	h.call("stack_undo", map[string]any{"force": true}, &res)
	if res.Undone == nil || res.Undone.Command != "git stack modify" || !res.Staged {
		t.Errorf("undo = %+v", res)
	}
	if got := gittest.Run(t, h.dir, "rev-parse", "b"); got != before {
		t.Errorf("b = %s, want %s", got, before)
	}
}
