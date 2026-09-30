package stack_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestErrorKindsMatchAndWrap(t *testing.T) {
	cause := errors.New("exit 3")
	err := fmt.Errorf("modify: %w", stack.New(stack.KindConflict, "rebase stopped").WithSteps("resolve", "continue").WithCause(cause))

	se, ok := errors.AsType[*stack.Error](err)
	if !ok {
		t.Fatalf("AsType failed for %v", err)
	}
	if se.Kind != stack.KindConflict || len(se.NextSteps) != 2 {
		t.Errorf("unexpected error %+v", se)
	}
	if !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
		t.Error("errors.Is by kind should match")
	}
	if errors.Is(err, &stack.Error{Kind: stack.KindLocked}) {
		t.Error("errors.Is should not match a different kind")
	}
	if !errors.Is(err, cause) {
		t.Error("cause should be reachable through Unwrap")
	}
	if stack.KindConflict.Code() != "conflict" || stack.Kind(99).Code() != "kind_99" {
		t.Error("codes")
	}
}
