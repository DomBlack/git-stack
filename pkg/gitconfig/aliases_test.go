package gitconfig_test

import (
	"context"
	"slices"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/gitconfig"
)

func TestPlanApplyUninstall(t *testing.T) {
	gittest.Isolate(t)
	home := t.TempDir()
	g := git.New(exec.New())
	ctx := context.Background()
	gittest.Run(t, home, "config", "--global", "alias.co", "checkout")             // conflict
	gittest.Run(t, home, "config", "--global", "alias.up", "stack up")             // same
	gittest.Run(t, home, "config", "--global", "alias.recent", "!echo x")          // untouched user alias
	gittest.Run(t, home, "config", "--global", "alias.ss", "stack submit --stack") // an old value of ours

	aliases := append(slices.Clone(gitconfig.DefaultAliases), gitconfig.Alias{Name: "checkout", Command: "stack checkout"}, gitconfig.Alias{Name: "lfs", Command: "stack x"})
	m := gitconfig.New(g)
	plan, err := m.Plan(ctx, aliases)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]gitconfig.Status{}
	for _, e := range plan.Entries {
		status[e.Alias.Name] = e.Status
	}
	if status["co"] != gitconfig.StatusConflict || status["up"] != gitconfig.StatusSame || status["c"] != gitconfig.StatusNew || status["checkout"] != gitconfig.StatusBuiltin {
		t.Errorf("statuses = %v", status)
	}
	if status["ss"] != gitconfig.StatusNew {
		t.Errorf("retired ss value should be replaced without asking, got %v", status["ss"])
	}
	if s, ok := status["lfs"]; ok && s != gitconfig.StatusShadows && s != gitconfig.StatusNew {
		t.Errorf("lfs status = %v", s)
	}

	installed, skipped, err := m.Apply(ctx, plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(installed, "co") || !slices.Contains(skipped, "co") || !slices.Contains(skipped, "checkout") {
		t.Errorf("installed=%v skipped=%v", installed, skipped)
	}
	if !slices.Contains(installed, "up") || !slices.Contains(installed, "ss") {
		t.Errorf("installed = %v", installed)
	}
	if v := gittest.Run(t, home, "config", "--global", "alias.ss"); v != "stack submit" {
		t.Errorf("ss = %q", v)
	}
	if v := gittest.Run(t, home, "config", "--global", "alias.co"); v != "checkout" {
		t.Errorf("conflict overwritten without force: %q", v)
	}
	managed, _ := m.Managed(ctx)
	if !slices.Contains(managed, "up") || slices.Contains(managed, "co") {
		t.Errorf("managed = %v", managed)
	}

	// Force overwrites the conflict; applying twice does not duplicate records.
	if _, _, err := m.Apply(ctx, plan, true); err != nil {
		t.Fatal(err)
	}
	if v := gittest.Run(t, home, "config", "--global", "alias.co"); v != "stack checkout" {
		t.Errorf("force: %q", v)
	}
	managed, _ = m.Managed(ctx)
	if n := slices.Index(managed, "up"); n < 0 || slices.Contains(managed[n+1:], "up") {
		t.Errorf("duplicate managed records: %v", managed)
	}

	// User edits one managed alias; uninstall keeps it and removes the rest.
	gittest.Run(t, home, "config", "--global", "alias.d", "!echo mine")
	removed, kept, err := m.Uninstall(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(removed, "ss") || !slices.Contains(kept, "d") {
		t.Errorf("removed=%v kept=%v", removed, kept)
	}
	if v := gittest.Run(t, home, "config", "--global", "alias.recent"); v != "!echo x" {
		t.Errorf("user alias touched: %q", v)
	}
	if v := gittest.Run(t, home, "config", "--global", "alias.d"); v != "!echo mine" {
		t.Errorf("edited alias removed: %q", v)
	}
	if managed, _ := m.Managed(ctx); len(managed) != 0 {
		t.Errorf("managed record not cleared: %v", managed)
	}
	if _, _, err := m.Uninstall(ctx); err != nil {
		t.Errorf("second uninstall: %v", err)
	}
}
