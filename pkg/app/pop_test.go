package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func (f *restackFixture) current(t *testing.T) string {
	t.Helper()
	return gittest.Run(t, f.dir, "branch", "--show-current")
}

func (f *restackFixture) staged(t *testing.T) []string {
	t.Helper()
	out := gittest.Run(t, f.dir, "diff", "--cached", "--name-only")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func (f *restackFixture) exists(t *testing.T, branch string) bool {
	t.Helper()
	_, err := gittest.RunErr(t, f.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func (f *restackFixture) pop(t *testing.T) (app.PopResult, error) {
	t.Helper()
	return f.app.Pop(context.Background(), f.repo)
}

func TestPopTheTopBranch(t *testing.T) {
	f := newRestackFixture(t) // main -> a -> b -> c, HEAD on c
	res, err := f.pop(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "c" || res.Parent != "b" || res.StackGone || len(res.Restacked) != 0 {
		t.Errorf("result = %+v", res)
	}
	if cur := f.current(t); cur != "b" {
		t.Errorf("current = %s", cur)
	}
	if got := f.staged(t); !slices.Equal(got, []string{"c.txt"}) {
		t.Errorf("staged = %v", got)
	}
	if f.exists(t, "c") {
		t.Error("c should be deleted")
	}
	if !slices.Equal(f.names(), []string{"a", "b"}) {
		t.Errorf("stack = %v", f.names())
	}
}

func TestPopAMiddleBranchRestacksTheOnesAbove(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "b")
	res, err := f.pop(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "b" || res.Parent != "a" || !slices.Equal(res.Restacked, []string{"c"}) {
		t.Errorf("result = %+v", res)
	}
	if cur := f.current(t); cur != "a" {
		t.Errorf("current = %s", cur)
	}
	if got := f.staged(t); !slices.Equal(got, []string{"b.txt"}) {
		t.Errorf("staged = %v", got)
	}
	if f.exists(t, "b") {
		t.Error("b should be deleted")
	}
	if !slices.Equal(f.names(), []string{"a", "c"}) {
		t.Errorf("stack = %v", f.names())
	}
	if f.has(t, "c", "b.txt") || !f.has(t, "c", "c.txt") || !f.has(t, "c", "a.txt") {
		t.Error("c should sit on a without b's file")
	}
	if f.base("c") != f.rev(t, "a") {
		t.Errorf("c's base = %s, want a's tip", f.base("c"))
	}
}

func TestPopTheBottomBranchLandsOnTrunk(t *testing.T) {
	f := newRestackFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "a")
	res, err := f.pop(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.Parent != "main" || !slices.Equal(res.Restacked, []string{"b", "c"}) {
		t.Errorf("result = %+v", res)
	}
	if cur := f.current(t); cur != "main" {
		t.Errorf("current = %s", cur)
	}
	if got := f.staged(t); !slices.Equal(got, []string{"a.txt"}) {
		t.Errorf("staged = %v", got)
	}
	if !slices.Equal(f.names(), []string{"b", "c"}) {
		t.Errorf("stack = %v", f.names())
	}
	if f.has(t, "b", "a.txt") || f.has(t, "c", "a.txt") || !f.has(t, "c", "b.txt") {
		t.Error("b and c should sit on main without a's file")
	}
}

func TestPopTheOnlyBranchRemovesTheStack(t *testing.T) {
	f := newLinearFixture(t, []fileCommit{{"a", "a.txt", "a"}})
	res, err := f.pop(t)
	if err != nil {
		t.Fatal(err)
	}
	if !res.StackGone || res.Parent != "main" {
		t.Errorf("result = %+v", res)
	}
	if len(f.fb.graph.Stacks) != 0 {
		t.Errorf("stacks = %v", f.fb.graph.Stacks)
	}
	if cur := f.current(t); cur != "main" {
		t.Errorf("current = %s", cur)
	}
	if got := f.staged(t); !slices.Equal(got, []string{"a.txt"}) {
		t.Errorf("staged = %v", got)
	}
}

func TestPopKeepsUntrackedFiles(t *testing.T) {
	f := newRestackFixture(t)
	gittest.WriteFile(t, f.dir, "notes.txt", "mine")
	if _, err := f.pop(t); err != nil {
		t.Fatal(err)
	}
	if st := gittest.Run(t, f.dir, "status", "--porcelain", "--", "notes.txt"); st != "?? notes.txt" {
		t.Errorf("status of notes.txt = %q", st)
	}
}

func TestPopRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, f *restackFixture)
		kind stack.Kind
		want string
	}{
		{"dirty tree", func(t *testing.T, f *restackFixture) {
			gittest.WriteFile(t, f.dir, "a.txt", "changed")
		}, stack.KindInvalidArgs, "a.txt"},
		{"parent checked out elsewhere", func(t *testing.T, f *restackFixture) {
			gittest.Run(t, f.dir, "worktree", "add", "-q", filepath.Join(t.TempDir(), "wt"), "b")
		}, stack.KindInvalidArgs, "checked out in"},
		{"not on its parent", func(t *testing.T, f *restackFixture) {
			f.amend(t, "b", "b2.txt")
		}, stack.KindInvalidArgs, "restack"},
		{"merged branch", func(t *testing.T, f *restackFixture) {
			f.fb.graph.Stacks[0].Branches[0].PR = &stack.PRRef{Number: 1, Merged: true}
		}, stack.KindInvalidArgs, "sync"},
		{"detached", func(t *testing.T, f *restackFixture) {
			gittest.Run(t, f.dir, "switch", "-q", "--detach", "c")
		}, stack.KindInvalidArgs, "detached"},
		{"trunk", func(t *testing.T, f *restackFixture) {
			gittest.Run(t, f.dir, "switch", "-q", "main")
		}, stack.KindInvalidArgs, "trunk"},
		{"untracked branch", func(t *testing.T, f *restackFixture) {
			gittest.Run(t, f.dir, "switch", "-q", "-c", "loose")
		}, stack.KindNotInStack, "init"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRestackFixture(t)
			tc.prep(t, f)
			tips := map[string]string{"a": f.rev(t, "a"), "b": f.rev(t, "b"), "c": f.rev(t, "c")}
			_, err := f.pop(t)
			var se *stack.Error
			if !errors.As(err, &se) || se.Kind != tc.kind || !strings.Contains(se.Error()+strings.Join(se.NextSteps, " "), tc.want) {
				t.Fatalf("want %s mentioning %q, got %v", tc.kind, tc.want, err)
			}
			if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
				t.Errorf("stack = %v", f.names())
			}
			for n, tip := range tips {
				if f.rev(t, n) != tip {
					t.Errorf("%s moved", n)
				}
			}
		})
	}
}

