package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// syncFixture is main -> a -> b, with a bare origin holding all three, a
// forge that knows an open PR for a, and a graph tracking a and b. HEAD is
// on b.
type syncFixture struct {
	deps  app.Deps
	meta  memMeta
	forge *recordingForge
	repo  git.Repo
	dir   string
	bare  string
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	gittest.Commit(t, dir, "a.txt", "a", "feat: a")
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "b.txt", "b", "feat: b")
	bare := gittest.InitRemote(t, dir)
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	graph := stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}, Worktree: repo.TopLevel}})
	fg := &recordingForge{prs: []forge.PullRequest{{Number: 7, Head: "a", Base: "main", State: forge.StateOpen, URL: "u/a"}}}
	cfg := config.Defaults()
	cfg.CacheTTL = 0
	meta := memMeta{graph}
	deps := app.Deps{Git: g, Meta: meta, Forge: fg, Cache: cache.New(repo), Config: cfg}
	return &syncFixture{deps: deps, meta: meta, forge: fg, repo: repo, dir: dir, bare: bare}
}

func (f *syncFixture) sync(t *testing.T, o app.SyncOptions) (app.SyncResult, error) {
	t.Helper()
	return app.New(f.deps).Sync(context.Background(), f.repo, o)
}

func (f *syncFixture) rev(t *testing.T, rev string) string {
	t.Helper()
	return gittest.Run(t, f.dir, "rev-parse", rev)
}

// advanceRemote commits file on top of origin/<branch> and pushes it, then
// puts the local checkout back. Returns the new remote tip.
func (f *syncFixture) advanceRemote(t *testing.T, branch, file string) string {
	t.Helper()
	cur := gittest.Run(t, f.dir, "branch", "--show-current")
	gittest.Run(t, f.dir, "switch", "-q", "--detach", "refs/remotes/origin/"+branch)
	// Content includes the parent id so a second push of the same file is never an empty commit.
	sha := gittest.Commit(t, f.dir, file, file+" at "+gittest.Run(t, f.dir, "rev-parse", "HEAD"), "remote: "+file)
	gittest.Run(t, f.dir, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	gittest.Run(t, f.dir, "switch", "-q", cur)
	// Forget what the push taught us so sync's fetch is what brings it in.
	gittest.Run(t, f.dir, "update-ref", "-d", "refs/remotes/origin/"+branch)
	return sha
}

func trunk(res app.SyncResult, name string) app.TrunkSync {
	i := slices.IndexFunc(res.Trunks, func(t app.TrunkSync) bool { return t.Name == name })
	if i < 0 {
		return app.TrunkSync{}
	}
	return res.Trunks[i]
}

func TestSyncFastForwardsTrunkThatIsNotCheckedOut(t *testing.T) {
	f := newSyncFixture(t) // on b
	sha := f.advanceRemote(t, "main", "r.txt")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if ts := trunk(res, "main"); ts.Status != app.TrunkFastForwarded || ts.To != sha {
		t.Errorf("trunk = %+v", ts)
	}
	if f.rev(t, "main") != sha {
		t.Error("main not moved")
	}
	if res.Remote != "origin" {
		t.Errorf("remote = %q", res.Remote)
	}
}

func TestSyncFastForwardsCheckedOutTrunk(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	sha := f.advanceRemote(t, "main", "r.txt")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if trunk(res, "main").Status != app.TrunkFastForwarded || f.rev(t, "main") != sha {
		t.Errorf("trunk = %+v", trunk(res, "main"))
	}
	if _, err := os.Stat(filepath.Join(f.dir, "r.txt")); err != nil {
		t.Error("working tree should have r.txt")
	}
}

func TestSyncDirtyTrunkWorktreeIsLeft(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	before := f.rev(t, "main")
	f.advanceRemote(t, "main", "README.md") // conflicts with the dirty edit below
	gittest.WriteFile(t, f.dir, "README.md", "dirty")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if trunk(res, "main").Status != app.TrunkDirty || f.rev(t, "main") != before {
		t.Errorf("dirty trunk must be left: %+v", trunk(res, "main"))
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "README.md")); string(b) != "dirty" {
		t.Error("the dirty file must be untouched")
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "main") {
		t.Errorf("want a notice about main: %v", res.Notices)
	}
}

func TestSyncUntrackedFileBlocksFastForward(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	before := f.rev(t, "main")
	f.advanceRemote(t, "main", "r.txt")
	gittest.WriteFile(t, f.dir, "r.txt", "mine")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if trunk(res, "main").Status != app.TrunkDirty || f.rev(t, "main") != before {
		t.Errorf("trunk = %+v", trunk(res, "main"))
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "r.txt")); string(b) != "mine" {
		t.Error("the untracked file must be untouched")
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "was not updated (the checkout was left alone: fast forward") {
		t.Errorf("notices = %v", res.Notices)
	}
}

