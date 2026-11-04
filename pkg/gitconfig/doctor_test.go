package gitconfig_test

import (
	"context"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/gitconfig"
)

func TestDoctorAliases(t *testing.T) {
	gittest.Isolate(t)
	home := t.TempDir()
	ctx := context.Background()
	m := gitconfig.New(git.New(exec.New()))
	c := m.Checks(nil)[0]
	if f := c.Run(ctx); f.Status != doctor.Warn || !strings.Contains(f.Summary, "not installed") {
		t.Errorf("none set = %+v", f)
	}
	gittest.Run(t, home, "config", "--global", "alias.co", "checkout") // the user's own
	if err := c.Fix(ctx); err != nil {
		t.Fatal(err)
	}
	f := c.Run(ctx)
	if f.Status != doctor.OK || len(f.Details) != 1 || !strings.Contains(f.Details[0], `git co is "checkout"`) {
		t.Errorf("after the fix = %+v", f)
	}
	if got := gittest.Run(t, home, "config", "--global", "alias.co"); got != "checkout" {
		t.Errorf("the user's alias was replaced: %q", got)
	}
	gittest.Run(t, home, "config", "--global", "--unset", "alias.ss")
	if f := c.Run(ctx); f.Status != doctor.Warn || f.Summary != "1 missing: git ss" {
		t.Errorf("one missing = %+v", f)
	}
}
