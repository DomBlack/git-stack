package ghstack

import (
	"bytes"
	"context"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// TestSubmitAndSyncStreamOutput checks that with WithOutput the backend
// relays gh stack's progress live and tells callers not to print it again,
// while without it the output is only returned.
func TestSubmitAndSyncStreamOutput(t *testing.T) {
	newFake := func() *exectest.Fake {
		f := exectest.New()
		f.On("gh", "extension", "list").Reply("gh stack\tgithub/gh-stack\tv0.1.1\n")
		f.On("gh", "stack", "submit").Do(func(c exec.Cmd) (exec.Result, error) {
			if c.Stream == nil {
				t.Error("submit should pass the output writer as Stream")
			}
			return exec.Result{Stderr: []byte("Pushing to origin...\n✓ Created PR #1\n")}, nil
		})
		f.On("gh", "stack", "sync").Do(func(exec.Cmd) (exec.Result, error) {
			return exec.Result{Stderr: []byte("Fetching...\nSynced.\n")}, nil
		})
		return f
	}
	repo := git.Repo{TopLevel: t.TempDir()}

	var live bytes.Buffer
	b := New(newFake(), nil, WithOutput(&live))
	res, err := b.Submit(context.Background(), repo, stack.SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Streamed || res.Output != "Pushing to origin...\n✓ Created PR #1" {
		t.Errorf("submit result = %+v", res)
	}
	if live.String() != "Pushing to origin...\n✓ Created PR #1\n" {
		t.Errorf("live output = %q", live.String())
	}
	live.Reset()
	sres, err := b.Sync(context.Background(), repo, stack.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !sres.Streamed || !bytes.Contains(live.Bytes(), []byte("Synced.")) {
		t.Errorf("sync result = %+v, live = %q", sres, live.String())
	}

	// No writer (the MCP server): nothing streamed, output returned.
	f := exectest.New()
	f.On("gh", "extension", "list").Reply("gh stack\tgithub/gh-stack\tv0.1.1\n")
	f.On("gh", "stack", "submit").Do(func(c exec.Cmd) (exec.Result, error) {
		if c.Stream != nil {
			t.Error("no WithOutput means no Stream")
		}
		return exec.Result{Stderr: []byte("✓ Created PR #1\n")}, nil
	})
	res, err = New(f, nil).Submit(context.Background(), repo, stack.SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Streamed || res.Output != "✓ Created PR #1" {
		t.Errorf("unstreamed submit result = %+v", res)
	}
}