func TestSyncDivergedTrunk(t *testing.T) {
	setup := func(t *testing.T) (*syncFixture, string, string) {
		f := newSyncFixture(t)
		gittest.Run(t, f.dir, "switch", "-q", "main")
		local := gittest.Commit(t, f.dir, "local.txt", "l", "local only")
		remote := f.advanceRemote(t, "main", "r.txt")
		return f, local, remote
	}

	t.Run("no consent leaves it", func(t *testing.T) {
		f, local, _ := setup(t)
		res, err := f.sync(t, app.SyncOptions{NoRestack: true})
		if err != nil || trunk(res, "main").Status != app.TrunkDiverged || f.rev(t, "main") != local {
			t.Errorf("trunk = %+v %v", trunk(res, "main"), err)
		}
	})
	t.Run("force resets", func(t *testing.T) {
		f, _, remote := setup(t)
		res, err := f.sync(t, app.SyncOptions{Force: true, NoRestack: true})
		if err != nil || trunk(res, "main").Status != app.TrunkReset || f.rev(t, "main") != remote {
			t.Errorf("trunk = %+v %v", trunk(res, "main"), err)
		}
	})
	t.Run("force leaves an untracked file the remote adds", func(t *testing.T) {
		f, local, _ := setup(t)
		gittest.WriteFile(t, f.dir, "r.txt", "mine")
		res, err := f.sync(t, app.SyncOptions{Force: true, NoRestack: true})
		if err != nil || trunk(res, "main").Status != app.TrunkDirty || f.rev(t, "main") != local {
			t.Errorf("trunk = %+v %v", trunk(res, "main"), err)
		}
		if b, _ := os.ReadFile(filepath.Join(f.dir, "r.txt")); string(b) != "mine" {
			t.Error("the untracked file must survive")
		}
		if !strings.Contains(strings.Join(res.Notices, "\n"), "would overwrite untracked r.txt") {
			t.Errorf("notices = %v", res.Notices)
		}
	})
	t.Run("yes at the prompt resets", func(t *testing.T) {
		f, _, remote := setup(t)
		ap := &askPrompter{answer: true}
		f.deps.Prompter = ap
		res, err := f.sync(t, app.SyncOptions{NoRestack: true})
		if err != nil || trunk(res, "main").Status != app.TrunkReset || f.rev(t, "main") != remote {
			t.Errorf("trunk = %+v %v", trunk(res, "main"), err)
		}
		if len(ap.asked) != 1 || !strings.Contains(ap.asked[0], "main has diverged") || !strings.Contains(ap.asked[0], "1 local commit") {
			t.Errorf("question = %v", ap.asked)
		}
	})
	t.Run("no at the prompt leaves it", func(t *testing.T) {
		f, local, _ := setup(t)
		f.deps.Prompter = &askPrompter{answer: false}
		res, err := f.sync(t, app.SyncOptions{NoRestack: true})
		if err != nil || trunk(res, "main").Status != app.TrunkDiverged || f.rev(t, "main") != local {
			t.Errorf("trunk = %+v %v", trunk(res, "main"), err)
		}
	})
}

func TestSyncTrunkAheadOfRemote(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	local := gittest.Commit(t, f.dir, "local.txt", "l", "local only")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil || trunk(res, "main").Status != app.TrunkAhead || f.rev(t, "main") != local {
		t.Errorf("trunk = %+v %v", trunk(res, "main"), err)
	}
}

func TestSyncNoStacksStillUpdatesTrunk(t *testing.T) {
	f := newSyncFixture(t)
	f.deps.Meta = memMeta{stack.NewGraph(nil)}
	sha := f.advanceRemote(t, "main", "r.txt")
	res, err := f.sync(t, app.SyncOptions{})
	if err != nil || trunk(res, "main").Status != app.TrunkFastForwarded || f.rev(t, "main") != sha {
		t.Errorf("trunk = %+v %v", trunk(res, "main"), err)
	}
}

func TestSyncNoRemoteBranchForTrunk(t *testing.T) {
	f := newSyncFixture(t)
	// A bare repo refuses to delete the branch its HEAD points at.
	gittest.Run(t, f.bare, "config", "receive.denyDeleteCurrent", "ignore")
	gittest.Run(t, f.dir, "push", "-q", "origin", "--delete", "main")
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil || trunk(res, "main").Status != app.TrunkNoRemote {
		t.Errorf("trunk = %+v %v", trunk(res, "main"), err)
	}
}

func TestSyncFetchFailure(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))
	before := f.rev(t, "main")
	res, err := f.sync(t, app.SyncOptions{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindAPIFailure}) {
		t.Errorf("want api_failure, got %v", err)
	}
	if len(res.Trunks) != 0 || f.rev(t, "main") != before {
		t.Errorf("nothing may run after a failed fetch: %+v", res.Trunks)
	}
	// The pull request list runs alongside the fetch, so it may well have
	// been asked; what matters is that nothing moved and no phase ran.
}

func TestSyncAsksTheForgeOnceWhateverTheCache(t *testing.T) {
	f := newSyncFixture(t)
	f.deps.Config.CacheTTL = time.Hour
	gittest.Run(t, f.dir, "switch", "-q", "main")
	// A fresh cache that still says a's PR is open.
	if _, err := app.New(f.deps).RefreshPRs(context.Background(), f.repo); err != nil {
		t.Fatal(err)
	}
	f.mergeOnRemote(t, "a", 0)
	f.forge.lists = 0
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deleted(res, "a"); !ok {
		t.Errorf("the merge must be seen through a fresh cache: %+v", res.Deleted)
	}
	if f.forge.lists != 1 {
		t.Errorf("forge listed %d times, want 1", f.forge.lists)
	}
}
