package app_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

// closeFixture is submitFixture with an origin remote that has branch c
// (a pull request a fold or pop left behind, #12) queued to be closed.
func closeFixture(t *testing.T) (*app.App, *recordingForge, git.Repo, string, string) {
	t.Helper()
	deps, _, fg, repo, dir := submitFixture(t)
	origin := t.TempDir()
	gittest.Run(t, origin, "init", "-q", "--bare")
	gittest.Run(t, dir, "remote", "add", "origin", origin)
	gittest.Run(t, dir, "branch", "c", "b")
	gittest.Run(t, dir, "push", "-q", "origin", "c")
	tip := gittest.Run(t, dir, "rev-parse", "c")
	gittest.Run(t, dir, "branch", "-D", "c")
	fg.prs = append(fg.prs, forge.PullRequest{Number: 12, Head: "c", Base: "b", State: forge.StateOpen, URL: "u/c"})
	path := filepath.Join(repo.CommonDir, "git-stack", "remote-stacks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	queue := `{"dissolve":[],"close":[{"number":12,"branch":"c","comment":"Folded into #7 (a)","tip":"` + tip + `"}]}`
	if err := os.WriteFile(path, []byte(queue), 0o644); err != nil {
		t.Fatal(err)
	}
	return app.New(deps), fg, repo, path, origin
}

func remoteHas(t *testing.T, origin, branch string) bool {
	t.Helper()
	return strings.Contains(gittest.Run(t, origin, "branch", "--list", branch), branch)
}

func TestSubmitClosesQueuedPullRequestsAndDeletesTheirBranch(t *testing.T) {
	a, fg, repo, path, origin := closeFixture(t)
	ctx := context.Background()

	if _, err := a.Submit(ctx, repo, app.SubmitOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if len(fg.closed) != 0 || !remoteHas(t, origin, "c") {
		t.Fatal("a dry run must not close or delete anything")
	}

	res, err := a.Submit(ctx, repo, app.SubmitOptions{Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fg.closed) != 1 || fg.closed[0].number != 12 || fg.closed[0].comment != "Folded into #7 (a)" {
		t.Errorf("closed = %+v", fg.closed)
	}
	if remoteHas(t, origin, "c") {
		t.Error("the remote branch should be deleted")
	}
	if len(res.Closed) != 1 || res.Closed[0].Number != 12 || res.Closed[0].Branch != "c" || !res.Closed[0].BranchDeleted {
		t.Errorf("result closed = %+v", res.Closed)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("nothing should be left queued: %v", err)
	}
}

// Deleting a branch on GitHub closes every pull request based on it, so
// the branch is kept while one still is.
func TestSubmitKeepsTheBranchOfAClosedPullRequestSomethingIsBasedOn(t *testing.T) {
	a, fg, repo, path, origin := closeFixture(t)
	fg.prs = append(fg.prs, forge.PullRequest{Number: 13, Head: "d", Base: "c", State: forge.StateOpen, URL: "u/d"})
	res, err := a.Submit(context.Background(), repo, app.SubmitOptions{Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fg.closed) != 1 || !remoteHas(t, origin, "c") {
		t.Errorf("closed = %+v; the branch should stay", fg.closed)
	}
	if n := strings.Join(res.Notices, "\n"); !strings.Contains(n, "kept c on GitHub because #13 is still based on it") {
		t.Errorf("notices = %v", res.Notices)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"closed":true`) {
		t.Errorf("the delete should stay queued: %q %v", data, err)
	}

	// Next time, with #13 retargeted, only the delete is left to do.
	for i := range fg.prs {
		if fg.prs[i].Number == 13 {
			fg.prs[i].Base = "b"
		}
	}
	if _, err := a.Submit(context.Background(), repo, app.SubmitOptions{Publish: true}); err != nil {
		t.Fatal(err)
	}
	if len(fg.closed) != 1 || remoteHas(t, origin, "c") {
		t.Errorf("closed again (%+v) or branch still there", fg.closed)
	}
}

func TestSubmitKeepsAQueuedCloseThatFailed(t *testing.T) {
	a, fg, repo, path, origin := closeFixture(t)
	fg.closeErr = errors.New("boom")
	res, err := a.Submit(context.Background(), repo, app.SubmitOptions{Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Join(res.Notices, "\n"); !strings.Contains(n, "could not close #12 (c)") {
		t.Errorf("notices = %v", res.Notices)
	}
	if !remoteHas(t, origin, "c") {
		t.Error("the branch must stay while its pull request is open")
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), `"number":12`) || strings.Contains(string(data), `"closed":true`) {
		t.Errorf("the close should stay queued: %q %v", data, err)
	}
}
