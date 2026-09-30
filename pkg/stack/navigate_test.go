package stack_test

import (
	"errors"
	"testing"

	"github.com/DomBlack/git-stack/pkg/stack"
)

func graph() *stack.Graph {
	return stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}, {Name: "c"}}},
		{Trunk: "main", Branches: []stack.Branch{{Name: "x"}, {Name: "y", PR: &stack.PRRef{Number: 2, Merged: true}}, {Name: "z"}}},
		{Trunk: "release", Branches: []stack.Branch{{Name: "r1"}}},
	})
}

func TestGraphQueries(t *testing.T) {
	g := graph()
	if got := g.Trunks; len(got) != 2 || got[0] != "main" || got[1] != "release" {
		t.Errorf("Trunks = %v", got)
	}
	if p, ok := g.Parent("a"); !ok || p != "main" {
		t.Errorf("Parent(a) = %q %v", p, ok)
	}
	if p, ok := g.Parent("c"); !ok || p != "b" {
		t.Errorf("Parent(c) = %q %v", p, ok)
	}
	if _, ok := g.Parent("main"); ok {
		t.Error("trunk has no parent")
	}
	if ch := g.Children("main"); len(ch) != 2 || ch[0] != "a" || ch[1] != "x" {
		t.Errorf("Children(main) = %v", ch)
	}
	if ch := g.Children("b"); len(ch) != 1 || ch[0] != "c" {
		t.Errorf("Children(b) = %v", ch)
	}
	if ch := g.Children("c"); ch != nil {
		t.Errorf("Children(c) = %v", ch)
	}
	if !g.Tracked("main") || !g.Tracked("z") || g.Tracked("nope") {
		t.Error("Tracked")
	}
	if s, i, ok := g.StackOf("y"); !ok || i != 1 || s.Bottom() != "x" || s.Top() != "z" {
		t.Errorf("StackOf(y) = %v %d %v", s, i, ok)
	}
	if n := len(g.Branches()); n != 7 {
		t.Errorf("Branches = %d", n)
	}
}

func TestNavigate(t *testing.T) {
	g := graph()
	tests := []struct {
		name    string
		req     stack.NavRequest
		target  string
		moved   int
		clamped bool
		msg     string
	}{
		{"up one", stack.NavRequest{From: "a", Dir: stack.Up}, "b", 1, false, ""},
		{"up two", stack.NavRequest{From: "a", Dir: stack.Up, Steps: 2}, "c", 2, false, ""},
		{"up clamps to top silently", stack.NavRequest{From: "a", Dir: stack.Up, Steps: 5}, "c", 2, true, ""},
		{"up at top", stack.NavRequest{From: "c", Dir: stack.Up}, "c", 0, true, stack.MsgAlreadyTop},
		{"up skips merged", stack.NavRequest{From: "x", Dir: stack.Up}, "z", 1, false, ""},
		{"up from merged branch", stack.NavRequest{From: "y", Dir: stack.Up}, "z", 1, false, ""},
		{"down one", stack.NavRequest{From: "c", Dir: stack.Down}, "b", 1, false, ""},
		{"down from bottom goes to trunk", stack.NavRequest{From: "a", Dir: stack.Down}, "main", 1, false, ""},
		{"down past bottom lands on trunk", stack.NavRequest{From: "b", Dir: stack.Down, Steps: 5}, "main", 2, true, ""},
		{"down skips merged", stack.NavRequest{From: "z", Dir: stack.Down}, "x", 1, false, ""},
		{"down from trunk", stack.NavRequest{From: "main", Dir: stack.Down}, "main", 0, true, stack.MsgAlreadyBottom},
		{"top", stack.NavRequest{From: "a", Dir: stack.Top}, "c", 2, false, ""},
		{"top already", stack.NavRequest{From: "c", Dir: stack.Top}, "c", 0, true, stack.MsgAlreadyTop},
		{"bottom", stack.NavRequest{From: "c", Dir: stack.Bottom}, "a", 2, false, ""},
		{"bottom already", stack.NavRequest{From: "a", Dir: stack.Bottom}, "a", 0, true, "Already at the bottom of the stack."},
		{"up from single-stack trunk", stack.NavRequest{From: "release", Dir: stack.Up}, "r1", 1, false, ""},
		{"top from trunk with --to", stack.NavRequest{From: "main", Dir: stack.Top, To: "y"}, "z", 2, false, ""},
		{"up from trunk with --to and steps", stack.NavRequest{From: "main", Dir: stack.Up, To: "c", Steps: 2}, "b", 2, false, ""},
		{"bottom from trunk with --to", stack.NavRequest{From: "main", Dir: stack.Bottom, To: "z"}, "x", 1, false, ""},
		{"up with --to along the path", stack.NavRequest{From: "a", Dir: stack.Up, To: "c"}, "b", 1, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := stack.Navigate(g, tc.req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Target != tc.target || res.Moved != tc.moved || res.Clamped != tc.clamped || res.Message != tc.msg {
				t.Errorf("got %+v, want target=%s moved=%d clamped=%v msg=%q", res, tc.target, tc.moved, tc.clamped, tc.msg)
			}
		})
	}
}

func TestNavigateErrors(t *testing.T) {
	g := graph()
	cases := []struct {
		name string
		req  stack.NavRequest
		kind stack.Kind
	}{
		{"untracked", stack.NavRequest{From: "nope", Dir: stack.Up}, stack.KindNotInStack},
		{"ambiguous trunk", stack.NavRequest{From: "main", Dir: stack.Up}, stack.KindDisambiguate},
		{"ambiguous top", stack.NavRequest{From: "main", Dir: stack.Top}, stack.KindDisambiguate},
		{"to in other stack", stack.NavRequest{From: "a", Dir: stack.Up, To: "z"}, stack.KindInvalidArgs},
		{"to not upstack", stack.NavRequest{From: "c", Dir: stack.Up, To: "a"}, stack.KindInvalidArgs},
		{"to unknown from trunk", stack.NavRequest{From: "main", Dir: stack.Up, To: "nope"}, stack.KindInvalidArgs},
		{"trunk without stacks", stack.NavRequest{From: "orphan", Dir: stack.Up}, stack.KindNotInStack},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := stack.Navigate(g, tc.req)
			if !errors.Is(err, &stack.Error{Kind: tc.kind}) {
				t.Errorf("err = %v, want kind %v", err, tc.kind)
			}
		})
	}
	empty := stack.NewGraph([]stack.Stack{{Trunk: "main"}})
	res, err := stack.Navigate(empty, stack.NavRequest{From: "main", Dir: stack.Up})
	if err != nil || !res.Clamped {
		t.Errorf("empty stack from trunk: %+v %v", res, err)
	}
}
