package ui

import (
	"testing"

	"github.com/DomBlack/git-stack/pkg/doctor"
)

func TestRenderDoctorPlain(t *testing.T) {
	results := []doctor.Result{
		{ID: "git", Title: "git", Status: doctor.OK, Summary: "git 2.47.1"},
		{ID: "gh", Title: "gh", Status: doctor.OK, Summary: "gh version 2.62.0", Checks: []doctor.Result{
			{ID: "gh-stack", Title: "gh-stack", Status: doctor.Fail, Summary: "too old", Details: []string{"v0.2.1\x1b[31m"},
				Fix: "gh extension upgrade stack", Checks: []doctor.Result{
					{ID: "x", Title: "under", Status: doctor.Skipped, Summary: "skipped; needs gh-stack"},
				}},
			{ID: "auth", Title: "auth", Status: doctor.Fixed, Summary: "logged in", Fix: "log in"},
			{ID: "y", Title: "other", Status: doctor.Warn, Fix: "do it", FixError: "no network", Steps: []string{"by hand"}},
		}},
	}
	want := `ok: git: git 2.47.1
ok: gh: gh version 2.62.0
  fail: gh-stack: too old
      v0.2.1\x1b[31m
      - git stack doctor --fix runs: gh extension upgrade stack
    skip: under: skipped; needs gh-stack
  fixed: auth: logged in
      - fixed with log in
  warn: other
      - tried do it, but no network
      - by hand
`
	if got := RenderDoctor(results, Styles{}, false); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
