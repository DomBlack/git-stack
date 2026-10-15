package github

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestCreateStack(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "--method", "POST").Reply(`{"id":123456789012,"number":12,"pull_requests":[{"number":4},{"number":5}]}`)
	ref, err := New(f).CreateStack(context.Background(), git.Repo{TopLevel: "/r"}, []int{4, 5, 6})
	if err != nil || ref != (forge.StackRef{ID: "123456789012", Number: 12}) {
		t.Fatalf("CreateStack = %+v, %v", ref, err)
	}
	call := f.CallsTo("gh")[0]
	if got := strings.Join(call.Args, " "); got != "api --method POST repos/{owner}/{repo}/stacks --input -" {
		t.Errorf("args = %q", got)
	}
	if call.Stdin != `{"pull_requests":[4,5,6]}` {
		t.Errorf("body = %q", call.Stdin)
	}
}

func TestCreateStackRefused(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "--method", "POST").Fail(1, "gh: Pull request #5 is already in a stack (HTTP 422)\n{\"message\":\"Pull request #5 is already in a stack\"}")
	_, err := New(f).CreateStack(context.Background(), git.Repo{TopLevel: "/r"}, []int{4, 5})
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindAPIFailure || se.Msg != "Pull request #5 is already in a stack" {
		t.Errorf("refused create: %v", err)
	}
}

// A validation failure only says so on stderr; the reason is in the body.
func TestCreateStackRefusedWithReasonsInTheBody(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "--method", "POST").Do(func(c exec.Cmd) (exec.Result, error) {
		res := exec.Result{
			Stdout:   []byte(`{"message":"Validation Failed","errors":[{"resource":"Stack","code":"custom","message":"Pull request #5 is not based on #4"}],"status":"422"}`),
			Stderr:   []byte("gh: Validation Failed (HTTP 422)"),
			ExitCode: 1,
		}
		return res, &exec.ExitError{Cmd: c, Result: res}
	})
	_, err := New(f).CreateStack(context.Background(), git.Repo{TopLevel: "/r"}, []int{4, 5})
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindAPIFailure || se.Msg != "Validation Failed: Pull request #5 is not based on #4" {
		t.Errorf("refused create: %v", err)
	}
}
