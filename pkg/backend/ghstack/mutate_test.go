package ghstack

import (
	"context"
	"errors"
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
