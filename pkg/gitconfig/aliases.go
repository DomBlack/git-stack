// Package gitconfig installs and removes the git aliases that make
// `git create`, `git co` and friends run git-stack. Everything goes through
// `git config --global`; the file is never edited as text.
package gitconfig

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/git"
)

// Alias maps a git alias to a git-stack command.
type Alias struct {
	Name string
	// Command is the part after "git ", e.g. "stack submit --stack".
	Command string
}

// Value is the alias value written to git config.
func (a Alias) Value() string { return a.Command }

// DefaultAliases mirrors Graphite's command names and short forms. "checkout"
// is a git builtin and can never be aliased, hence "co".
var DefaultAliases = []Alias{
	{"create", "stack create"},
	{"modify", "stack modify"},
	{"restack", "stack restack"},
	{"submit", "stack submit"},
	{"sync", "stack sync"},
	{"up", "stack up"},
	{"down", "stack down"},
	{"top", "stack top"},
	{"bottom", "stack bottom"},
	{"continue", "stack continue"},
	{"abort", "stack abort"},
	{"c", "stack create"},
	{"m", "stack modify"},
	{"rs", "stack restack"},
	{"ss", "stack submit --stack"},
	{"u", "stack up"},
	{"d", "stack down"},
	{"t", "stack top"},
	{"b", "stack bottom"},
	{"co", "stack checkout"},
}

// Status of one alias in a Plan.
type Status int

const (
	// StatusNew: not set yet.
	StatusNew Status = iota
	// StatusSame: already set to our value.
	StatusSame
	// StatusConflict: set to a different value.
	StatusConflict
	// StatusBuiltin: git would ignore it because a builtin has that name.
	StatusBuiltin
	// StatusShadows: an installed git command has that name.
	StatusShadows
)

func (s Status) String() string {
	switch s {
	case StatusNew:
		return "new"
	case StatusSame:
		return "already installed"
	case StatusConflict:
		return "conflict"
	case StatusBuiltin:
		return "shadows a git builtin"
	case StatusShadows:
		return "shadows an installed git command"
	default:
		return "unknown"
	}
}

// Entry is one alias with its planned status.
type Entry struct {
	Alias    Alias
	Status   Status
	Existing string
}

// Plan lists what installing the aliases would do.
type Plan struct {
	Entries []Entry
}

// Manager plans and applies alias changes.
type Manager struct {
	git *git.Client
}

// New returns a Manager.
func New(g *git.Client) *Manager {
	return &Manager{git: g}
}

// Plan checks every alias against builtins, installed commands and existing
// aliases without changing anything.
func (m *Manager) Plan(ctx context.Context, aliases []Alias) (Plan, error) {
	builtins, err := m.git.ListCmds(ctx, "builtins")
	if err != nil {
		return Plan{}, fmt.Errorf("list git builtins: %w", err)
	}
	others, err := m.git.ListCmds(ctx, "main,others")
	if err != nil {
		return Plan{}, fmt.Errorf("list git commands: %w", err)
	}
	existing, err := m.git.ConfigGetRegexp(ctx, git.Repo{}, git.ScopeGlobal, `^alias\.`)
	if err != nil {
		return Plan{}, err
	}
	current := make(map[string]string, len(existing))
	for _, e := range existing {
		current[strings.TrimPrefix(strings.ToLower(e.Key), "alias.")] = e.Value
	}
	var p Plan
	for _, a := range aliases {
		e := Entry{Alias: a}
		switch {
		case slices.Contains(builtins, a.Name):
			e.Status = StatusBuiltin
		case slices.Contains(others, a.Name):
			e.Status = StatusShadows
		default:
			if v, ok := current[strings.ToLower(a.Name)]; ok {
				e.Existing = v
				if v == a.Value() {
					e.Status = StatusSame
				} else {
					e.Status = StatusConflict
				}
			}
		}
		p.Entries = append(p.Entries, e)
	}
	return p, nil
}

// Apply writes the aliases in the plan. Conflicts are written only when
// force is set; builtins and shadowing names are always skipped. Every
// alias written is recorded in stack.managedAliases. It returns the names
// installed and the names skipped.
func (m *Manager) Apply(ctx context.Context, p Plan, force bool) (installed, skipped []string, err error) {
	managed, err := m.git.ConfigGetAll(ctx, git.Repo{}, git.ScopeGlobal, config.KeyManagedAliases)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range p.Entries {
		switch e.Status {
		case StatusBuiltin, StatusShadows:
			skipped = append(skipped, e.Alias.Name)
			continue
		case StatusConflict:
			if !force {
				skipped = append(skipped, e.Alias.Name)
				continue
			}
		case StatusSame:
			// Adopt it as managed even if it was set by hand.
		}
		if e.Status != StatusSame {
			if err := m.git.ConfigSet(ctx, git.Repo{}, git.ScopeGlobal, "alias."+e.Alias.Name, e.Alias.Value()); err != nil {
				return installed, skipped, err
			}
		}
		if !slices.Contains(managed, e.Alias.Name) {
			if err := m.git.ConfigAdd(ctx, git.Repo{}, git.ScopeGlobal, config.KeyManagedAliases, e.Alias.Name); err != nil {
				return installed, skipped, err
			}
			managed = append(managed, e.Alias.Name)
		}
		installed = append(installed, e.Alias.Name)
	}
	return installed, skipped, nil
}

// Managed returns the aliases recorded as ours.
func (m *Manager) Managed(ctx context.Context) ([]string, error) {
	return m.git.ConfigGetAll(ctx, git.Repo{}, git.ScopeGlobal, config.KeyManagedAliases)
}

// Uninstall removes every managed alias that still points at git-stack and
// clears the record. Aliases the user changed since are left alone and
// reported in kept.
func (m *Manager) Uninstall(ctx context.Context) (removed, kept []string, err error) {
	managed, err := m.Managed(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range managed {
		v, ok, err := m.git.ConfigGet(ctx, git.Repo{}, git.ScopeGlobal, "alias."+name)
		if err != nil {
			return removed, kept, err
		}
		switch {
		case !ok:
			// Already gone.
		case strings.HasPrefix(v, "stack ") || v == "stack":
			if err := m.git.ConfigUnset(ctx, git.Repo{}, git.ScopeGlobal, "alias."+name); err != nil {
				return removed, kept, err
			}
			removed = append(removed, name)
		default:
			kept = append(kept, name)
		}
	}
	if err := m.git.ConfigUnset(ctx, git.Repo{}, git.ScopeGlobal, config.KeyManagedAliases); err != nil {
		return removed, kept, err
	}
	return removed, kept, nil
}
