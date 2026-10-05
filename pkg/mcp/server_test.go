package mcp

import (
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/stack/stacktest"
)

type fakeForge struct {
	prs     []forge.PullRequest
	updates map[int]forge.UpdatePR
}

func (f *fakeForge) ListPRs(context.Context, git.Repo) ([]forge.PullRequest, error) {
	return slices.Clone(f.prs), nil
}
func (f *fakeForge) CreatePR(context.Context, git.Repo, forge.CreatePR) (forge.PullRequest, error) {
	return forge.PullRequest{}, nil
}
func (f *fakeForge) UpdatePR(_ context.Context, _ git.Repo, n int, in forge.UpdatePR) error {
	if f.updates == nil {
		f.updates = map[int]forge.UpdatePR{}
	}
	f.updates[n] = in
	return nil
}

type harness struct {
	t       *testing.T
	dir     string
	repo    git.Repo
	backend *stacktest.Backend
	forge   *fakeForge
	session *mcp.ClientSession
	stdout  *os.File
	stdoutR *os.File
	// noRepoPath stops call from injecting repo_path.
	noRepoPath bool
	log        *strings.Builder
}

// newHarness builds main -> a -> b (HEAD on b) with an in-memory backend
// and connects a client over in-memory transports. os.Stdout is replaced
// by a pipe for the test's lifetime so any pollution is detectable.
func newHarness(t *testing.T, roots ...string) *harness {
	t.Helper()
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	gittest.Commit(t, dir, "a.txt", "a", "feat: a")
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "b.txt", "b", "feat: b")
	gittest.InitRemote(t, dir)

	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	backend := stacktest.New(g, stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "b"}}}}))
	ff := &fakeForge{prs: []forge.PullRequest{{Number: 7, Head: "a", Base: "main", State: forge.StateOpen, URL: "u/7", Title: "A"}}}
	backend.SubmitFn = func(o stack.SubmitOptions) error {
		for _, name := range []string{"a", "b"} {
			if _, ok := app.PRsFor(ff.prs)[name]; !ok {
				st := forge.StateDraft
				if o.Publish {
					st = forge.StateOpen
				}
				ff.prs = append(ff.prs, forge.PullRequest{Number: 10 + len(ff.prs), Head: name, State: st, URL: "u/" + name})
			}
		}
		return nil
	}

	h := &harness{t: t, dir: dir, repo: repo, backend: backend, forge: ff}
	h.stdoutR, h.stdout, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = h.stdout
	t.Cleanup(func() { os.Stdout = old })

	h.log = &strings.Builder{}
	srv := New(Options{
		Version: "test",
		Cwd:     t.TempDir(), // deliberately not the repo: tests must pass repo_path or roots
		Git:     g,
		Log:     slog.New(slog.NewTextHandler(h.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		NewApp: func(context.Context, git.Repo) (*app.App, error) {
			cfg := config.Defaults()
			cfg.CacheTTL = 0
			return app.New(app.Deps{
				Git: g, Meta: backend, Tracker: backend, Restack: backend, Submit: backend,
				Forge: ff, Cache: cache.New(repo), Config: cfg,
			}), nil
		},
	})
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.MCP().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	for _, r := range roots {
		if r == "USE_REPO" {
			r = dir
		}
		client.AddRoots(&mcp.Root{URI: "file://" + r}) //nolint:staticcheck // roots stay supported during the deprecation window
	}
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	h.session = cs
	return h
}

// call invokes a tool and decodes its structured content into out.
func (h *harness) call(name string, args map[string]any, out any) (*mcp.CallToolResult, string) {
	h.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	if _, ok := args["repo_path"]; !ok && !h.noRepoPath {
		args["repo_path"] = h.dir
	}
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: protocol error: %v", name, err)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if res.IsError && out != nil {
		h.t.Fatalf("%s: unexpected tool error: %s", name, text)
	}
	if !res.IsError && out != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			h.t.Fatal(err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			h.t.Fatalf("%s: decode structured content %s: %v", name, b, err)
		}
	}
	return res, text
}

func (h *harness) toolErr(name string, args map[string]any) toolError {
	h.t.Helper()
	res, text := h.call(name, args, nil)
	if !res.IsError {
		h.t.Fatalf("%s: expected an error, got %+v", name, res.StructuredContent)
	}
	var te toolError
	if err := json.Unmarshal([]byte(text), &te); err != nil {
		h.t.Fatalf("%s: error is not JSON: %q", name, text)
	}
	return te
}

func (h *harness) assertNoStdout() {
	h.t.Helper()
	_ = h.stdout.Close()
	data, _ := io.ReadAll(h.stdoutR)
	if len(data) != 0 {
		h.t.Fatalf("tool paths wrote to os.Stdout: %q", data)
	}
}

func (h *harness) current() string { return gittest.Run(h.t, h.dir, "branch", "--show-current") }

