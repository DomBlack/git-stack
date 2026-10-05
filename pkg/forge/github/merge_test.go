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

func TestMergeStackPollsUntilMerged(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "--method", "PUT").Reply(`{"status":"pending","details":{"uuid":"u-1","merge_method":"rebase"}}`)
	polls := 0
	f.On("gh", "api", "repos/{owner}/{repo}/pulls/5/merge-async/u-1").Do(func(c exec.Cmd) (exec.Result, error) {
		polls++
		if polls < 2 {
			return exec.Result{Stdout: []byte(`{"status":"pending","details":{"uuid":"u-1"}}`)}, nil
		}
		return exec.Result{Stdout: []byte(`{"status":"merged","details":{"sha":"abc123","message":"done"}}`)}, nil
	})
	g := New(f)
	g.poll = 0
	out, err := g.MergeStack(context.Background(), git.Repo{TopLevel: "/r"}, 5, forge.MergeRebase)
	if err != nil || out.Status != forge.MergeMerged || out.SHA != "abc123" {
		t.Fatalf("MergeStack = %+v, %v", out, err)
	}
	put := f.CallsTo("gh")[0].Args
	if strings.Join(put, " ") != "api --method PUT repos/{owner}/{repo}/pulls/5/merge-async -f merge_method=rebase" {
		t.Errorf("PUT args = %q", put)
	}
	if polls != 2 {
		t.Errorf("polled %d times, want 2", polls)
	}
}

func TestMergeStackDefaultMethodAndQueue(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "--method", "PUT").Reply(`{"status":"enqueued","details":{"message":"added to the merge queue"}}`)
	out, err := New(f).MergeStack(context.Background(), git.Repo{TopLevel: "/r"}, 5, forge.MergeDefault)
	if err != nil || out.Status != forge.MergeEnqueued || out.SHA != "" {
		t.Fatalf("MergeStack = %+v, %v", out, err)
	}
	if args := f.CallsTo("gh")[0].Args; strings.Contains(strings.Join(args, " "), "merge_method") {
		t.Errorf("no merge_method should be sent for the default: %q", args)
	}
}

func TestMergeStackFailures(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "--method", "PUT").Reply(`{"status":"failed","details":{"message":"Required status check \"ci\" is failing"}}`)
	_, err := New(f).MergeStack(context.Background(), git.Repo{TopLevel: "/r"}, 5, forge.MergeSquash)
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindAPIFailure || !strings.Contains(se.Msg, "ci") {
		t.Errorf("failed status: %v", err)
	}

	f.On("gh", "api", "--method", "PUT").Fail(1, "gh: Pull request is a draft (HTTP 400)\n{\"message\":\"Pull request is a draft\"}")
	_, err = New(f).MergeStack(context.Background(), git.Repo{TopLevel: "/r"}, 5, forge.MergeSquash)
	if !errors.As(err, &se) || se.Kind != stack.KindAPIFailure || !strings.Contains(se.Msg, "Pull request is a draft") || len(se.NextSteps) == 0 {
		t.Errorf("refused merge: %v", err)
	}
}
