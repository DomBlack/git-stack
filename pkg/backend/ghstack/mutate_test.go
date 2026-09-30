package ghstack

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func installed() *exectest.Fake {
	f := exectest.New()
	f.On("gh", "extension", "list").Reply("gh stack\tgithub/gh-stack\tv0.9.0\n")
	return f
}

func lastGh(f *exectest.Fake) string {
	calls := f.CallsTo("gh")
	return strings.Join(calls[len(calls)-1].Args, " ")
}

func TestTracker(t *testing.T) {
	f := installed()
	f.On("gh", "stack").Reply("")
	b := New(f, nil)
	ctx := context.Background()
	repo := git.Repo{TopLevel: "/r"}

	if err := b.InitStack(ctx, repo, "main", []string{"feat/a"}); err != nil {
		t.Fatal(err)
	}
	if got := lastGh(f); got != "stack init --base main feat/a" {
		t.Errorf("init args = %q", got)
	}
	if err := b.AddTop(ctx, repo, "feat/b"); err != nil {
		t.Fatal(err)
	}
	if got := lastGh(f); got != "stack add feat/b" {
		t.Errorf("add args = %q (must not pass -m)", got)
	}

	f.On("gh", "stack", "add").Fail(5, "✗ can only add branches to the top of the stack; run `gh stack top` then `gh stack add`")
	err := b.AddTop(ctx, repo, "feat/c")
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindNotAtTop || len(se.NextSteps) == 0 {
		t.Errorf("mid-stack add: %v", err)
	}
	f.On("gh", "stack", "add").Fail(5, "branch already exists in a stack")
	if err := b.AddTop(ctx, repo, "feat/c"); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("other invalid args: %v", err)
	}
}

func TestRestacker(t *testing.T) {
	f := installed()
	f.On("gh", "stack", "rebase").Reply("")
	b := New(f, nil)
	ctx := context.Background()
	repo := git.Repo{TopLevel: "/r"}

	cases := map[stack.Scope]string{
		stack.ScopeAll:       "stack rebase --no-trunk",
		stack.ScopeUpstack:   "stack rebase --no-trunk --upstack",
		stack.ScopeDownstack: "stack rebase --no-trunk --downstack",
	}
	for scope, want := range cases {
		if err := b.Restack(ctx, repo, scope); err != nil {
			t.Fatalf("%v: %v", scope, err)
		}
		if got := lastGh(f); got != want {
			t.Errorf("%v: args = %q, want %q", scope, got, want)
		}
	}
	if err := b.Restack(ctx, repo, stack.ScopeOnly); !errors.Is(err, &stack.Error{Kind: stack.KindUnsupported}) {
		t.Errorf("only: %v", err)
	}
	if err := b.Continue(ctx, repo); err != nil || lastGh(f) != "stack rebase --continue" {
		t.Errorf("continue: %v %q", err, lastGh(f))
	}
	if err := b.Abort(ctx, repo); err != nil || lastGh(f) != "stack rebase --abort" {
		t.Errorf("abort: %v %q", err, lastGh(f))
	}

	f.On("gh", "stack", "rebase").Fail(3, "CONFLICT (content): Merge conflict in pkg/x.go\nResolve conflicts on feat/b, then run `gh stack rebase --continue`")
	err := b.Restack(ctx, repo, stack.ScopeUpstack)
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindConflict || !slices.Equal(se.Files, []string{"pkg/x.go"}) {
		t.Errorf("conflict: %v", err)
	}

	missing := exectest.New()
	missing.On("gh", "extension", "list").Reply("")
	if err := New(missing, nil).Restack(ctx, repo, stack.ScopeAll); !errors.Is(err, &stack.Error{Kind: stack.KindNotInstalled}) {
		t.Errorf("preflight should run first: %v", err)
	}
	if n := len(missing.CallsTo("gh")); n != 1 {
		t.Errorf("no gh stack call expected without the extension, got %d calls", n)
	}
}