func TestToolsAreListedWithAnnotationsAndSchemas(t *testing.T) {
	h := newHarness(t)
	defer h.assertNoStdout()
	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	for _, name := range []string{"stack_view", "stack_create", "stack_modify", "stack_restack", "stack_navigate", "stack_submit", "stack_sync"} {
		if got[name] == nil {
			t.Errorf("missing tool %s", name)
		}
	}
	if !got["stack_view"].Annotations.ReadOnlyHint || got["stack_submit"].Annotations.DestructiveHint == nil || !*got["stack_submit"].Annotations.DestructiveHint ||
		got["stack_sync"].Annotations.OpenWorldHint == nil || !*got["stack_sync"].Annotations.OpenWorldHint {
		t.Error("annotations wrong")
	}
	schema, _ := json.Marshal(got["stack_create"].InputSchema)
	if !strings.Contains(string(schema), "repo_path") || !strings.Contains(string(schema), "defaults to the client") || !strings.Contains(string(schema), `"required":["message"]`) {
		t.Errorf("stack_create schema = %s", schema)
	}
	init := h.session.InitializeResult()
	if init == nil || !strings.HasPrefix(init.Instructions, "git-stack manages stacked branches") {
		t.Error("instructions missing")
	}
}

func TestViewCreateModifyNavigate(t *testing.T) {
	h := newHarness(t)
	defer h.assertNoStdout()

	var v viewOutput
	h.call("stack_view", map[string]any{"include_untracked": true}, &v)
	if v.CurrentBranch != "b" || !slices.Equal(v.Trunks, []string{"main"}) || len(v.Stacks) != 1 {
		t.Fatalf("view = %+v", v)
	}
	bs := v.Stacks[0].Branches
	if bs[0].Name != "a" || bs[0].Parent != "main" || bs[0].PR == nil || bs[0].PR.Number != 7 || bs[0].PR.State != "open" ||
		bs[1].Name != "b" || bs[1].Parent != "a" || !bs[1].IsCurrent || bs[1].PR != nil || bs[1].NeedsRestack {
		t.Errorf("branches = %+v", bs)
	}

	// create requires a message: the SDK rejects it against the schema.
	if res, text := h.call("stack_create", map[string]any{}, nil); !res.IsError || !strings.Contains(text, "message") {
		t.Errorf("create without message: %v %q", res.IsError, text)
	}
	if te := h.toolErr("stack_create", map[string]any{"message": "  "}); te.Code != "invalid_args" {
		t.Errorf("blank message: %+v", te)
	}
	gittest.WriteFile(t, h.dir, "c.txt", "c")
	var cr app.CreateResult
	h.call("stack_create", map[string]any{"message": "feat: add c\n\nbody", "staging": "all"}, &cr)
	if cr.Branch != "feat-add-c" || cr.Parent != "b" || cr.Commit == nil || cr.Commit.Subject != "feat: add c" || h.current() != "feat-add-c" {
		t.Errorf("create = %+v (on %s)", cr, h.current())
	}

	// modify with a conflict yields structured next steps in tool terms.
	h.backend.RestackErr = &stack.Error{Kind: stack.KindConflict, Msg: "rebase stopped", Files: []string{"x.go"}}
	var nav navigateOutput
	h.call("stack_navigate", map[string]any{"direction": "down"}, &nav)
	if nav.To != "b" || h.current() != "b" {
		t.Errorf("down = %+v", nav)
	}
	gittest.WriteFile(t, h.dir, "b.txt", "b2")
	te := h.toolErr("stack_modify", map[string]any{"staging": "update"})
	if te.Code != "conflict" || !slices.Equal(te.Files, []string{"x.go"}) || !slices.ContainsFunc(te.NextSteps, func(s string) bool { return s == "call stack_modify with continue: true" }) {
		t.Errorf("conflict error = %+v", te)
	}
	h.backend.RestackErr = nil
	var mr app.ModifyResult
	h.call("stack_modify", map[string]any{"continue": true}, &mr)
	if h.backend.Continued != 1 {
		t.Error("continue not forwarded")
	}
	gittest.WriteFile(t, h.dir, "b2.txt", "b2")
	h.call("stack_modify", map[string]any{"mode": "commit", "message": "second", "staging": "all"}, &mr)
	if mr.Amended || mr.Commit.Subject != "second" || !slices.Equal(mr.Restacked, []string{"feat-add-c"}) {
		t.Errorf("modify commit = %+v", mr)
	}
	if te := h.toolErr("stack_modify", map[string]any{"mode": "squash"}); te.Code != "invalid_args" {
		t.Errorf("bad mode: %+v", te)
	}

	var rr app.RestackResult
	h.call("stack_restack", map[string]any{"scope": "upstack"}, &rr)
	if !slices.Equal(rr.Branches, []string{"b", "feat-add-c"}) {
		t.Errorf("restack = %+v", rr)
	}

	h.call("stack_navigate", map[string]any{"direction": "bottom"}, &nav)
	h.call("stack_navigate", map[string]any{"direction": "down"}, &nav)
	if nav.To != "main" || h.current() != "main" {
		t.Errorf("down from bottom = %+v", nav)
	}
	h.call("stack_navigate", map[string]any{"direction": "down"}, &nav)
	if !nav.Clamped || nav.Message == "" {
		t.Errorf("clamp = %+v", nav)
	}
	h.call("stack_navigate", map[string]any{"branch": "feat-add-c"}, &nav)
	if h.current() != "feat-add-c" {
		t.Errorf("checkout by name: on %s", h.current())
	}
	if te := h.toolErr("stack_navigate", map[string]any{"branch": "nope"}); te.Code != "invalid_args" {
		t.Errorf("bad branch: %+v", te)
	}
	if te := h.toolErr("stack_navigate", map[string]any{}); te.Code != "invalid_args" {
		t.Errorf("no direction: %+v", te)
	}
}

