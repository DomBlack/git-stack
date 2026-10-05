package app_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func deleted(res app.SyncResult, name string) (app.DeletedBranch, bool) {
	i := slices.IndexFunc(res.Deleted, func(d app.DeletedBranch) bool { return d.Name == name })
	if i < 0 {
		return app.DeletedBranch{}, false
	}
	return res.Deleted[i], true
}

func kept(res app.SyncResult, name string) (app.KeptBranch, bool) {
	i := slices.IndexFunc(res.Kept, func(k app.KeptBranch) bool { return k.Name == name })
	if i < 0 {
		return app.KeptBranch{}, false
	}
	return res.Kept[i], true
}

func branchExists(t *testing.T, dir, name string) bool {
	t.Helper()
	return strings.Contains(gittest.Run(t, dir, "branch", "--list", name), name)
}

// mergeOnRemote squash merges branch into origin/main (one commit with the
// branch's tree) and marks its PR merged with that merge commit.
func (f *syncFixture) mergeOnRemote(t *testing.T, branch string, prIndex int) string {
	t.Helper()
	cur := gittest.Run(t, f.dir, "branch", "--show-current")
	gittest.Run(t, f.dir, "switch", "-q", "--detach", "refs/remotes/origin/main")
	gittest.Run(t, f.dir, "merge", "-q", "--squash", branch)
	gittest.Run(t, f.dir, "commit", "-q", "-m", "merge "+branch)
	sha := gittest.Run(t, f.dir, "rev-parse", "HEAD")
	gittest.Run(t, f.dir, "push", "-q", "origin", "HEAD:refs/heads/main")
	gittest.Run(t, f.dir, "switch", "-q", cur)
	f.forge.prs[prIndex].State = forge.StateMerged
	f.forge.prs[prIndex].MergeCommit = sha
	f.forge.prs[prIndex].HeadSHA = f.rev(t, branch)
	return sha
}

func TestSyncDeletesMergedTrackedBranch(t *testing.T) {
	f := newSyncFixture(t) // on b; a's PR is open
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	f.mergeOnRemote(t, "a", 0)
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := deleted(res, "a"); !ok || d.Reason != app.ReasonMerged || d.Head == "" {
		t.Errorf("deleted = %+v", res.Deleted)
	}
	if branchExists(t, f.dir, "a") {
		t.Error("a should be gone")
	}
	g, _ := f.deps.Meta.Load(context.Background(), f.repo)
	if len(g.Stacks) != 1 || strings.Join(g.Stacks[0].Names(), ",") != "b" {
		t.Errorf("metadata = %+v", g.Stacks)
	}
}

func TestSyncDeletesFinishedStack(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	f.forge.prs = append(f.forge.prs, forge.PullRequest{Number: 8, Head: "b", Base: "a", State: forge.StateOpen, URL: "u/b"})
	f.mergeOnRemote(t, "a", 0)
	f.mergeOnRemote(t, "b", 1)
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 2 || branchExists(t, f.dir, "a") || branchExists(t, f.dir, "b") {
		t.Errorf("deleted = %+v", res.Deleted)
	}
	if g, _ := f.deps.Meta.Load(context.Background(), f.repo); len(g.Stacks) != 0 {
		t.Errorf("finished stack must be forgotten: %+v", g.Stacks)
	}
}

