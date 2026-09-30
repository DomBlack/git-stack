package github

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

const listOutput = `[
 {"number":12,"url":"https://github.com/o/r/pull/12","title":"API","state":"OPEN","isDraft":false,"isCrossRepository":false,"headRefName":"feat/api","baseRefName":"main","updatedAt":"2026-09-01T10:00:00Z"},
 {"number":13,"url":"https://github.com/o/r/pull/13","title":"UI","state":"OPEN","isDraft":true,"isCrossRepository":false,"headRefName":"feat/ui","baseRefName":"feat/api","updatedAt":"2026-09-02T10:00:00Z"},
 {"number":3,"url":"https://github.com/o/r/pull/3","title":"typo","state":"MERGED","isDraft":false,"isCrossRepository":false,"headRefName":"fix/typo","baseRefName":"main","updatedAt":"2026-08-01T10:00:00Z"},
 {"number":4,"url":"https://github.com/o/r/pull/4","title":"old","state":"CLOSED","isDraft":false,"isCrossRepository":false,"headRefName":"old","baseRefName":"main","updatedAt":"2026-07-01T10:00:00Z"},
 {"number":5,"url":"https://github.com/o/r/pull/5","title":"fork","state":"OPEN","isDraft":false,"isCrossRepository":true,"headRefName":"feat/api","baseRefName":"main","updatedAt":"2026-07-01T10:00:00Z"}
]`

func TestListPRs(t *testing.T) {
	f := exectest.New()
	f.On("gh", "pr", "list").Reply(listOutput)
	prs, err := New(f).ListPRs(context.Background(), git.Repo{TopLevel: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 4 {
		t.Fatalf("got %d PRs (forks must be skipped): %+v", len(prs), prs)
	}
	states := map[int]forge.State{}
	for _, p := range prs {
		states[p.Number] = p.State
	}
	if states[12] != forge.StateOpen || states[13] != forge.StateDraft || states[3] != forge.StateMerged || states[4] != forge.StateClosed {
		t.Errorf("states = %v", states)
	}
	if prs[1].Head != "feat/ui" || prs[1].Base != "feat/api" || prs[1].UpdatedAt.IsZero() {
		t.Errorf("pr 13 = %+v", prs[1])
	}
	call := f.Calls()[0]
	if call.Dir != "/repo" || !slices.Contains(call.Args, "--state") || !slices.Contains(call.Args, "all") {
		t.Errorf("call = %+v", call)
	}
}

func TestCreateAndUpdate(t *testing.T) {
	f := exectest.New()
	f.On("gh", "pr", "create").Reply("https://github.com/o/r/pull/14\n")
	f.On("gh", "pr", "view", "feat/x").Reply(`{"number":14,"url":"https://github.com/o/r/pull/14","title":"T","state":"OPEN","isDraft":true,"headRefName":"feat/x","baseRefName":"main"}`)
	f.On("gh", "pr", "edit").Reply("")
	f.On("gh", "pr", "ready").Reply("")
	fg := New(f)
	ctx := context.Background()
	repo := git.Repo{TopLevel: "/repo"}

	pr, err := fg.CreatePR(ctx, repo, forge.CreatePR{Head: "feat/x", Base: "main", Title: "T", Body: "B", Draft: true})
	if err != nil || pr.Number != 14 || pr.State != forge.StateDraft {
		t.Fatalf("CreatePR = %+v, %v", pr, err)
	}
	create := f.CallsTo("gh")[0]
	if !slices.Contains(create.Args, "--draft") || create.Stdin != "B\n" || !slices.Contains(create.Args, "--base") {
		t.Errorf("create call = %+v", create)
	}

	title, body, ready := "New", "Body", true
	if err := fg.UpdatePR(ctx, repo, 14, forge.UpdatePR{Title: &title, Body: &body, Ready: &ready}); err != nil {
		t.Fatal(err)
	}
	calls := f.CallsTo("gh")
	edit, rdy := calls[len(calls)-2], calls[len(calls)-1]
	if edit.Args[1] != "edit" || edit.Args[2] != "14" || !slices.Contains(edit.Args, "--title") || edit.Stdin != "Body\n" {
		t.Errorf("edit call = %+v", edit)
	}
	if strings.Join(rdy.Args, " ") != "pr ready 14" {
		t.Errorf("ready call = %+v", rdy)
	}

	f.Reset()
	if err := fg.UpdatePR(ctx, repo, 14, forge.UpdatePR{}); err != nil || len(f.Calls()) != 0 {
		t.Errorf("empty update should not call gh: %v %v", err, f.Calls())
	}
}

func TestErrors(t *testing.T) {
	f := exectest.New()
	f.On("gh").Fail(4, "To get started with GitHub CLI, please run:  gh auth login")
	_, err := New(f).ListPRs(context.Background(), git.Repo{})
	if !errors.Is(err, &stack.Error{Kind: stack.KindAuthRequired}) {
		t.Errorf("auth: %v", err)
	}
	f = exectest.New()
	f.On("gh").Fail(1, "GraphQL: Something went wrong")
	_, err = New(f).ListPRs(context.Background(), git.Repo{})
	se, ok := errors.AsType[*stack.Error](err)
	if !ok || se.Kind != stack.KindAPIFailure || se.Detail == "" {
		t.Errorf("api: %v", err)
	}
	f = exectest.New()
	f.On("gh").NotFound()
	if _, err := New(f).ListPRs(context.Background(), git.Repo{}); !errors.Is(err, &stack.Error{Kind: stack.KindNotInstalled}) {
		t.Errorf("missing gh: %v", err)
	}
	f = exectest.New()
	f.On("gh").Reply("not json")
	if _, err := New(f).ListPRs(context.Background(), git.Repo{}); err == nil {
		t.Error("bad json should fail")
	}
	_ = exec.Capture
}
