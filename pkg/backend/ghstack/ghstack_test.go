package ghstack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func TestParseFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "two-stacks.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseFile(b)
	if err != nil {
		t.Fatal(err)
	}
	g := f.toGraph()
	if len(g.Stacks) != 2 || !slices.Equal(g.Trunks, []string{"main"}) {
		t.Fatalf("graph = %+v", g)
	}
	s := g.Stacks[0]
	if s.ID != "S_1" || s.Number != 7 || s.Trunk != "main" || !slices.Equal(s.Names(), []string{"feat/api", "feat/ui"}) {
		t.Errorf("stack 0 = %+v", s)
	}
	api := s.Branches[0]
	if api.PR == nil || api.PR.Number != 12 || api.PR.Merged || api.PR.URL == "" || api.Base != s.Branches[1].Base[:0]+"1111111111111111111111111111111111111111" {
		t.Errorf("feat/api = %+v", api)
	}
	if s.Branches[1].PR != nil {
		t.Errorf("feat/ui should have no PR")
	}
	typo := g.Stacks[1].Branches[0]
	if !typo.Merged() || g.Stacks[1].Number != 0 {
		t.Errorf("fix/typo = %+v", typo)
	}
}

func TestParseRejectsBadFiles(t *testing.T) {
	cases := map[string]string{
		"newer schema": `{"schemaVersion": 2, "stacks": []}`,
		"missing":      `{"stacks": []}`,
		"no trunk":     `{"schemaVersion": 1, "stacks": [{"branches": []}]}`,
		"no name":      `{"schemaVersion": 1, "stacks": [{"trunk": {"branch": "main"}, "branches": [{}]}]}`,
		"not json":     `{`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseFile([]byte(in))
			if err == nil {
				t.Fatal("expected error")
			}
			if name == "newer schema" && !errors.Is(err, &stack.Error{Kind: stack.KindUnsupported}) {
				t.Errorf("newer schema should be KindUnsupported, got %v", err)
			}
		})
	}
}

func TestLoadMissingFileIsEmptyGraph(t *testing.T) {
	gitDir := t.TempDir()
	b := New(exectest.New(), nil)
	g, err := b.Load(context.Background(), git.Repo{GitDir: gitDir})
	if err != nil || len(g.Stacks) != 0 {
		t.Fatalf("Load = %+v, %v", g, err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, FileName), []byte(`{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"a"}]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err = b.Load(context.Background(), git.Repo{GitDir: gitDir})
	if err != nil || len(g.Stacks) != 1 || g.Stacks[0].Top() != "a" {
		t.Fatalf("Load = %+v, %v", g, err)
	}
}

func TestMapError(t *testing.T) {
	cases := []struct {
		code   int
		stderr string
		kind   stack.Kind
	}{
		{2, "current branch is not part of a stack", stack.KindNotInStack},
		{3, "CONFLICT (content): Merge conflict in pkg/a.go\nCONFLICT (add/add): Merge conflict in b.txt\nResolve conflicts", stack.KindConflict},
		{4, "GraphQL: something", stack.KindAPIFailure},
		{5, "can only add branches to the top of the stack", stack.KindInvalidArgs},
		{6, "branch belongs to multiple stacks", stack.KindDisambiguate},
		{7, "rebase in progress", stack.KindRebaseActive},
		{8, "another process is currently editing the stack", stack.KindLocked},
		{9, "Stacked PRs are not enabled for this repository", stack.KindStacksUnavailable},
		{10, "modify recovery", stack.KindModifyRecovery},
		{1, "To get started with GitHub CLI, please run:  gh auth login", stack.KindAuthRequired},
		{1, "boom", stack.KindUnknown},
	}
	for _, tc := range cases {
		res := exec.Result{ExitCode: tc.code, Stderr: []byte(tc.stderr)}
		err := mapError([]string{"rebase"}, &exec.ExitError{Cmd: exec.Cmd{Name: "gh"}, Result: res})
		se, ok := errors.AsType[*stack.Error](err)
		if !ok || se.Kind != tc.kind {
			t.Errorf("code %d: got %v (kind %v), want %v", tc.code, err, se.Kind, tc.kind)
			continue
		}
		if se.Detail != tc.stderr {
			t.Errorf("code %d: detail %q", tc.code, se.Detail)
		}
		if tc.code == 3 && !slices.Equal(se.Files, []string{"pkg/a.go", "b.txt"}) {
			t.Errorf("conflict files = %v", se.Files)
		}
		if tc.kind != stack.KindUnknown && len(se.NextSteps) == 0 && tc.kind != stack.KindConflict && tc.kind != stack.KindAPIFailure && tc.kind != stack.KindInvalidArgs {
			t.Errorf("code %d: no next steps", tc.code)
		}
	}

	err := mapError(nil, &exec.NotFoundError{Name: "gh"})
	if !errors.Is(err, &stack.Error{Kind: stack.KindNotInstalled}) {
		t.Errorf("not found: %v", err)
	}
	if err := mapError(nil, context.Canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation must pass through: %v", err)
	}
}

func TestPreflight(t *testing.T) {
	f := exectest.New()
	f.On("gh", "extension", "list").Reply("gh pr-review\tagynio/gh-pr-review\tv1.6.2\ngh stack\tgithub/gh-stack\tv0.9.0\n")
	if err := New(f, nil).Preflight(context.Background()); err != nil {
		t.Errorf("installed: %v", err)
	}

	f = exectest.New()
	f.On("gh", "extension", "list").Reply("gh pr-review\tagynio/gh-pr-review\tv1.6.2\n")
	err := New(f, nil).Preflight(context.Background())
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindNotInstalled || !slices.ContainsFunc(se.NextSteps, func(s string) bool { return s == "run: gh extension install github/gh-stack" }) {
		t.Errorf("missing extension: %v", err)
	}

	f = exectest.New()
	f.On("gh").NotFound()
	b := New(f, nil)
	err = b.Preflight(context.Background())
	if !errors.Is(err, &stack.Error{Kind: stack.KindNotInstalled}) {
		t.Errorf("missing gh: %v", err)
	}
	_ = b.Preflight(context.Background())
	if n := len(f.CallsTo("gh")); n != 1 {
		t.Errorf("preflight should be cached, ran %d times", n)
	}
}

func TestGhEnvIsNonInteractive(t *testing.T) {
	f := exectest.New()
	f.On("gh", "stack", "view").Reply("")
	b := New(f, nil)
	if err := b.gh(context.Background(), git.Repo{TopLevel: "/repo"}, "view"); err != nil {
		t.Fatal(err)
	}
	call := f.Calls()[0]
	if call.Dir != "/repo" || call.Mode != exec.Capture {
		t.Errorf("call = %+v", call)
	}
}