func TestSyncPrunePolicy(t *testing.T) {
	t.Run("never keeps", func(t *testing.T) {
		f := newSyncFixture(t)
		f.deps.Config.SyncPrune = config.SyncPruneNever
		f.mergeOnRemote(t, "a", 0)
		res, err := f.sync(t, app.SyncOptions{NoRestack: true})
		if err != nil || len(res.Deleted) != 0 {
			t.Fatalf("never: %+v %v", res.Deleted, err)
		}
		if k, ok := kept(res, "a"); !ok || k.Reason != app.KeptPolicy {
			t.Errorf("kept = %+v", res.Kept)
		}
	})
	t.Run("delete-all beats never", func(t *testing.T) {
		f := newSyncFixture(t)
		f.deps.Config.SyncPrune = config.SyncPruneNever
		f.mergeOnRemote(t, "a", 0)
		res, err := f.sync(t, app.SyncOptions{DeleteAll: true, NoRestack: true})
		if err != nil || len(res.Deleted) != 1 {
			t.Errorf("delete-all: %+v %v", res.Deleted, err)
		}
	})
	t.Run("force beats never", func(t *testing.T) {
		f := newSyncFixture(t)
		f.deps.Config.SyncPrune = config.SyncPruneNever
		f.mergeOnRemote(t, "a", 0)
		res, err := f.sync(t, app.SyncOptions{Force: true, NoRestack: true})
		if _, ok := deleted(res, "a"); err != nil || !ok || branchExists(t, f.dir, "a") {
			t.Errorf("force: %+v %v", res.Deleted, err)
		}
	})
	t.Run("ask yes", func(t *testing.T) {
		f := newSyncFixture(t)
		f.deps.Config.SyncPrune = config.SyncPruneAsk
		ap := &askPrompter{answer: true}
		f.deps.Prompter = ap
		f.mergeOnRemote(t, "a", 0)
		res, err := f.sync(t, app.SyncOptions{NoRestack: true})
		if err != nil || len(res.Deleted) != 1 {
			t.Errorf("ask yes: %+v %v", res.Deleted, err)
		}
		if len(ap.asked) != 1 || !strings.Contains(ap.asked[0], "Delete 1 branch (a: merged)?") {
			t.Errorf("question = %v", ap.asked)
		}
	})
	t.Run("ask no", func(t *testing.T) {
		f := newSyncFixture(t)
		f.deps.Config.SyncPrune = config.SyncPruneAsk
		f.deps.Prompter = &askPrompter{answer: false}
		f.mergeOnRemote(t, "a", 0)
		res, err := f.sync(t, app.SyncOptions{NoRestack: true})
		if err != nil || len(res.Deleted) != 0 || !branchExists(t, f.dir, "a") {
			t.Errorf("ask no: %+v %v", res.Deleted, err)
		}
	})
	t.Run("ask without a terminal keeps with a notice", func(t *testing.T) {
		f := newSyncFixture(t)
		f.deps.Config.SyncPrune = config.SyncPruneAsk
		f.mergeOnRemote(t, "a", 0)
		res, err := f.sync(t, app.SyncOptions{NoRestack: true})
		if err != nil || len(res.Deleted) != 0 {
			t.Fatalf("no terminal: %+v %v", res.Deleted, err)
		}
		if !strings.Contains(strings.Join(res.Notices, "\n"), "sync -d") {
			t.Errorf("want a notice pointing at -d: %v", res.Notices)
		}
	})
}

func TestSyncClosedPRIsACandidate(t *testing.T) {
	f := newSyncFixture(t)
	f.forge.prs[0].State = forge.StateClosed
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := deleted(res, "a"); !ok || d.Reason != app.ReasonClosed {
		t.Errorf("deleted = %+v", res.Deleted)
	}
}

func TestSyncKeepsMergedIntoParentNotTrunk(t *testing.T) {
	f := newSyncFixture(t) // b's PR merged into a, a still open
	gittest.Run(t, f.dir, "switch", "-q", "main")
	mergeIntoA := gittest.Run(t, f.dir, "rev-parse", "b") // pretend b merged into a as a commit not in main
	f.forge.prs = append(f.forge.prs, forge.PullRequest{Number: 8, Head: "b", Base: "a", State: forge.StateMerged, MergeCommit: mergeIntoA, HeadSHA: mergeIntoA})
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := kept(res, "b"); !ok || k.Reason != app.KeptNotInTrunk || !branchExists(t, f.dir, "b") {
		t.Errorf("kept = %+v", res.Kept)
	}
}

