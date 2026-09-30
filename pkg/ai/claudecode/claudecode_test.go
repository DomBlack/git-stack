package claudecode

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/ai"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

const okResponse = `{"type":"result","subtype":"success","is_error":false,"result":"{\"branch\":\"add-adder\",\"message\":\"Add adder\"}","structured_output":{"branch":"add-adder","message":"Add adder\n\nBecause."}}`

func TestDraftCommitBuildsTheRightInvocation(t *testing.T) {
	f := exectest.New()
	f.On("claude").Reply(okResponse)
	a := New(f, Config{Model: "haiku", ExtraPrompt: "House: no emoji"}, nil)

	c, err := a.DraftCommit(context.Background(), ai.CommitInput{Diff: "+func Add()", BranchPrefix: "dom/"})
	if err != nil {
		t.Fatal(err)
	}
	if c.BranchName != "add-adder" || !strings.HasPrefix(c.Message, "Add adder\n\nBecause.") {
		t.Errorf("draft = %+v", c)
	}
	call := f.Calls()[0]
	args := strings.Join(call.Args, " ")
	for _, want := range []string{"--model haiku", "--tools  ", "--output-format json", "--json-schema", "--strict-mcp-config", "--no-session-persistence", "--permission-prompts none"} {
		if !strings.Contains(args+" ", want) {
			t.Errorf("args missing %q: %q", want, args)
		}
	}
	if !slices.Contains(call.Args, "") {
		t.Error(`--tools must be followed by an empty argument`)
	}
	if !strings.Contains(call.Args[1], "House: no emoji") || strings.Contains(call.Args[1], "+func Add()") {
		t.Errorf("instruction = %q (house rules in, diff out)", call.Args[1])
	}
	if !strings.Contains(call.Stdin, "+func Add()") {
		t.Errorf("stdin = %q", call.Stdin)
	}
	if call.Mode != exec.Capture {
		t.Error("claude must run captured")
	}
}

func TestDraftPRFallsBackToResultField(t *testing.T) {
	f := exectest.New()
	f.On("claude").Reply(`{"type":"result","is_error":false,"result":"{\"title\":\"T\",\"body\":\"B\"}"}`)
	pr, err := New(f, Config{}, nil).DraftPR(context.Background(), ai.PRInput{Branch: "b", Parent: "a", Diff: "+x"})
	if err != nil || pr.Title != "T" || pr.Body != "B" {
		t.Fatalf("pr = %+v, %v", pr, err)
	}
}

func TestMalformedOutputRetriesOnce(t *testing.T) {
	f := exectest.New()
	n := 0
	f.On("claude").Do(func(exec.Cmd) (exec.Result, error) {
		n++
		if n == 1 {
			return exec.Result{Stdout: []byte(`{"type":"result","is_error":false,"structured_output":{"branch":"","message":""}}`)}, nil
		}
		return exec.Result{Stdout: []byte(okResponse)}, nil
	})
	c, err := New(f, Config{}, nil).DraftCommit(context.Background(), ai.CommitInput{Diff: "+x"})
	if err != nil || c.BranchName != "add-adder" || n != 2 {
		t.Fatalf("retry: %+v %v n=%d", c, err, n)
	}

	f = exectest.New()
	f.On("claude").Reply("not json")
	_, err = New(f, Config{}, nil).DraftCommit(context.Background(), ai.CommitInput{Diff: "+x"})
	if !errors.Is(err, &stack.Error{Kind: stack.KindAPIFailure}) {
		t.Errorf("unparseable: %v", err)
	}

	f = exectest.New()
	f.On("claude").Reply(`{"type":"result","is_error":false,"structured_output":{"branch":"has space","message":"m"}}`)
	_, err = New(f, Config{}, nil).DraftCommit(context.Background(), ai.CommitInput{Diff: "+x"})
	if !errors.Is(err, &stack.Error{Kind: stack.KindAPIFailure}) || len(f.Calls()) != 2 {
		t.Errorf("invalid twice: %v (%d calls)", err, len(f.Calls()))
	}
}

func TestErrorMapping(t *testing.T) {
	f := exectest.New()
	f.On("claude").NotFound()
	if _, err := New(f, Config{Command: "claude"}, nil).DraftCommit(context.Background(), ai.CommitInput{}); !errors.Is(err, &stack.Error{Kind: stack.KindNotInstalled}) {
		t.Errorf("not installed: %v", err)
	}

	f = exectest.New()
	f.On("claude").Reply(`{"type":"result","is_error":true,"result":"Not logged in · Please run /login"}`)
	if _, err := New(f, Config{}, nil).DraftCommit(context.Background(), ai.CommitInput{}); !errors.Is(err, &stack.Error{Kind: stack.KindAuthRequired}) {
		t.Errorf("auth via is_error: %v", err)
	}

	f = exectest.New()
	f.On("claude").Fail(1, "Invalid API key · Please run /login")
	if _, err := New(f, Config{}, nil).DraftCommit(context.Background(), ai.CommitInput{}); !errors.Is(err, &stack.Error{Kind: stack.KindAuthRequired}) {
		t.Errorf("auth via exit: %v", err)
	}

	f = exectest.New()
	f.On("claude").Reply(`{"type":"result","is_error":true,"result":"Prompt is too long"}`)
	_, err := New(f, Config{}, nil).DraftCommit(context.Background(), ai.CommitInput{})
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindAPIFailure || !strings.Contains(se.Detail, "too long") {
		t.Errorf("model error: %v", err)
	}

	f = exectest.New()
	f.On("claude").Do(func(c exec.Cmd) (exec.Result, error) {
		return exec.Result{ExitCode: -1}, context.DeadlineExceeded
	})
	_, err = New(f, Config{Timeout: time.Millisecond}, nil).DraftCommit(context.Background(), ai.CommitInput{})
	se, ok = errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindAPIFailure || !strings.Contains(se.Msg, "did not answer") {
		t.Errorf("timeout: %v", err)
	}
}
