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
	if n := res.NotUpdated; len(n) != 1 || n[0].Reason != app.NotUpdatedDirty || !slices.Equal(n[0].Changed, []string{"README.md"}) {
		t.Errorf("want main left for README.md: %+v", n)
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
	if n := res.NotUpdated; len(n) != 1 || n[0].Reason != app.NotUpdatedUntracked || !slices.Equal(n[0].Untracked, []string{"r.txt"}) {
		t.Errorf("want main left for r.txt: %+v", n)
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
		if n := res.NotUpdated; len(n) != 1 || n[0].Reason != app.NotUpdatedUntracked || !slices.Equal(n[0].Untracked, []string{"r.txt"}) {
			t.Errorf("want main left for r.txt: %+v", n)
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
	t.Run("the question is never asked under a step", func(t *testing.T) {
		for _, answer := range []bool{true, false} {
			f, _, _ := setup(t)
			g := &stepGuard{t: t, answer: answer}
			f.deps.Prompter, f.deps.Progress = g, g.progress
			if _, err := f.sync(t, app.SyncOptions{NoRestack: true}); err != nil {
				t.Fatal(err)
			}
			want := []string{"step Updating main", "end", "ask", "step Resetting main to origin/main", "end"}
			if !answer {
				want = want[:3]
			}
			got := slices.DeleteFunc(slices.Clone(g.events), func(e string) bool {
				return strings.HasPrefix(e, "step ") && !strings.Contains(e, "main") || e == "end-other"
			})
			if !slices.Equal(got[:len(want)], want) {
				t.Errorf("answer %v: events = %q, want %q first", answer, got, want)
			}
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

// stepGuard is a Progress hook and a Prompter that fails the test if a
// question is asked while a step is running, which would put a spinner
// over the prompt.
type stepGuard struct {
	t      *testing.T
	answer bool
	active int
	events []string
}

func (g *stepGuard) progress(ctx context.Context, _ app.Phase, msg string, fn func(context.Context) error) error {
	g.active++
	g.events = append(g.events, "step "+msg)
	defer func() {
		g.active--
		if strings.Contains(msg, "main") {
			g.events = append(g.events, "end")
		} else {
			g.events = append(g.events, "end-other")
		}
	}()
	return fn(ctx)
}

func (g *stepGuard) Confirm(q string, _ bool) (bool, error) {
	if g.active > 0 {
		g.t.Errorf("asked %q while a step was running", q)
	}
	g.events = append(g.events, "ask")
	return g.answer, nil
}

func (g *stepGuard) Select(q string, _ []string) (int, error) {
	if g.active > 0 {
		g.t.Errorf("asked %q while a step was running", q)
	}
	return 0, nil
}

// A trunk checked out somewhere whose index lock another git process holds:
// a brief hold is waited out, one that stays fails the sync with a clear
// reason, and the lock file is never touched.
func TestSyncTrunkWithAHeldIndexLock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release time.Duration // 0: never
	}{
		{"released while we wait", 300 * time.Millisecond},
		{"held throughout", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSyncFixture(t)
			gittest.Run(t, f.dir, "switch", "-q", "main")
			before := f.rev(t, "main")
			remote := f.advanceRemote(t, "main", "r.txt")
			lock := filepath.Join(f.dir, ".git", "index.lock")
			if err := os.WriteFile(lock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.release > 0 {
				time.AfterFunc(tc.release, func() { _ = os.Remove(lock) })
			}
			res, err := f.sync(t, app.SyncOptions{NoRestack: true})
			if tc.release > 0 {
				if err != nil || trunk(res, "main").Status != app.TrunkFastForwarded || f.rev(t, "main") != remote {
					t.Fatalf("a brief lock should be waited out: %+v %v", trunk(res, "main"), err)
				}
				return
			}
			if !errors.Is(err, &stack.Error{Kind: stack.KindPartial}) {
				t.Fatalf("err = %v, want a partial sync", err)
			}
			if msg := err.Error(); strings.Contains(msg, "\n") || !strings.Contains(msg, "another git process holds its index lock, so main was not updated") {
				t.Errorf("error should be one clear line: %q", msg)
			}
			if trunk(res, "main").Status != app.TrunkNotUpdated || f.rev(t, "main") != before {
				t.Errorf("trunk = %+v", trunk(res, "main"))
			}
			if len(res.NotUpdated) != 1 || res.NotUpdated[0].Reason != app.NotUpdatedLocked || !strings.HasSuffix(res.NotUpdated[0].Lock, filepath.Join(".git", "index.lock")) {
				t.Errorf("not updated = %+v", res.NotUpdated)
			}
			if _, err := os.Stat(lock); err != nil {
				t.Error("sync must never remove someone else's lock")
			}
		})
	}
}

// A branch's own ref lock held during the fast forward is a ref lock, not
// the index: reported with its file and never retried, since git has moved
// the checkout by then.
func TestSyncTrunkWithAHeldRefLock(t *testing.T) {
	f := newSyncFixture(t)
	gittest.Run(t, f.dir, "switch", "-q", "main")
	f.advanceRemote(t, "main", "r.txt")
	lock := filepath.Join(f.dir, ".git", "refs", "heads", "main.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := f.sync(t, app.SyncOptions{NoRestack: true})
	if !errors.Is(err, &stack.Error{Kind: stack.KindPartial}) {
		t.Fatalf("err = %v, want a partial sync", err)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Errorf("took %v; a ref lock must not be waited on", time.Since(start))
	}
	n := res.NotUpdated
	if len(n) != 1 || n[0].Reason != app.NotUpdatedLocked || n[0].LockKind != "ref" {
		t.Fatalf("not updated = %+v", n)
	}
	if real, _ := filepath.EvalSymlinks(filepath.Dir(lock)); filepath.Join(real, "main.lock") != n[0].Lock && lock != n[0].Lock {
		t.Errorf("lock = %q, want %q", n[0].Lock, lock)
	}
	if !strings.Contains(err.Error(), "holds the lock on main") || strings.Contains(err.Error(), "index lock") {
		t.Errorf("message = %q", err.Error())
	}
}
