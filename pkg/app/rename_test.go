package app_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// renameForge records every RenameBranch call and fails them with err.
type renameForge struct {
	fakeForge
	renames [][2]string
	err     error
}

func (f *renameForge) RenameBranch(_ context.Context, _ git.Repo, from, to string) error {
	if f.err != nil {
		return f.err
	}
	f.renames = append(f.renames, [2]string{from, to})
	return nil
}

// renameFixture is main -> a -> b -> c with HEAD on c, a forge that
// records renames and, with pushed, an origin holding every branch.
func renameFixture(t *testing.T, pushed bool) (*restackFixture, *renameForge) {
	t.Helper()
	f := newRestackFixture(t)
	if pushed {
		gittest.InitRemote(t, f.dir)
	}
	rf := &renameForge{}
	f.app = app.New(app.Deps{Git: f.fb.git, Meta: f.fb, Tracker: f.fb, Forge: rf, Cache: cache.New(f.repo)})
	return f, rf
}

func TestRenameUnpushedBranchStaysLocal(t *testing.T) {
	f, rf := renameFixture(t, false)
	gittest.Run(t, f.dir, "switch", "-q", "b")
	tip, base := f.rev(t, "b"), f.base("b")
	res, err := f.app.Rename(context.Background(), f.repo, app.RenameOptions{Name: "b2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "b" || res.To != "b2" || res.Remote != "" {
		t.Errorf("result = %+v", res)
	}
	if len(rf.renames) != 0 {
		t.Errorf("a branch that was never pushed has nothing to rename on GitHub: %v", rf.renames)
	}
	if !slices.Equal(f.names(), []string{"a", "b2", "c"}) {
		t.Errorf("stack = %v", f.names())
	}
	if branchExists(t, f.dir, "b") || f.rev(t, "b2") != tip || f.base("b2") != base {
		t.Error("b2 should be b under a new name, with its base")
	}
	if cur := gittest.Run(t, f.dir, "branch", "--show-current"); cur != "b2" {
		t.Errorf("current = %s", cur)
	}
}

func TestRenamePushedBranchRenamesItOnGitHubToo(t *testing.T) {
	f, rf := renameFixture(t, true)
	f.fb.graph.Stacks[0].Branches[2].PR = &stack.PRRef{Number: 7}
	withPRCache(t, f)
	res, err := f.app.Rename(context.Background(), f.repo, app.RenameOptions{Name: "c2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Remote != "origin" || !slices.Equal(rf.renames, [][2]string{{"c", "c2"}}) {
		t.Errorf("result = %+v, renames = %v", res, rf.renames)
	}
	if pr := f.fb.graph.Stacks[0].Branches[2].PR; pr == nil || pr.Number != 7 {
		t.Errorf("c2 should keep its pull request: %+v", pr)
	}
	if up := gittest.Run(t, f.dir, "rev-parse", "--abbrev-ref", "c2@{upstream}"); up != "origin/c2" {
		t.Errorf("upstream = %s", up)
	}
	if _, err := gittest.RunErr(t, f.dir, "rev-parse", "--verify", "-q", "refs/remotes/origin/c"); err == nil {
		t.Error("the old remote tracking ref should be gone")
	}
	if _, err := os.Stat(cache.New(f.repo).Path("prs")); !os.IsNotExist(err) {
		t.Error("the PR cache names the old branch and should be dropped")
	}
}

func withPRCache(t *testing.T, f *restackFixture) {
	t.Helper()
	if err := cache.Write(cache.New(f.repo), "prs", app.PRSnapshot{PRs: []forge.PullRequest{{Number: 7, Head: "c", State: forge.StateOpen}}}); err != nil {
		t.Fatal(err)
	}
}

// When GitHub says no, nothing local has changed and the error says how to
// rename just this copy.
func TestRenameGitHubFailureChangesNothing(t *testing.T) {
	f, rf := renameFixture(t, true)
	rf.err = stack.New(stack.KindAPIFailure, "Resource not accessible by integration")
	_, err := f.app.Rename(context.Background(), f.repo, app.RenameOptions{Name: "c2"})
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || !strings.Contains(strings.Join(se.NextSteps, "\n"), "git stack rename --local c2") {
		t.Fatalf("err = %v", err)
	}
	if !branchExists(t, f.dir, "c") || branchExists(t, f.dir, "c2") || !slices.Equal(f.names(), []string{"a", "b", "c"}) {
		t.Error("nothing should have changed")
	}
}

func TestRenameLocalLeavesGitHubAlone(t *testing.T) {
	f, rf := renameFixture(t, true)
	f.fb.graph.Stacks[0].Branches[2].PR = &stack.PRRef{Number: 7}
	res, err := f.app.Rename(context.Background(), f.repo, app.RenameOptions{Name: "c2", Local: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Remote != "" || len(rf.renames) != 0 {
		t.Errorf("result = %+v, renames = %v", res, rf.renames)
	}
	if len(res.Notices) != 1 || !strings.Contains(res.Notices[0], "#7") {
		t.Errorf("notices = %v", res.Notices)
	}
	if f.fb.graph.Stacks[0].Branches[2].PR != nil {
		t.Error("c2 has no pull request of its own on GitHub yet")
	}
	if !slices.Equal(f.names(), []string{"a", "b", "c2"}) {
		t.Errorf("stack = %v", f.names())
	}
}

func TestRenameRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, f *restackFixture)
		to     string
		kind   stack.Kind
		substr string
	}{
		{"trunk", func(t *testing.T, f *restackFixture) { gittest.Run(t, f.dir, "switch", "-q", "main") }, "trunk2", stack.KindInvalidArgs, "is a trunk"},
		{"untracked", func(t *testing.T, f *restackFixture) { gittest.Run(t, f.dir, "switch", "-q", "-c", "loose") }, "loose2", stack.KindNotInStack, "not in a stack"},
		{"taken", nil, "a", stack.KindInvalidArgs, "already exists"},
		{"same", nil, "c", stack.KindInvalidArgs, "already called c"},
		{"invalid", nil, "bad..name", stack.KindInvalidArgs, "not a valid branch name"},
		{"taken on the remote", func(t *testing.T, f *restackFixture) {
			gittest.Run(t, f.dir, "update-ref", "refs/remotes/origin/elsewhere", "HEAD")
		}, "elsewhere", stack.KindInvalidArgs, "already exists on origin"},
		{"rebasing", func(t *testing.T, f *restackFixture) {
			if err := os.MkdirAll(f.repo.GitDir+"/rebase-merge", 0o755); err != nil {
				t.Fatal(err)
			}
		}, "c2", stack.KindRebaseActive, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, rf := renameFixture(t, true)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			_, err := f.app.Rename(context.Background(), f.repo, app.RenameOptions{Name: tc.to})
			se, ok := errors.AsType[*stack.Error](err)
			if !ok || se.Kind != tc.kind || !strings.Contains(se.Msg, tc.substr) {
				t.Fatalf("err = %v", err)
			}
			if len(rf.renames) != 0 {
				t.Errorf("nothing should be renamed on GitHub: %v", rf.renames)
			}
		})
	}
}