func TestSyncDeletesOrphanWithMergedPR(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Run(t, f.dir, "switch", "-q", "-c", "orphan")
	tip := gittest.Commit(t, f.dir, "o.txt", "o", "orphan work")
	gittest.Run(t, f.dir, "push", "-q", "-u", "origin", "orphan")
	gittest.Run(t, f.dir, "switch", "-q", "main")
	f.forge.prs = append(f.forge.prs, forge.PullRequest{Number: 9, Head: "orphan", Base: "main", State: forge.StateMerged, HeadSHA: tip})
	f.mergeOnRemote(t, "orphan", 1)
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := deleted(res, "orphan"); !ok || d.Reason != app.ReasonMerged || branchExists(t, f.dir, "orphan") {
		t.Errorf("deleted = %+v", res.Deleted)
	}
}

func TestSyncKeepsRecreatedOrphan(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Run(t, f.dir, "switch", "-q", "-c", "orphan")
	gittest.Commit(t, f.dir, "o.txt", "o", "new work with an old name")
	gittest.Run(t, f.dir, "switch", "-q", "main")
	// A merged PR for a branch of the same name, but of a different commit.
	f.forge.prs = append(f.forge.prs, forge.PullRequest{Number: 9, Head: "orphan", Base: "main", State: forge.StateMerged, HeadSHA: "0000000000000000000000000000000000000000"})
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deleted(res, "orphan"); ok || !branchExists(t, f.dir, "orphan") {
		t.Errorf("a recreated branch must not be deleted: %+v", res.Deleted)
	}
}

func TestSyncKeepsEmptyBranchAtTrunkTip(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "branch", "empty", "main")
	f.deps.Meta = memMeta{stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: f.repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "empty"}}, Worktree: f.repo.TopLevel},
	})}
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil || len(res.Deleted) != 0 || !branchExists(t, f.dir, "empty") {
		t.Errorf("empty branch at trunk tip must stay: %+v %v", res.Deleted, err)
	}
}

func TestSyncDeletesBranchAlreadyInTrunk(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	gittest.Run(t, f.dir, "merge", "-q", "--ff-only", "a")           // a merged by hand, no PR flag
	gittest.Commit(t, f.dir, "after.txt", "after", "trunk moves on") // so a's tip is behind trunk, not equal to it
	gittest.Run(t, f.dir, "push", "-q", "origin", "main")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := deleted(res, "a"); !ok || d.Reason != app.ReasonInTrunk {
		t.Errorf("deleted = %+v", res.Deleted)
	}
}

func TestSyncSkipsBranchCheckedOutElsewhere(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "a")
	f.mergeOnRemote(t, "a", 0)
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := kept(res, "a"); !ok || k.Reason != app.KeptOtherWorktree || !branchExists(t, f.dir, "a") {
		t.Errorf("kept = %+v", res.Kept)
	}
}

func TestSyncMovesCurrentWorktreeOffDeletedBranch(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "a")
	f.mergeOnRemote(t, "a", 0)
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deleted(res, "a"); !ok || gittest.Run(t, f.dir, "branch", "--show-current") != "main" {
		t.Errorf("should be on main with a deleted: %+v", res.Deleted)
	}
}

func TestSyncKeepsDeletedBranchWhenCurrentTreeIsDirty(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "a")
	f.mergeOnRemote(t, "a", 0)
	gittest.WriteFile(t, f.dir, "a.txt", "edited")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := kept(res, "a"); !ok || k.Reason != app.KeptDirty || !branchExists(t, f.dir, "a") {
		t.Errorf("kept = %+v", res.Kept)
	}
}

