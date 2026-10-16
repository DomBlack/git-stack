package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/ui"
	"github.com/DomBlack/git-stack/pkg/update"
)

func newUpdateCmd(c *cli) *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update git-stack to the latest release",
		Long: `Download the latest GitHub release and replace this binary with it.

The archive for this OS and architecture is verified against the release's
checksums.txt before anything is written. No login is needed; releases are
fetched over plain HTTPS.

A binary built from source (git stack version says "dev") is left alone unless
you pass --force, since you probably built it that way on purpose.

It lists what changed in every release between this one and the latest
(a one line summary each, plus any breaking changes) with a link to the full
release notes, with or without --check.

Once updated, whatever git stack install set up before (aliases, completion,
agent registration, the Claude Code and Codex skills) is brought in line with
the new release without prompting. Only the changes are printed.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := c.currentVersion()
			target := c.updateTarget
			if target == "" {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				if resolved, err := filepath.EvalSymlinks(exe); err == nil {
					exe = resolved
				}
				target = exe
			}
			client := c.updateClient
			if client == nil {
				client = update.New(info)
			}
			rep := c.report()
			opts := update.Options{
				Current: info, Target: target, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Check: check, Force: force,
			}
			var (
				plan     update.Plan
				notes    []update.ReleaseNote
				notesErr error
			)
			err := rep.Step(cmd.Context(), app.PhaseUpdate, "Checking for updates", func(ctx context.Context) error {
				var err error
				plan, err = client.Plan(ctx, opts)
				if err != nil {
					return err
				}
				if plan.Install || plan.Result.Status == update.StatusAvailable {
					// Every release between this one and the new one, so
					// skipping a few still shows what each brought.
					from := ""
					if info.Release {
						from = info.Version
					}
					notes, notesErr = client.Notes(ctx, from, plan.Result.Latest)
				}
				return nil
			})
			if err != nil {
				return err
			}
			res := plan.Result
			if plan.Install {
				err := rep.Step(cmd.Context(), app.PhaseUpdate, "Downloading git-stack "+res.Latest, func(ctx context.Context) error {
					var err error
					res, err = client.Install(ctx, plan, opts)
					return err
				})
				if err != nil {
					return err
				}
			}
			switch res.Status {
			case update.StatusUpToDate:
				rep.Success("git-stack %s is the latest release", res.Current)
			case update.StatusAhead:
				rep.Success("git-stack %s is ahead of the latest release (%s)", res.Current, res.Latest)
			case update.StatusAvailable:
				if res.DevBuild {
					rep.Partial("This is a dev build (%s); the latest release is %s", info, res.Latest)
					showNotes(rep, client, notes, notesErr)
					rep.NextStep("git stack update --force replaces it with the release")
					rep.NextStep("or rebuild from your checkout")
				} else {
					rep.Partial("Update available: %s is out, you have %s", res.Latest, res.Current)
					showNotes(rep, client, notes, notesErr)
					rep.NextStep("git stack update installs it")
				}
			case update.StatusUpdated:
				rep.Success("Updated git-stack %s to %s (%s)", info, res.Latest, res.Target)
				showNotes(rep, client, notes, notesErr)
				return refreshInstall(cmd.Context(), c, res.Target)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().BoolVar(&force, "force", false, "install the latest release even if this binary is up to date, ahead, or a dev build")
	return cmd
}

// maxNotes is how many releases update lists before "and N more".
const maxNotes = 10

// showNotes lists what the releases being installed (or available) brought,
// newest first: each one's summary line, then any breaking changes, then
// where to read the full notes. Not being able to fetch them never stops an
// update; it just says where they are.
func showNotes(rep *ui.Reporter, client *update.Client, notes []update.ReleaseNote, err error) {
	if err != nil {
		rep.Warn("Couldn't fetch what changed; the release notes are at %s", client.ReleasesURL())
		return
	}
	if len(notes) == 0 {
		return
	}
	shown := notes[:min(len(notes), maxNotes)]
	tagWidth := 0
	for _, n := range shown {
		tagWidth = max(tagWidth, len(n.Tag))
	}
	width := rep.Width()
	if width > 0 {
		width -= 2 // Info's indent
	}
	for _, n := range shown {
		head := ui.Hyperlink(rep.Links(), n.URL, n.Tag) + strings.Repeat(" ", tagWidth-len(n.Tag)+2)
		rep.Info("%s", ui.FitLine(head, ui.Printable(n.Summary), width))
	}
	if len(notes) > len(shown) {
		rep.Info("and %d more", len(notes)-len(shown))
	}
	for _, n := range notes {
		for _, pr := range n.Breaking {
			rep.Info("breaking change in %s: %s %s", n.Tag, ui.Printable(pr.Title), rep.Ref(pr.Number, pr.URL, forge.StateUnknown))
		}
	}
	url := client.ReleasesURL()
	if len(notes) == 1 && notes[0].URL != "" {
		url = notes[0].URL
	}
	rep.Info("Release notes: %s", rep.Link(url))
}

// refreshInstall hands over to the new binary to bring whatever git stack
// install set up before (aliases, completion, agent registrations, skills)
// in line with it. It has to be the new binary: the completion script and
// the skill are built into it. It prints only what it changed.
func refreshInstall(ctx context.Context, c *cli, exe string) error {
	rt := c.runtime()
	args := []string{"install", "--refresh", "--no-interactive"}
	if c.globals.Quiet {
		args = append(args, "--quiet")
	}
	cmd := exec.Cmd{
		Name: exe,
		Args: args,
		// This process already owns the window title and program status,
		// and has just checked for updates.
		Env: []string{noTerminalStatusEnv + "=1", "GIT_STACK_NO_UPDATE_CHECK=1"},
	}
	// On a terminal the new binary writes straight to it, so its lines look
	// like ours; otherwise its plain output is passed on.
	if rt.Interactive {
		cmd.Mode = exec.Passthrough
	}
	res, err := rt.Runner.Run(ctx, cmd)
	if err != nil {
		se := stack.New(stack.KindUnknown, "git-stack was updated, but refreshing what git stack install set up failed").
			WithCause(err).
			WithSteps("git stack install")
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			se = se.WithDetail(ee.Result.Err())
		}
		return se
	}
	rt.Report.Print(string(res.Stdout))
	return nil
}
