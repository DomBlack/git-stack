package cmd

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// fakeGh simulates the gh-stack extension: git commands run for real, gh
// stack add/init create the branch and update the metadata file, recording
// each branch's base the way gh stack does.
func fakeGh(t *testing.T, dir string) *exectest.Fake {
	t.Helper()
	f := exectest.New()
	real := exec.New()
	f.Fallback = func(c exec.Cmd) (exec.Result, error) { return real.Run(context.Background(), c) }
	f.On("gh", "extension", "list").Reply("gh stack\tgithub/gh-stack\tv0.9.0\n")
	f.On("gh", "pr", "list").Reply("[]")
	gitDir := gittest.Run(t, dir, "rev-parse", "--absolute-git-dir")
	metaPath := filepath.Join(gitDir, "gh-stack")

	type branch struct {
		Branch string `json:"branch"`
		Base   string `json:"base,omitzero"`
	}
	type stk struct {
		Trunk    branch   `json:"trunk"`
		Branches []branch `json:"branches"`
	}
	type file struct {
		SchemaVersion int   `json:"schemaVersion"`
		Stacks        []stk `json:"stacks"`
	}
	load := func() file {
		var fl file
		if b, err := os.ReadFile(metaPath); err == nil {
			_ = json.Unmarshal(b, &fl)
		}
		fl.SchemaVersion = 1
		return fl
	}
	save := func(fl file) {
		b, _ := json.Marshal(fl)
		if err := os.WriteFile(metaPath, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun := func(args ...string) error {
		_, err := real.Run(context.Background(), exec.Cmd{Name: "git", Args: args, Dir: dir})
		return err
	}
	f.On("gh", "stack", "init").Do(func(c exec.Cmd) (exec.Result, error) {
		// gh stack init --base <trunk> <branches...>
		trunk, names := c.Args[3], c.Args[4:]
		fl := load()
		s := stk{Trunk: branch{Branch: trunk}}
		prev := trunk
		for _, n := range names {
			if err := gitRun("branch", n, prev); err != nil {
				return exec.Result{}, err
			}
			s.Branches = append(s.Branches, branch{Branch: n, Base: gittest.Run(t, dir, "rev-parse", prev)})
			prev = n
		}
		fl.Stacks = append(fl.Stacks, s)
		save(fl)
		return exec.Result{}, gitRun("switch", prev)
	})
	f.On("gh", "stack", "add").Do(func(c exec.Cmd) (exec.Result, error) {
		name := c.Args[2]
		cur := gittest.Run(t, dir, "branch", "--show-current")
		fl := load()
		for i := range fl.Stacks {
			s := &fl.Stacks[i]
			if n := len(s.Branches); n > 0 && s.Branches[n-1].Branch == cur {
				if err := gitRun("branch", name, cur); err != nil {
					return exec.Result{}, err
				}
				s.Branches = append(s.Branches, branch{Branch: name, Base: gittest.Run(t, dir, "rev-parse", cur)})
				save(fl)
				return exec.Result{}, gitRun("switch", name)
			}
		}
		res := exec.Result{ExitCode: 5, Stderr: []byte("✗ can only add branches to the top of the stack; run `gh stack top` then `gh stack add`")}
		return res, &exec.ExitError{Cmd: c, Result: res}
	})
	return f
}

func runWith(t *testing.T, f *exectest.Fake, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &cli{
		streams:   Streams{In: strings.NewReader(""), Out: &out, Err: &errOut},
		newRunner: func(...exec.Option) exec.Runner { return f },
	}
	root := newRootCmd(c)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestCreateModifyRestackCommands(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)
	cur := func() string { return gittest.Run(t, dir, "branch", "--show-current") }
	ghCalls := func() []string {
		var out []string
		for _, c := range f.CallsTo("gh") {
			out = append(out, strings.Join(c.Args, " "))
		}
		return out
	}

	// create from trunk with a message: new stack, derived name, commit.
	gittest.WriteFile(t, dir, "a.txt", "a")
	out, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "-m", "Add feature A")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(out, "ok: Created add-feature-a on main  ") || !strings.HasSuffix(out, " Add feature A\n") {
		t.Errorf("create output = %q", out)
	}
	if cur() != "add-feature-a" || !strings.Contains(strings.Join(ghCalls(), "|"), "stack init --base main add-feature-a") {
		t.Errorf("state: on %s, gh calls %v", cur(), ghCalls())
	}

	// create on top with explicit name and nothing staged: empty branch via add.
	out, _, err = runWith(t, f, "--cwd", dir, "c", "feat/b")
	if err != nil || !strings.Contains(out, "Created empty branch feat/b on add-feature-a") || cur() != "feat/b" {
		t.Errorf("empty create: %q %v (on %s)", out, err, cur())
	}

	// modify on the empty branch creates a commit (never amends the parent).
	gittest.WriteFile(t, dir, "b.txt", "b")
	out, errOut, err := runWith(t, f, "--cwd", dir, "modify", "-a", "-m", "Add B")
	if err != nil || !strings.Contains(errOut, "had no commits of its own") || !strings.Contains(out, "Committed to feat/b") {
		t.Errorf("modify empty: %q %v", out, err)
	}
	if subj := gittest.Run(t, dir, "log", "-1", "--format=%s", "add-feature-a"); subj != "Add feature A" {
		t.Errorf("parent rewritten: %s", subj)
	}

	// modify on a lower branch amends and restacks upstack.
	gittest.Run(t, dir, "switch", "-q", "add-feature-a")
	gittest.WriteFile(t, dir, "a.txt", "a2")
	f.Reset()
	out, _, err = runWith(t, f, "--cwd", dir, "m", "-u")
	if err != nil || !strings.HasPrefix(out, "ok: Amended add-feature-a  ") || !strings.Contains(out, "Restacked 1 branch above add-feature-a") {
		t.Errorf("modify amend: %q %v", out, err)
	}
	// gittest.Run fails the test when git exits non-zero.
	gittest.Run(t, dir, "merge-base", "--is-ancestor", "add-feature-a", "feat/b")

	// create mid-stack is refused with the gh-stack limitation explained.
	_, _, err = runWith(t, f, "--cwd", dir, "create", "mid")
	if err == nil || !strings.Contains(err.Error(), "not the top of its stack") {
		t.Errorf("mid-stack create: %v", err)
	}

	// restack after an amend lower down reports what moved and what didn't.
	gittest.Run(t, dir, "switch", "-q", "add-feature-a")
	gittest.WriteFile(t, dir, "a.txt", "a3")
	gittest.Run(t, dir, "commit", "-q", "-a", "--amend", "--no-edit")
	gittest.Run(t, dir, "switch", "-q", "feat/b")
	out, errOut, err = runWith(t, f, "--cwd", dir, "restack")
	if err != nil || out != "  add-feature-a already in place\nok: Restacked feat/b\n" || !strings.Contains(errOut, "Restacking add-feature-a, feat/b...") {
		t.Errorf("restack: %q %q %v", out, errOut, err)
	}
	out, _, err = runWith(t, f, "--cwd", dir, "rs")
	if err != nil || out != "ok: Nothing to restack; add-feature-a, feat/b are already in place\n" {
		t.Errorf("no-op restack: %q %v", out, err)
	}
	out, _, err = runWith(t, f, "--cwd", dir, "restack", "--only", "--branch", "add-feature-a")
	if err != nil || !strings.HasPrefix(out, "ok: Nothing to restack") {
		t.Errorf("--only --branch: %q %v", out, err)
	}
	if _, _, err := runWith(t, f, "--cwd", dir, "restack", "-u", "-d"); err == nil {
		t.Error("mutually exclusive scope flags")
	}
	_, _, err = runWith(t, f, "--cwd", dir, "rs", "--continue")
	if err == nil || !strings.Contains(err.Error(), "nothing to continue") {
		t.Errorf("continue with nothing pending: %v", err)
	}

	// Trunk moved: the bottom branch follows it.
	gittest.Run(t, dir, "switch", "-q", "main")
	gittest.Commit(t, dir, "m.txt", "m", "trunk moves")
	gittest.Run(t, dir, "switch", "-q", "feat/b")
	out, _, err = runWith(t, f, "--cwd", dir, "restack")
	if err != nil || out != "ok: Restacked add-feature-a, feat/b\n" {
		t.Errorf("after trunk moved: %q %v", out, err)
	}
	gittest.Run(t, dir, "merge-base", "--is-ancestor", "main", "add-feature-a")

	// A conflict stops with the files and our commands; abort restores.
	gittest.Run(t, dir, "switch", "-q", "add-feature-a")
	gittest.Commit(t, dir, "b.txt", "a's b", "a edits b.txt")
	gittest.Run(t, dir, "switch", "-q", "feat/b")
	_, _, err = runWith(t, f, "--cwd", dir, "restack")
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindConflict || !strings.Contains(se.Error(), "feat/b conflicts when rebased onto add-feature-a") ||
		!slices.Contains(se.NextSteps, "git add b.txt") || !slices.Contains(se.NextSteps, "git stack continue") {
		t.Errorf("conflict: %v", err)
	}
	// The top level commands do the same as the flags.
	gittest.WriteFile(t, dir, "b.txt", "resolved")
	out, _, err = runWith(t, f, "--cwd", dir, "continue", "--all")
	if err != nil || out != "ok: Restacked feat/b\n" {
		t.Errorf("continue: %q %v", out, err)
	}
	gittest.Run(t, dir, "merge-base", "--is-ancestor", "add-feature-a", "feat/b")
	_, _, err = runWith(t, f, "--cwd", dir, "abort")
	if err == nil || !strings.Contains(err.Error(), "nothing to abort") {
		t.Errorf("abort with nothing pending: %v", err)
	}
	_, _, err = runWith(t, f, "--cwd", dir, "cont")
	if err == nil || !strings.Contains(err.Error(), "nothing to continue") {
		t.Errorf("cont alias: %v", err)
	}
}

func TestCreateWithAIUsesClaude(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	f := fakeGh(t, dir)
	f.On("claude").Reply(`{"type":"result","is_error":false,"structured_output":{"branch":"add-adder","message":"feat: add adder\n\nBody."}}`)
	gittest.Run(t, dir, "config", "stack.branchPrefix", "dom/")
	gittest.WriteFile(t, dir, "add.go", "package x\n")

	out, _, err := runWith(t, f, "--cwd", dir, "create", "-a", "--ai")
	if err != nil {
		t.Fatalf("create --ai: %v", err)
	}
	if !strings.HasPrefix(out, "ok: Created dom/add-adder on main  ") || !strings.HasSuffix(out, " feat: add adder\n") {
		t.Errorf("output = %q", out)
	}
	calls := f.CallsTo("claude")
	if len(calls) != 1 || !strings.Contains(calls[0].Stdin, "+package x") {
		t.Errorf("claude call = %+v", calls)
	}
	if _, _, err := runWith(t, f, "--cwd", dir, "create", "--ai", "--no-ai"); err == nil || !strings.Contains(err.Error(), "no branch name") {
		t.Errorf("--no-ai wins: %v", err)
	}
}
