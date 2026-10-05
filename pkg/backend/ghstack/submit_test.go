package ghstack

import (
	"context"
	"errors"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestSubmit(t *testing.T) {
	f := installed()
	f.On("gh", "stack").Do(func(c exec.Cmd) (exec.Result, error) {
		return exec.Result{Stderr: []byte("✓ pushed\n")}, nil
	})
	b := New(f, nil)
	ctx := context.Background()
	repo := git.Repo{TopLevel: "/r"}

	res, err := b.Submit(ctx, repo, stack.SubmitOptions{})
	if err != nil || res.Output != "✓ pushed" || lastGh(f) != "stack submit --auto" {
		t.Errorf("submit: %+v %v %q", res, err, lastGh(f))
	}
	if _, err := b.Submit(ctx, repo, stack.SubmitOptions{Publish: true}); err != nil || lastGh(f) != "stack submit --open --auto" {
		t.Errorf("publish: %v %q", err, lastGh(f))
	}
	calls := f.CallsTo("gh")
	if calls[len(calls)-1].Mode != exec.Capture {
		t.Error("non-interactive submit must be captured")
	}

	// Interactive submit hands over the terminal; the fake honours Passthrough.
	if _, err := b.Submit(ctx, repo, stack.SubmitOptions{Interactive: true}); err != nil {
		t.Errorf("interactive: %v", err)
	}
	calls = f.CallsTo("gh")
	last := calls[len(calls)-1]
	if last.Mode != exec.Passthrough || lastGh(f) != "stack submit" {
		t.Errorf("interactive call = %+v", last)
	}

	// Without a TTY the runner refuses passthrough and we explain.
	noTTY := installed()
	noTTY.On("gh", "stack").Do(func(c exec.Cmd) (exec.Result, error) {
		if c.Mode == exec.Passthrough {
			return exec.Result{}, exec.ErrTTYUnavailable
		}
		return exec.Result{}, nil
	})
	_, err = New(noTTY, nil).Submit(ctx, repo, stack.SubmitOptions{Interactive: true})
	if !errors.Is(err, &stack.Error{Kind: stack.KindInteractionRequired}) {
		t.Errorf("no tty: %v", err)
	}

	f.On("gh", "stack", "submit").Fail(9, "Stacked PRs are not enabled for this repository")
	if _, err := b.Submit(ctx, repo, stack.SubmitOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindStacksUnavailable}) {
		t.Errorf("exit 9: %v", err)
	}
}