func TestSubmitAndSync(t *testing.T) {
	h := newHarness(t)
	defer h.assertNoStdout()

	var sr app.SubmitResult
	h.call("stack_submit", map[string]any{"dry_run": true}, &sr)
	if !sr.DryRun || len(h.backend.Submits) != 0 || len(sr.PullRequests) != 2 || !sr.PullRequests[1].WouldCreate {
		t.Errorf("dry run = %+v", sr)
	}

	h.call("stack_submit", map[string]any{"pull_requests": map[string]any{"b": map[string]any{"title": "B!", "body": "because"}}}, &sr)
	if len(h.backend.Submits) != 1 || h.backend.Submits[0].Interactive || h.backend.Submits[0].Publish {
		t.Errorf("submit opts = %+v", h.backend.Submits)
	}
	b := sr.PullRequests[1]
	if !b.Created || !b.TextUpdated || b.State != forge.StateDraft || b.Number == 0 {
		t.Errorf("b = %+v", b)
	}
	if up := h.forge.updates[b.Number]; up.Title == nil || *up.Title != "B!" {
		t.Errorf("text not applied: %+v", h.forge.updates)
	}
	if len(sr.Notices) != 0 {
		t.Errorf("no notices expected when texts were supplied: %v", sr.Notices)
	}

	var sy app.SyncResult
	h.call("stack_sync", map[string]any{"prune": true, "no_restack": true}, &sy)
	if sy.Remote != "origin" || len(sy.Trunks) != 1 || sy.Trunks[0].Status != app.TrunkUpToDate {
		t.Errorf("sync = %+v", sy)
	}
}

func TestRepoResolution(t *testing.T) {
	h := newHarness(t)
	defer h.assertNoStdout()
	te := h.toolErr("stack_view", map[string]any{"repo_path": t.TempDir()})
	if te.Code != "not_repo" || len(te.NextSteps) == 0 {
		t.Errorf("not a repo: %+v", te)
	}
	// Without repo_path and without roots the cwd (a temp dir) is used.
	h.noRepoPath = true
	if te := h.toolErr("stack_view", map[string]any{}); te.Code != "not_repo" {
		t.Errorf("cwd fallback: %+v", te)
	}
}

// TestRootsSkippedOnNewProtocol: the SDK's default protocol (2026-07-28)
// has no roots, so a call without repo_path must fall back to the working
// directory immediately rather than waiting for a roots lookup.
func TestRootsSkippedOnNewProtocol(t *testing.T) {
	h := newHarness(t, "USE_REPO")
	defer h.assertNoStdout()
	h.noRepoPath = true
	start := time.Now()
	te := h.toolErr("stack_view", map[string]any{"fresh": false})
	if te.Code != "not_repo" {
		t.Errorf("cwd fallback: %+v", te)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("call took %s; the roots timeout must not apply on the new protocol", d)
	}
}

// TestRootEntry covers the race-free hand-off between the initialised hook
// and the first tool call for old-protocol clients.
func TestRootEntry(t *testing.T) {
	s := New(Options{Git: git.New(exec.New()), NewApp: func(context.Context, git.Repo) (*app.App, error) { return nil, nil }})
	var sess *mcp.ServerSession
	e1 := s.entryFor(sess)
	e2 := s.entryFor(sess)
	if e1 != e2 {
		t.Fatal("entryFor must return the same entry for a session")
	}
	select {
	case <-e1.ready:
		t.Fatal("entry ready too early")
	default:
	}
	e1.done("/repo")
	e1.done("/other") // second completion is ignored
	<-e1.ready
	if e1.path != "/repo" {
		t.Errorf("path = %q", e1.path)
	}
}

func TestRootPath(t *testing.T) {
	if rootPath("file:///tmp/x") != "/tmp/x" || rootPath("https://x") != "" || rootPath("/plain") != "/plain" {
		t.Error("rootPath")
	}
}

func TestNoTransitiveUIImport(t *testing.T) {
	res, err := exec.New().Run(context.Background(), exec.Cmd{Name: "go", Args: []string{"list", "-deps", "."}, Dir: mustAbs(t, ".")})
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Split(res.Out(), "\n") {
		if strings.HasSuffix(dep, "/pkg/ui") || strings.HasPrefix(dep, "charm.land/") {
			t.Errorf("pkg/mcp must not depend on TUI code, found %s", dep)
		}
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
