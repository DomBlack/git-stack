// Package app holds the use cases. Both the cobra commands and the MCP tools
// call into it, so no behaviour lives anywhere else.
package app

import (
	"context"
	"log/slog"

	"github.com/DomBlack/git-stack/pkg/ai"
	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Deps are the ports an App needs. Optional ports may be nil; the use cases
// that need them return KindUnsupported / KindNotInstalled errors.
type Deps struct {
	Git      *git.Client
	Meta     stack.Metadata
	Tracker  stack.Tracker
	Restack  stack.Restacker
	Submit   stack.Submitter
	Sync     stack.Syncer
	Forge    forge.Forge
	AI       ai.Drafter
	Cache    *cache.Store
	Config   *config.Config
	Log      *slog.Logger
	Prompter Prompter
	// Progress, when set, wraps slow operations (AI calls) so the CLI can
	// show a spinner. It must run fn and return its error.
	Progress func(ctx context.Context, message string, fn func(ctx context.Context) error) error
}

// Prompter asks the user a question. It is nil under --no-interactive and in
// the MCP server, in which case use cases must fall back to a default or
// return KindInteractionRequired.
type Prompter interface {
	Confirm(question string, defaultYes bool) (bool, error)
	Select(question string, options []string) (int, error)
}

// App exposes the use cases.
type App struct {
	d Deps
}

// New wires an App. Log defaults to a discarding logger.
func New(d Deps) *App {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Config == nil {
		d.Config = config.Defaults()
	}
	return &App{d: d}
}
