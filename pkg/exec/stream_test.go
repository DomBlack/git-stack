package exec_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
)

func TestRunStreamsStderrWhileCapturing(t *testing.T) {
	var stream bytes.Buffer
	res, err := exec.New().Run(context.Background(), exec.Cmd{
		Name:   "sh",
		Args:   []string{"-c", "echo out; echo progress one >&2; echo progress two >&2"},
		Stream: &stream,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Err(); got != "progress one\nprogress two" {
		t.Errorf("stderr still has to be captured, got %q", got)
	}
	if got := stream.String(); got != "progress one\nprogress two\n" {
		t.Errorf("stream = %q; stderr should be relayed as it is produced", got)
	}
	if got := res.Out(); got != "out" {
		t.Errorf("stdout = %q; stdout is captured only", got)
	}
}

func TestRunStreamIsIgnoredInPassthrough(t *testing.T) {
	// Passthrough has no runner TTY here, so it must fail before touching
	// Stream; this just pins that Stream never makes passthrough "work".
	var stream bytes.Buffer
	_, err := exec.New().Run(context.Background(), exec.Cmd{Name: "true", Mode: exec.Passthrough, Stream: &stream})
	if err == nil || stream.Len() != 0 {
		t.Errorf("passthrough without a TTY must still fail (err=%v, stream=%q)", err, stream.String())
	}
}