// popConflictFixture: main -> a (a.txt) -> b (shared.txt "b") -> c
// (shared.txt "c"), HEAD on b. Popping b replays c's edit of b's line onto
// a, which conflicts.
func popConflictFixture(t *testing.T) *restackFixture {
	f := newLinearFixture(t, []fileCommit{{"a", "a.txt", "a"}, {"b", "shared.txt", "b"}, {"c", "shared.txt", "c"}})
	gittest.Run(t, f.dir, "switch", "-q", "b")
	return f
}

func TestPopConflictThenContinueFinishesThePop(t *testing.T) {
	f := popConflictFixture(t)
	ctx := context.Background()
	_, err := f.pop(t)
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindConflict || se.Branch != "c" {
		t.Fatalf("want a conflict on c, got %v", err)
	}
	if !f.exists(t, "b") {
		t.Fatal("b must survive until the pop finishes")
	}
	gittest.WriteFile(t, f.dir, "shared.txt", "c")
	gittest.Run(t, f.dir, "add", "shared.txt")
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Continue: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Popped != "b" || res.PoppedOnto != "a" {
		t.Errorf("popped %q onto %q", res.Popped, res.PoppedOnto)
	}
	if cur := f.current(t); cur != "a" {
		t.Errorf("current = %s", cur)
	}
	if got := f.staged(t); !slices.Equal(got, []string{"shared.txt"}) {
		t.Errorf("staged = %v", got)
	}
	if got := gittest.Run(t, f.dir, "show", ":shared.txt"); got != "b" {
		t.Errorf("staged shared.txt = %q, want b's version", got)
	}
	if f.exists(t, "b") {
		t.Error("b should be deleted")
	}
	if !slices.Equal(f.names(), []string{"a", "c"}) || f.stateExists() {
		t.Errorf("stack = %v, state left = %v", f.names(), f.stateExists())
	}
}

