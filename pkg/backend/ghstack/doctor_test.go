//go:build unix

package ghstack

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
)

func runAll(ctx context.Context, checks []doctor.Check, out map[string]doctor.Finding) {
	for _, c := range checks {
		f := c.Run(ctx)
		out[c.ID] = f
		if f.Status != doctor.Fail {
			runAll(ctx, c.Sub, out)
		}
	}
}

func TestDoctorChecksGhAndGhStack(t *testing.T) {
	ctx := context.Background()
	f := exectest.New()
	f.On("gh", "--version").Reply("gh version 2.62.0 (2026-10-01)\nhttps://github.com/cli/cli/releases/tag/v2.62.0\n")
	f.On("gh", "auth", "status").Reply("github.com\n  ✓ Logged in to github.com account dom (keyring)\n")
	f.On("gh", "extension", "list").Reply("gh stack\tgithub/gh-stack\tv0.2.1\n")
	f.On("gh", "extension", "upgrade", "stack").Do(func(exec.Cmd) (exec.Result, error) {
		f.On("gh", "extension", "list").Reply("gh stack\tgithub/gh-stack\tv0.3.0\n")
		return exec.Result{}, nil
	})
	b := New(f, nil)
	got := map[string]doctor.Finding{}
	checks := b.Checks(nil)
	runAll(ctx, checks, got)
	if got["gh"].Summary != "gh version 2.62.0 (2026-10-01)" || got["gh.auth"].Summary != "Logged in to github.com account dom (keyring)" {
		t.Errorf("gh = %+v, auth = %+v", got["gh"], got["gh.auth"])
	}
	if v := got["gh-stack.version"]; v.Status != doctor.Fail || !strings.Contains(v.Summary, "older than v0.3.0") {
		t.Errorf("version = %+v", v)
	}
	if _, ok := got["stack-file"]; ok {
		t.Error("the stack file needs a repository")
	}
	if err := checks[0].Sub[1].Sub[0].Fix(ctx); err != nil {
		t.Fatal(err)
	}
	if v := checks[0].Sub[1].Sub[0].Run(ctx); v.Status != doctor.OK {
		t.Errorf("after the upgrade = %+v", v)
	}

	f = exectest.New()
	f.On("gh").NotFound()
	got = map[string]doctor.Finding{}
	runAll(ctx, New(f, nil).Checks(nil), got)
	if g := got["gh"]; g.Status != doctor.Fail || len(g.Steps) == 0 {
		t.Errorf("missing gh = %+v", g)
	}
	if _, ok := got["gh.auth"]; ok {
		t.Error("nothing under a missing gh should run")
	}
}

func TestDoctorMovesWorktreeFiles(t *testing.T) {
	b, repo, mainPath, wtPath := updateFixture(t)
	ctx := context.Background()
	got := map[string]doctor.Finding{}
	checks := b.Checks(&repo)
	runAll(ctx, checks[1:], got)
	if l := got["stack-file.shared"]; l.Status != doctor.Fail || len(l.Details) != 1 || l.Details[0] != wtPath {
		t.Fatalf("layout = %+v", l)
	}
	if err := checks[1].Sub[0].Fix(ctx); err != nil {
		t.Fatal(err)
	}
	if l := checks[1].Sub[0].Run(ctx); l.Status != doctor.OK {
		t.Errorf("after the fix = %+v", l)
	}
	if data, _ := os.ReadFile(mainPath); !strings.Contains(string(data), `"branch": "w"`) {
		t.Errorf("catalog lacks w:\n%s", data)
	}
}
