package update

import (
	"context"
	"errors"
	"fmt"

	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Doctor checks whether git-stack itself is the latest release.
type Doctor struct {
	Client *Client
	// Options are what an update would use (Current, Target, GOOS, GOARCH).
	Options Options
	// AfterUpdate runs once the binary has been replaced, with its path (the
	// CLI hands over to the new binary to refresh what install set up). Nil
	// leaves the check without a fix: the MCP server reports, it doesn't
	// replace the binary it runs as.
	AfterUpdate func(ctx context.Context, target string) error

	updated string // the release a fix installed
}

var _ doctor.Provider = (*Doctor)(nil)

// Checks implements doctor.Provider.
func (d *Doctor) Checks(*git.Repo) []doctor.Check {
	c := doctor.Check{
		ID:    "git-stack",
		Title: "git-stack is up to date",
		Run: func(ctx context.Context) doctor.Finding {
			cur := d.Options.Current
			if d.updated != "" {
				return doctor.Finding{Status: doctor.OK, Summary: fmt.Sprintf("updated from %s to %s; the next command runs it", cur.Version, d.updated)}
			}
			o := d.Options
			o.Check = true
			p, err := d.Client.Plan(ctx, o)
			if err != nil {
				msg := err.Error()
				if se, ok := errors.AsType[*stack.Error](err); ok {
					msg = se.Msg
				}
				return doctor.Finding{Status: doctor.Warn, Summary: "couldn't check for a newer release", Details: []string{msg}}
			}
			r := p.Result
			switch {
			case r.DevBuild:
				return doctor.Finding{Status: doctor.OK, Summary: "a dev build, so it isn't compared with releases (the latest is " + r.Latest + ")"}
			case r.Status == StatusAvailable:
				return doctor.Finding{Status: doctor.Warn, Summary: fmt.Sprintf("%s is out, you have %s", r.Latest, r.Current),
					Steps: []string{"git stack update installs it"}}
			case r.Status == StatusAhead:
				return doctor.Finding{Status: doctor.OK, Summary: fmt.Sprintf("%s, ahead of the latest release (%s)", r.Current, r.Latest)}
			}
			return doctor.Finding{Status: doctor.OK, Summary: r.Current + ", the latest release"}
		},
	}
	if d.AfterUpdate != nil {
		c.Fix = func(ctx context.Context) error {
			res, err := d.Client.Run(ctx, d.Options)
			if err != nil {
				return err
			}
			if res.Status != StatusUpdated {
				return nil
			}
			d.updated = res.Latest
			return d.AfterUpdate(ctx, res.Target)
		}
		c.FixLabel = "git stack update"
	}
	return []doctor.Check{c}
}
