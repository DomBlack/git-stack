// Package install sets up git aliases, shell completion (with git
// integration), agent MCP registration and the optional Claude Code skill,
// and removes exactly what it installed.
package install

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/agents"
	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/gitconfig"
	"github.com/DomBlack/git-stack/pkg/shell"
)

//go:embed skill/SKILL.md
var skillTemplate string

// Options selects what to install.
type Options struct {
	Aliases    bool
	Completion bool
	Agents     bool
	// Skill is nil to ask (or skip when non-interactive), otherwise explicit.
	Skill  *bool
	Shells []shell.Shell
	DryRun bool
	Force  bool
}

// Installer performs the work.
type Installer struct {
	Git    *git.Client
	Runner exec.Runner
	// Exe is the absolute path of the git-stack binary for agent registration.
	Exe string
	Env shell.Env
	// CobraScript generates cobra's completion script for a shell.
	CobraScript func(sh shell.Shell) (string, error)
	// Prompter is nil when non-interactive.
	Prompter app.Prompter
	// Out receives the human-readable report.
	Out io.Writer
	// Agents overrides the agent list (tests).
	Agents []agents.Agent
}

func (i *Installer) say(format string, args ...any) {
	if i.Out != nil {
		fmt.Fprintf(i.Out, format+"\n", args...)
	}
}

// Run installs the selected components.
func (i *Installer) Run(ctx context.Context, o Options) error {
	if o.DryRun {
		i.say("dry run: nothing will be changed")
	}
	if o.Aliases {
		if err := i.installAliases(ctx, o); err != nil {
			return err
		}
	}
	if o.Completion {
		if err := i.installCompletion(ctx, o); err != nil {
			return err
		}
	}
	if o.Agents {
		if err := i.installAgents(ctx, o); err != nil {
			return err
		}
	}
	if err := i.installSkill(ctx, o); err != nil {
		return err
	}
	return nil
}

func (i *Installer) installAliases(ctx context.Context, o Options) error {
	m := gitconfig.New(i.Git)
	plan, err := m.Plan(ctx, gitconfig.DefaultAliases)
	if err != nil {
		return err
	}
	for idx := range plan.Entries {
		e := &plan.Entries[idx]
		switch e.Status {
		case gitconfig.StatusNew:
			i.say("  alias  git %-8s → git %s", e.Alias.Name, e.Alias.Command)
		case gitconfig.StatusSame:
			i.say("  alias  git %-8s already set", e.Alias.Name)
		case gitconfig.StatusBuiltin, gitconfig.StatusShadows:
			i.say("  alias  git %-8s skipped: %s", e.Alias.Name, e.Status)
		case gitconfig.StatusConflict:
			overwrite := o.Force
			if !overwrite && i.Prompter != nil && !o.DryRun {
				overwrite, err = i.Prompter.Confirm(fmt.Sprintf("Alias git %s is already %q. Replace it with %q?", e.Alias.Name, e.Existing, e.Alias.Command), false)
				if err != nil {
					return err
				}
			}
			if overwrite {
				i.say("  alias  git %-8s → git %s (replacing %q)", e.Alias.Name, e.Alias.Command, e.Existing)
				e.Status = gitconfig.StatusNew
			} else {
				i.say("  alias  git %-8s kept as %q (use --force to replace)", e.Alias.Name, e.Existing)
			}
		}
	}
	if o.DryRun {
		return nil
	}
	_, _, err = m.Apply(ctx, plan, false)
	return err
}