func TestSyncDropsGoneBranchesFromMetadata(t *testing.T) {
	f := newSyncFixture(t)
	f.deps.Config.SyncPrune = config.SyncPruneNever
	f.deps.Meta = memMeta{stack.NewGraph([]stack.Stack{
		{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: f.repo.TopLevel},
		{Trunk: "main", Branches: []stack.Branch{{Name: "gone", PR: &stack.PRRef{Number: 3, Merged: true}}}, Worktree: f.repo.TopLevel},
	})}
	if _, err := f.sync(t, app.SyncOptions{NoRestack: true}); err != nil {
		t.Fatal(err)
	}
	if g, _ := f.deps.Meta.Load(context.Background(), f.repo); len(g.Stacks) != 1 {
		t.Errorf("gone branch's stack must be forgotten whatever the policy: %+v", g.Stacks)
	}
}

func TestSyncKeepsClosedBranchWithNewLocalWork(t *testing.T) {
	f := newSyncFixture(t)
	f.forge.prs[0].State = forge.StateClosed
	f.forge.prs[0].HeadSHA = f.rev(t, "a")
	gittest.Run(t, f.dir, "switch", "-q", "a")
	gittest.Commit(t, f.dir, "more.txt", "more", "work the PR never saw")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := kept(res, "a"); !ok || k.Reason != app.KeptUnpushed || !branchExists(t, f.dir, "a") || len(res.Deleted) != 0 {
		t.Errorf("kept = %+v deleted = %+v", res.Kept, res.Deleted)
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "kept in case") {
		t.Errorf("notices = %v", res.Notices)
	}
}

func TestSyncDetachesLinkedWorktreeWhenTrunkIsElsewhere(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, f.dir, "worktree", "add", "-q", wt, "a")
	f.mergeOnRemote(t, "a", 0)
	repo, err := f.deps.Git.Discover(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	res, err := app.New(f.deps).Sync(context.Background(), repo, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deleted(res, "a"); !ok || branchExists(t, f.dir, "a") {
		t.Fatalf("a should be deleted: %+v %+v", res.Deleted, res.Kept)
	}
	if cur := gittest.Run(t, wt, "branch", "--show-current"); cur != "" {
		t.Errorf("HEAD should be detached, on %s", cur)
	}
	if got, want := gittest.Run(t, wt, "rev-parse", "HEAD"), gittest.Run(t, f.dir, "rev-parse", "main"); got != want {
		t.Errorf("HEAD = %s, want main %s", got, want)
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "HEAD is now detached at main") {
		t.Errorf("notices = %v", res.Notices)
	}
}

func TestSyncDeletesBranchRestackedSinceItsPRWasPushed(t *testing.T) {
	f := newSyncFixture(t) // on b; origin has a as pushed
	pushed := f.rev(t, "a")
	f.forge.prs[0].HeadSHA = pushed
	f.advanceRemote(t, "main", "r.txt")
	if _, err := f.sync(t, app.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.rev(t, "a") == pushed {
		t.Fatal("a should have been restacked locally")
	}
	// The PR merges at the commit GitHub saw, not the local restack.
	gittest.Run(t, f.dir, "switch", "-q", "--detach", "refs/remotes/origin/main")
	gittest.Run(t, f.dir, "merge", "-q", "--squash", pushed)
	gittest.Run(t, f.dir, "commit", "-q", "-m", "merge a")
	merge := f.rev(t, "HEAD")
	gittest.Run(t, f.dir, "push", "-q", "origin", "HEAD:refs/heads/main")
	gittest.Run(t, f.dir, "switch", "-q", "b")
	f.forge.prs[0].State = forge.StateMerged
	f.forge.prs[0].MergeCommit = merge

	res, err := f.sync(t, app.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := deleted(res, "a"); !ok || d.Reason != app.ReasonMerged || branchExists(t, f.dir, "a") {
		t.Errorf("deleted = %+v kept = %+v notices = %v", res.Deleted, res.Kept, res.Notices)
	}
	if got := gittest.Run(t, f.dir, "log", "--format=%s", "main..b"); got != "feat: b" {
		t.Errorf("b should sit on main with its own commit, got %q", got)
	}
}
