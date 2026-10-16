package install

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/DomBlack/git-stack/pkg/agents"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/gitconfig"
	"github.com/DomBlack/git-stack/pkg/shell"
)

// Refresh brings what an earlier install set up in line with this binary,
// without prompting: `git stack update` runs it with the new binary once
// the swap is done. It only touches what is already there (aliases and
// files recorded as managed, agent registrations that exist) and only
// reports what it changed; when nothing changed it prints nothing.
//
// Aliases this version adds are installed when the aliases were; an alias
// you have set to something else is left alone. A completion file or skill
// is only rewritten while it is still ours.
func (i *Installer) Refresh(ctx context.Context) error {
	var changes []string
	note := func(format string, args ...any) { changes = append(changes, fmt.Sprintf(format, args...)) }

	managed, err := i.Git.ConfigGetAll(ctx, git.Repo{}, git.ScopeGlobal, config.KeyManagedFiles)
	if err != nil {
		return err
	}
	if err := i.refreshAliases(ctx, note); err != nil {
		return err
	}
	for _, sh := range shell.All {
		path := i.Env.InstallPath(sh)
		if !slices.Contains(managed, path) {
			continue
		}
		existing, err := os.ReadFile(path)
		if err != nil || !isOurs(existing) {
			continue
		}
		cobraScript, err := i.CobraScript(sh)
		if err != nil {
			return err
		}
		script, err := shell.Compose(sh, cobraScript)
		if err != nil {
			return err
		}
		if bytes.Equal(existing, []byte(script)) {
			continue
		}
		if err := writeFile(path, script); err != nil {
			return err
		}
		note("completion  %-5s updated %s", sh, path)
	}
	for _, a := range i.agentList() {
		if !agents.Available(ctx, i.Runner, a) {
			continue
		}
		reg, err := a.Get(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name(), err)
		}
		if !reg.Present || reg.Command == "" || reg.Command == i.Exe {
			continue
		}
		if err := a.Remove(ctx); err != nil {
			return fmt.Errorf("%s: %w", a.Name(), err)
		}
		if err := a.Add(ctx, i.Exe); err != nil {
			return fmt.Errorf("%s: %w", a.Name(), err)
		}
		note("agent  %-12s %s", a.Name(), agents.ActionReplaced)
	}
	for _, t := range i.skillTargets() {
		if !slices.Contains(managed, t.path) {
			continue
		}
		existing, err := os.ReadFile(t.path)
		if err != nil || string(existing) == skillTemplate {
			continue
		}
		if err := writeFile(t.path, skillTemplate); err != nil {
			return err
		}
		note("skill  %-12s updated %s", t.agent, t.path)
	}

	if len(changes) == 0 || i.Report == nil {
		return nil
	}
	noun := "changes"
	if len(changes) == 1 {
		noun = "change"
	}
	i.Report.Success("Refreshed the git-stack install (%d %s)", len(changes), noun)
	for _, c := range changes {
		i.Report.Info("%s", c)
	}
	return nil
}

func (i *Installer) refreshAliases(ctx context.Context, note func(string, ...any)) error {
	m := gitconfig.New(i.Git)
	managed, err := m.Managed(ctx)
	if err != nil || len(managed) == 0 {
		return err
	}
	plan, err := m.Plan(ctx, gitconfig.DefaultAliases)
	if err != nil {
		return err
	}
	for _, e := range plan.Entries {
		if e.Status != gitconfig.StatusNew {
			continue
		}
		if e.Existing != "" {
			note("alias  git %-8s → git %s (was %q)", e.Alias.Name, e.Alias.Command, e.Existing)
		} else {
			note("alias  git %-8s → git %s", e.Alias.Name, e.Alias.Command)
		}
	}
	// Apply without force: conflicts stay as they are, the rest is written
	// and recorded as managed.
	_, _, err = m.Apply(ctx, plan, false)
	return err
}
