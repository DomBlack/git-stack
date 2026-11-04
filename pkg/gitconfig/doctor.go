package gitconfig

import (
	"context"
	"fmt"
	"strings"

	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/git"
)

var _ doctor.Provider = (*Manager)(nil)

// Checks implements doctor.Provider: whether git-stack's git aliases are
// set. One you set to something else yourself is listed but never counts
// against you, and the fix never replaces it.
func (m *Manager) Checks(*git.Repo) []doctor.Check {
	return []doctor.Check{{
		ID:    "aliases",
		Title: "git aliases",
		Run: func(ctx context.Context) doctor.Finding {
			plan, err := m.Plan(ctx, DefaultAliases)
			if err != nil {
				return doctor.Finding{Status: doctor.Fail, Summary: "can't read git's aliases", Details: []string{err.Error()}}
			}
			var missing, theirs []string
			for _, e := range plan.Entries {
				switch e.Status {
				case StatusNew:
					missing = append(missing, "git "+e.Alias.Name)
				case StatusConflict:
					theirs = append(theirs, fmt.Sprintf("git %s is %q, not %q", e.Alias.Name, e.Existing, e.Alias.Command))
				}
			}
			f := doctor.Finding{Status: doctor.OK, Details: theirs}
			switch {
			case len(missing) == len(plan.Entries):
				f.Status = doctor.Warn
				f.Summary = "not installed, so git ss, git c and the rest aren't there"
				f.Steps = []string{"git stack install sets them up with completion and agent registration"}
			case len(missing) > 0:
				f.Status = doctor.Warn
				f.Summary = fmt.Sprintf("%d missing: %s", len(missing), strings.Join(missing, ", "))
			default:
				f.Summary = fmt.Sprintf("%d set", len(plan.Entries)-len(theirs))
			}
			if len(theirs) > 0 {
				f.Steps = append(f.Steps, "git stack install --force replaces the ones you set yourself")
			}
			return f
		},
		Fix: func(ctx context.Context) error {
			plan, err := m.Plan(ctx, DefaultAliases)
			if err != nil {
				return err
			}
			_, _, err = m.Apply(ctx, plan, false)
			return err
		},
		FixLabel: "set the missing aliases (never replacing one you set yourself)",
	}}
}
