package github

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestRenameBranch(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "--method", "POST").Reply(`{"name":"billing/retry-v2"}`)
	if err := New(f).RenameBranch(context.Background(), git.Repo{TopLevel: "/r"}, "billing/retry", "billing/retry-v2"); err != nil {
		t.Fatal(err)
	}
	call := f.CallsTo("gh")[0]
	if got := strings.Join(call.Args, " "); got != "api --method POST repos/{owner}/{repo}/branches/billing%2Fretry/rename -f new_name=billing/retry-v2" {
		t.Errorf("args = %q", got)
	}
}

func TestRenameBranchRefused(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "--method", "POST").Fail(1, "gh: Branch already exists (HTTP 422)")
	err := New(f).RenameBranch(context.Background(), git.Repo{TopLevel: "/r"}, "a", "b")
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindAPIFailure || se.Msg != "Branch already exists" {
		t.Errorf("refused rename: %v", err)
	}
}