func (i *Installer) installCompletion(ctx context.Context, o Options) error {
	shells := o.Shells
	if len(shells) == 0 {
		if sh, ok := shell.Detect(os.Getenv("SHELL")); ok {
			shells = []shell.Shell{sh}
		} else {
			i.say("  completion: could not detect the shell from $SHELL; pass --shell")
			return nil
		}
	}
	for _, sh := range shells {
		cobraScript, err := i.CobraScript(sh)
		if err != nil {
			return err
		}
		script, err := shell.Compose(sh, cobraScript)
		if err != nil {
			return err
		}
		path := i.Env.InstallPath(sh)
		existing, err := os.ReadFile(path)
		switch {
		case err == nil && bytes.Equal(existing, []byte(script)):
			i.say("  completion  %-5s already installed at %s", sh, path)
			continue
		case err == nil:
			// Our own older script is updated silently; anything else needs
			// --force or consent.
			replace := o.Force || isOurs(existing)
			if !replace && i.Prompter != nil && !o.DryRun {
				replace, err = i.Prompter.Confirm(fmt.Sprintf("%s exists and is not managed by git-stack. Replace it?", path), false)
				if err != nil {
					return err
				}
			}
			if !replace {
				i.say("  completion  %-5s kept existing file %s", sh, path)
				continue
			}
			i.say("  completion  %-5s update %s", sh, path)
		case errors.Is(err, fs.ErrNotExist):
			i.say("  completion  %-5s write %s", sh, path)
		default:
			return err
		}
		i.say("              %s", i.Env.Hint(sh))
		if o.DryRun {
			continue
		}
		if err := writeFile(path, script); err != nil {
			return err
		}
		if err := i.recordFile(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

// marker identifies scripts we generated.
const marker = "git-stack git integration"

func isOurs(b []byte) bool { return bytes.Contains(b, []byte(marker)) }

func (i *Installer) agentList() []agents.Agent {
	if i.Agents != nil {
		return i.Agents
	}
	return agents.All(i.Runner)
}

func (i *Installer) installAgents(ctx context.Context, o Options) error {
	for _, a := range i.agentList() {
		if !agents.Available(ctx, i.Runner, a) {
			i.say("  agent  %-12s not on PATH, skipped", a.Name())
			continue
		}
		if o.DryRun {
			reg, err := a.Get(ctx)
			if err != nil {
				return err
			}
			switch {
			case !reg.Present:
				i.say("  agent  %-12s would register `%s mcp` (%s mcp add)", a.Name(), i.Exe, a.Binary())
			case reg.Command == "" || reg.Command == i.Exe:
				i.say("  agent  %-12s already registered", a.Name())
			default:
				i.say("  agent  %-12s would re-register (currently %s)", a.Name(), reg.Command)
			}
			continue
		}
		act, err := agents.Ensure(ctx, a, i.Exe)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name(), err)
		}
		i.say("  agent  %-12s %s", a.Name(), act)
	}
	return nil
}

// SkillPath is where the Claude Code personal skill is written.
func (i *Installer) SkillPath() string {
	return filepath.Join(i.Env.Home, ".claude", "skills", "git-stack", "SKILL.md")
}

func (i *Installer) installSkill(ctx context.Context, o Options) error {
	want := false
	switch {
	case o.Skill != nil:
		want = *o.Skill
	case i.Prompter != nil && !o.DryRun:
		var err error
		want, err = i.Prompter.Confirm("Install a Claude Code skill that teaches agents the stacked workflow (~/.claude/skills/git-stack)?", true)
		if err != nil {
			return err
		}
	}
	if !want {
		return nil
	}
	path := i.SkillPath()
	if existing, err := os.ReadFile(path); err == nil && string(existing) == skillTemplate {
		i.say("  skill  already installed at %s", path)
		return nil
	}
	i.say("  skill  write %s", path)
	if o.DryRun {
		return nil
	}
	if err := writeFile(path, skillTemplate); err != nil {
		return err
	}
	return i.recordFile(ctx, path)
}

func (i *Installer) recordFile(ctx context.Context, path string) error {
	files, err := i.Git.ConfigGetAll(ctx, git.Repo{}, git.ScopeGlobal, config.KeyManagedFiles)
	if err != nil {
		return err
	}
	if slices.Contains(files, path) {
		return nil
	}
	return i.Git.ConfigAdd(ctx, git.Repo{}, git.ScopeGlobal, config.KeyManagedFiles, path)
}

// Uninstall reverses everything recorded as managed.
func (i *Installer) Uninstall(ctx context.Context, o Options) error {
	if o.DryRun {
		i.say("dry run: nothing will be changed")
	}
	if o.Aliases {
		m := gitconfig.New(i.Git)
		if o.DryRun {
			managed, err := m.Managed(ctx)
			if err != nil {
				return err
			}
			for _, name := range managed {
				i.say("  alias  git %-8s remove", name)
			}
		} else {
			removed, kept, err := m.Uninstall(ctx)
			if err != nil {
				return err
			}
			for _, name := range removed {
				i.say("  alias  git %-8s removed", name)
			}
			for _, name := range kept {
				i.say("  alias  git %-8s kept: you changed it since it was installed", name)
			}
		}
	}
	if o.Completion || o.Skill == nil || *o.Skill {
		files, err := i.Git.ConfigGetAll(ctx, git.Repo{}, git.ScopeGlobal, config.KeyManagedFiles)
		if err != nil {
			return err
		}
		for _, path := range files {
			if o.DryRun {
				i.say("  file   remove %s", path)
				continue
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			i.say("  file   removed %s", path)
		}
		if !o.DryRun {
			if err := i.Git.ConfigUnset(ctx, git.Repo{}, git.ScopeGlobal, config.KeyManagedFiles); err != nil {
				return err
			}
		}
	}
	if o.Agents {
		for _, a := range i.agentList() {
			if !agents.Available(ctx, i.Runner, a) {
				continue
			}
			reg, err := a.Get(ctx)
			if err != nil {
				return err
			}
			if !reg.Present {
				continue
			}
			if o.DryRun {
				i.say("  agent  %-12s would unregister", a.Name())
				continue
			}
			if err := a.Remove(ctx); err != nil {
				return fmt.Errorf("%s: %w", a.Name(), err)
			}
			i.say("  agent  %-12s unregistered", a.Name())
		}
	}
	return nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// SkillContent exposes the embedded skill (docs/tests).
func SkillContent() string { return strings.TrimSpace(skillTemplate) + "\n" }