func TestPopConflictThenAbortPutsEverythingBack(t *testing.T) {
	f := popConflictFixture(t)
	ctx := context.Background()
	tips := map[string]string{"a": f.rev(t, "a"), "b": f.rev(t, "b"), "c": f.rev(t, "c")}
	bases := map[string]string{"a": f.base("a"), "b": f.base("b"), "c": f.base("c")}
	if _, err := f.pop(t); !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
		t.Fatalf("want a conflict, got %v", err)
	}
	res, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true})
	if err != nil || !res.OrderRestored {
		t.Fatalf("abort: %+v %v", res, err)
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Errorf("stack = %v", f.names())
	}
	for n, tip := range tips {
		if f.rev(t, n) != tip || f.base(n) != bases[n] {
			t.Errorf("%s: tip %s base %s, want %s %s", n, f.rev(t, n), f.base(n), tip, bases[n])
		}
	}
	if cur := f.current(t); cur != "b" {
		t.Errorf("current = %s", cur)
	}
	if got := f.staged(t); len(got) != 0 {
		t.Errorf("staged = %v", got)
	}
}

func TestPopQueuesThePullRequestAndTheStack(t *testing.T) {
	f := newRestackFixture(t)
	f.fb.graph.Stacks[0].ID, f.fb.graph.Stacks[0].Number = "S1", 1
	store := cache.New(f.repo)
	prs := app.PRSnapshot{PRs: []forge.PullRequest{{Number: 12, Head: "b", State: forge.StateOpen}}}
	if err := cache.Write(store, "prs", prs); err != nil {
		t.Fatal(err)
	}
	f.app = app.New(app.Deps{Git: f.fb.git, Meta: f.fb, Tracker: f.fb, Cache: store})
	gittest.Run(t, f.dir, "switch", "-q", "b")
	tip := f.rev(t, "b")
	res, err := f.pop(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.Closing != 12 {
		t.Errorf("closing = %d", res.Closing)
	}
	data, err := os.ReadFile(filepath.Join(f.repo.CommonDir, "git-stack", "remote-stacks.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id":"S1"`, `"number":12`, `"branch":"b"`, `"tip":"` + tip + `"`, "Closed by git stack pop"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("queue %s lacks %s", data, want)
		}
	}
	if f.fb.graph.Stacks[0].ID != "" {
		t.Error("the stack's remote id should be cleared")
	}
}

func TestPopAbortForgetsTheQueuedClose(t *testing.T) {
	f := popConflictFixture(t)
	store := cache.New(f.repo)
	prs := app.PRSnapshot{PRs: []forge.PullRequest{{Number: 12, Head: "b", State: forge.StateOpen}}}
	if err := cache.Write(store, "prs", prs); err != nil {
		t.Fatal(err)
	}
	f.app = app.New(app.Deps{Git: f.fb.git, Meta: f.fb, Tracker: f.fb, Cache: store})
	ctx := context.Background()
	if _, err := f.pop(t); !errors.Is(err, &stack.Error{Kind: stack.KindConflict}) {
		t.Fatalf("want a conflict, got %v", err)
	}
	path := filepath.Join(f.repo.CommonDir, "git-stack", "remote-stacks.json")
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), `"number":12`) {
		t.Fatalf("the close should be queued while the pop is stopped: %q %v", data, err)
	}
	if _, err := f.app.Restack(ctx, f.repo, app.RestackOptions{Abort: true}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), `"number":12`) {
		t.Errorf("abort should forget the close: %s", data)
	}
}
