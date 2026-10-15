package ghstack

import (
	"context"
	"errors"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
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

func TestLink(t *testing.T) {
	f := installed()
	f.On("gh", "stack").Do(func(c exec.Cmd) (exec.Result, error) {
		return exec.Result{Stderr: []byte("✓ linked\n")}, nil
	})
	b := New(f, nil)
	ctx := context.Background()
	repo := git.Repo{TopLevel: "/r"}

	res, err := b.Link(ctx, repo, stack.LinkOptions{Trunk: "main", PRs: []int{7, 8}})
	if err != nil || res.Output != "✓ linked" || lastGh(f) != "stack link --base main 7 8" {
		t.Errorf("new stack: %+v %v %q", res, err, lastGh(f))
	}
	if _, err := b.Link(ctx, repo, stack.LinkOptions{Stack: 3, Trunk: "main", PRs: []int{9}}); err != nil || lastGh(f) != "stack link 3 9" {
		t.Errorf("existing stack: %v %q", err, lastGh(f))
	}
	calls := f.CallsTo("gh")
	if calls[len(calls)-1].Mode != exec.Capture {
		t.Error("link must be captured")
	}
}

func TestUnstack(t *testing.T) {
	f := installed()
	f.On("gh", "stack").Reply("")
	b := New(f, nil)
	ctx := context.Background()
	repo := git.Repo{TopLevel: "/r"}

	if err := b.Unstack(ctx, repo, 7); err != nil || lastGh(f) != "stack unstack 7" {
		t.Errorf("unstack: %v %q", err, lastGh(f))
	}

	// Exit 2 means GitHub no longer has the stack: nothing left to do.
	gone := installed()
	gone.On("gh", "stack", "unstack").Fail(2, "stack not found")
	if err := New(gone, nil).Unstack(ctx, repo, 7); err != nil {
		t.Errorf("a stack that is already gone is not an error: %v", err)
	}

	// Exit 2 for any other reason (not a repository, not in a stack) is a
	// real failure.
	other := installed()
	other.On("gh", "stack", "unstack").Fail(2, "not a git repository")
	if err := New(other, nil).Unstack(ctx, repo, 7); !errors.Is(err, &stack.Error{Kind: stack.KindNotInStack}) {
		t.Errorf("exit 2 without not found must be returned: %v", err)
	}
	named := installed()
	named.On("gh", "stack", "unstack").Fail(2, "stack #7 not found on GitHub")
	if err := New(named, nil).Unstack(ctx, repo, 7); err != nil {
		t.Errorf("a stack GitHub does not have is not an error: %v", err)
	}

	// A missing gh stack extension is reported before anything runs.
	missing := exectest.New()
	missing.On("gh", "extension", "list").Reply("")
	if err := New(missing, nil).Unstack(ctx, repo, 7); !errors.Is(err, &stack.Error{Kind: stack.KindNotInstalled}) {
		t.Errorf("not installed: %v", err)
	}

	failing := installed()
	failing.On("gh", "stack", "unstack").Fail(4, "HTTP 500")
	if err := New(failing, nil).Unstack(ctx, repo, 7); !errors.Is(err, &stack.Error{Kind: stack.KindAPIFailure}) {
		t.Errorf("api failure: %v", err)
	}
}
