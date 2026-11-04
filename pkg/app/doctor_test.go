package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// fakeDoctor is a tool that is missing until its fix installs it, with a
// version check under it that only runs once it is there.
type fakeDoctor struct {
	installed bool
	fixErr    error
	fixes     int
}

func (f *fakeDoctor) Checks(*git.Repo) []doctor.Check {
	return []doctor.Check{{
		ID: "tool", Title: "tool",
		Run: func(context.Context) doctor.Finding {
			if !f.installed {
				return doctor.Finding{Status: doctor.Fail, Summary: "missing"}
			}
			return doctor.Finding{Status: doctor.OK, Summary: "there"}
		},
		Fix: func(context.Context) error {
			f.fixes++
			if f.fixErr != nil {
				return f.fixErr
			}
			f.installed = true
			return nil
		},
		FixLabel: "install tool",
		Sub: []doctor.Check{{
			ID: "tool.version", Title: "tool version",
			Run: func(context.Context) doctor.Finding { return doctor.Finding{Status: doctor.Warn, Summary: "old"} },
		}},
	}}
}

func find(results []doctor.Result, id string) doctor.Result {
	for _, r := range results {
		if r.ID == id {
			return r
		}
		if got := find(r.Checks, id); got.ID != "" {
			return got
		}
	}
	return doctor.Result{}
}

func TestDoctorReportsFixesAndSkipsUnder(t *testing.T) {
	gittest.Isolate(t)
	g := git.New(exec.New())
	tool := &fakeDoctor{}
	a := app.New(app.Deps{Git: g, Doctors: []doctor.Provider{tool}})

	res, err := a.Doctor(context.Background(), nil, app.DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r := find(res.Checks, "git"); r.Status != doctor.OK || !strings.HasPrefix(r.Summary, "git ") {
		t.Errorf("git = %+v", r)
	}
	if r := find(res.Checks, "tool"); r.Status != doctor.Fail || r.Fix != "install tool" {
		t.Errorf("tool = %+v", r)
	}
	if r := find(res.Checks, "tool.version"); r.Status != doctor.Skipped {
		t.Errorf("version should be skipped under a failing tool: %+v", r)
	}
	if res.Failed != 1 || res.Fixable != 1 || tool.fixes != 0 {
		t.Errorf("res = %+v, fixes %d", res, tool.fixes)
	}
	if find(res.Checks, "stacks").ID != "" {
		t.Error("stack checks need a repository")
	}

	res, err = a.Doctor(context.Background(), nil, app.DoctorOptions{Fix: true})
	if err != nil {
		t.Fatal(err)
	}
	if r := find(res.Checks, "tool"); r.Status != doctor.Fixed {
		t.Errorf("tool = %+v", r)
	}
	if r := find(res.Checks, "tool.version"); r.Status != doctor.Warn {
		t.Errorf("version should run once the tool is there: %+v", r)
	}
	if res.Failed != 0 || res.Fixed != 1 || res.Warned != 1 || tool.fixes != 1 {
		t.Errorf("res = %+v, fixes %d", res, tool.fixes)
	}
}

func TestDoctorFixThatFails(t *testing.T) {
	gittest.Isolate(t)
	tool := &fakeDoctor{fixErr: errors.New("no network")}
	a := app.New(app.Deps{Git: git.New(exec.New()), Doctors: []doctor.Provider{tool}})
	res, err := a.Doctor(context.Background(), nil, app.DoctorOptions{Fix: true})
	if err != nil {
		t.Fatal(err)
	}
	if r := find(res.Checks, "tool"); r.Status != doctor.Fail || r.FixError != "no network" {
		t.Errorf("tool = %+v", r)
	}
	if tool.fixes != 1 || res.Failed != 1 || res.Fixable != 0 {
		t.Errorf("a failed fix is tried once and not offered again: %+v, fixes %d", res, tool.fixes)
	}
}

func TestDoctorStackChecks(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	gittest.Commit(t, dir, "a.txt", "a", "feat: a")
	gittest.Run(t, dir, "branch", "empty")
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	gittest.Commit(t, dir, "b.txt", "b", "feat: b")
	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	meta := memMeta{stack.NewGraph([]stack.Stack{{Trunk: "main", Branches: []stack.Branch{{Name: "a"}, {Name: "empty"}, {Name: "gone"}, {Name: "b"}}}})}
	a := app.New(app.Deps{Git: g, Meta: meta})

	res, err := a.Doctor(context.Background(), &repo, app.DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r := find(res.Checks, "stacks.empty"); r.Status != doctor.Warn ||
		!slices.Contains(r.Steps, "git stack checkout a, then git stack reorder a gone b empty") {
		t.Errorf("empty = %+v", r)
	}
	if r := find(res.Checks, "stacks.missing"); r.Status != doctor.Warn || !slices.Equal(r.Details, []string{"gone"}) || r.Fix == "" {
		t.Errorf("missing = %+v", r)
	}

	res, err = a.Doctor(context.Background(), &repo, app.DoctorOptions{Fix: true})
	if err != nil {
		t.Fatal(err)
	}
	if r := find(res.Checks, "stacks.missing"); r.Status != doctor.Fixed {
		t.Errorf("missing = %+v", r)
	}
	if got := strings.Join(meta.g.Stacks[0].Names(), ","); got != "a,empty,b" {
		t.Errorf("stack = %s", got)
	}
}
