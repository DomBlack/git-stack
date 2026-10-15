package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestInitAdoptsBranches(t *testing.T) {
	f := newRestackFixture(t) // a, b, c tracked; add loose branches on top of each other
	gittest.Run(t, f.dir, "switch", "-q", "-c", "p", "main")
	gittest.Commit(t, f.dir, "p.txt", "p", "p")
	gittest.Run(t, f.dir, "switch", "-q", "-c", "q")
	gittest.Commit(t, f.dir, "q.txt", "q", "q")
	ctx := context.Background()
	res, err := f.app.Init(ctx, f.repo, app.InitOptions{Branches: []string{"p", "q"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Trunk != "main" || !slices.Equal(res.Branches, []string{"p", "q"}) || len(res.NeedsRestack) != 0 {
		t.Errorf("result = %+v", res)
	}
	s, i, ok := f.fb.graph.StackOf("q")
	if !ok || i != 1 || s.Trunk != "main" || s.Branches[0].Base != f.rev(t, "main") || s.Branches[1].Base != f.rev(t, "p") || s.Branches[1].Head != f.rev(t, "q") {
		t.Errorf("stack = %+v", s)
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "q" {
		t.Errorf("current = %s", cur)
	}

	// No branches means the current one.
	gittest.Run(t, f.dir, "switch", "-q", "-c", "r", "main")
	res, err = f.app.Init(ctx, f.repo, app.InitOptions{})
	if err != nil || !slices.Equal(res.Branches, []string{"r"}) {
		t.Errorf("current: %+v %v", res, err)
	}
}

func TestInitReportsNeedsRestack(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "-c", "p", "main")
	gittest.Commit(t, f.dir, "p.txt", "p", "p")
	gittest.Run(t, f.dir, "switch", "-q", "-c", "q", "main") // q is beside p, not on it
	gittest.Commit(t, f.dir, "q.txt", "q", "q")
	res, err := f.app.Init(context.Background(), f.repo, app.InitOptions{Branches: []string{"p", "q"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.NeedsRestack, []string{"q"}) {
		t.Errorf("needs restack = %v", res.NeedsRestack)
	}
	s, _, _ := f.fb.graph.StackOf("q")
	if s.Branches[1].Base != f.rev(t, "main") {
		t.Errorf("q's base should be the merge base with p (main's tip), got %s", s.Branches[1].Base)
	}
	if f.rev(t, "q") == f.rev(t, "p") || !f.has(t, "q", "q.txt") || f.has(t, "q", "p.txt") {
		t.Error("init must not rebase anything")
	}
}

func TestInitTrunkAndRefusals(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "-c", "dev", "main")
	gittest.Run(t, f.dir, "switch", "-q", "-c", "p", "main")
	gittest.Commit(t, f.dir, "p.txt", "p", "p")
	ctx := context.Background()
	var se *stack.Error
	_, err := f.app.Init(ctx, f.repo, app.InitOptions{Branches: []string{"nope"}})
	if !errors.As(err, &se) || se.Kind != stack.KindInvalidArgs || len(se.NextSteps) == 0 || !strings.Contains(se.NextSteps[0], "git stack create") {
		t.Errorf("missing branch: %v", err)
	}
	for _, tc := range []struct {
		o    app.InitOptions
		kind stack.Kind
		want string
	}{
		{app.InitOptions{Branches: []string{"a"}}, stack.KindInvalidArgs, "already in the stack"},
		{app.InitOptions{Branches: []string{"main"}}, stack.KindInvalidArgs, "trunk"},
		{app.InitOptions{Branches: []string{"p", "p"}}, stack.KindInvalidArgs, "twice"},
		{app.InitOptions{Branches: []string{"p"}, Base: "nope"}, stack.KindInvalidArgs, "nope"},
	} {
		_, err := f.app.Init(ctx, f.repo, tc.o)
		if !errors.Is(err, &stack.Error{Kind: tc.kind}) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc.o, err)
		}
	}
	gittest.Run(t, f.dir, "switch", "-q", "main")
	if _, err := f.app.Init(ctx, f.repo, app.InitOptions{}); !errors.Is(err, &stack.Error{Kind: stack.KindInvalidArgs}) {
		t.Errorf("on trunk with no branches: %v", err)
	}
	// --base picks the trunk; dev is a branch beside main and becomes a trunk.
	res, err := f.app.Init(ctx, f.repo, app.InitOptions{Branches: []string{"p"}, Base: "dev"})
	if err != nil || res.Trunk != "dev" {
		t.Fatalf("base: %+v %v", res, err)
	}
	if !slices.Contains(f.fb.graph.Trunks, "dev") {
		t.Error("dev should be a trunk now")
	}
	// Two trunks and no --base: the repository's default branch decides.
	// With no remote that is the local main.
	gittest.Run(t, f.dir, "switch", "-q", "-c", "r", "main")
	res, err = f.app.Init(ctx, f.repo, app.InitOptions{})
	if err != nil || res.Trunk != "main" {
		t.Errorf("two trunks: %+v %v", res, err)
	}
}
