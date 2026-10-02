package install

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/agents"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/shell"
)

const fakeFish = "function __git_stack_perform_completion\n    set -l args (commandline -opc)\nend\n"

type yesPrompter struct{ answer bool }

func (p yesPrompter) Confirm(string, bool) (bool, error)   { return p.answer, nil }
func (p yesPrompter) Select(string, []string) (int, error) { return 0, nil }

func newInstaller(t *testing.T, f *exectest.Fake, out *bytes.Buffer) *Installer {
	t.Helper()
	gittest.Isolate(t)
	home := os.Getenv("HOME")
	real := exec.New()
	f.Fallback = func(c exec.Cmd) (exec.Result, error) { return real.Run(context.Background(), c) }
	return &Installer{
		Git:    git.New(f),
		Runner: f,
		Exe:    "/opt/bin/git-stack",
		Env:    shell.Env{Home: home, XDGConfigHome: filepath.Join(home, ".config")},
		CobraScript: func(sh shell.Shell) (string, error) {
			if sh == shell.Fish {
				return fakeFish, nil
			}
			return "__start_git-stack() { :; }\n", nil
		},
		Report: bufReport{out},
	}
}

func TestInstallAndUninstall(t *testing.T) {
	f := exectest.New()
	f.On("claude", "--version").Reply("2.1")
	f.On("claude", "mcp", "get").Fail(1, `No MCP server named "git-stack"`)
	f.On("claude", "mcp", "add").Reply("")
	f.On("claude", "mcp", "remove").Reply("")
	f.On("codex").NotFound()
	var out bytes.Buffer
	in := newInstaller(t, f, &out)
	home := in.Env.Home
	gittest.Run(t, home, "config", "--global", "alias.co", "checkout")
	ctx := context.Background()
	yes := true
	opts := Options{Aliases: true, Completion: true, Agents: true, Skill: &yes, Shells: []shell.Shell{shell.Fish, shell.Bash}}

	// Dry run changes nothing.
	dry := opts
	dry.DryRun = true
	if err := in.Run(ctx, dry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(in.Env.InstallPath(shell.Fish)); err == nil {
		t.Fatal("dry run wrote the completion file")
	}
	if len(f.CallsTo("claude")) > 2 { // version + get only
		t.Errorf("dry run touched claude: %v", f.CallsTo("claude"))
	}
	if !strings.Contains(out.String(), "dry run") || !strings.Contains(out.String(), "git c ") || !strings.Contains(out.String(), "kept as \"checkout\"") {
		t.Errorf("dry run report:\n%s", out.String())
	}

	out.Reset()
	f.Reset()
	if err := in.Run(ctx, opts); err != nil {
		t.Fatal(err)
	}
	report := out.String()
	fishPath := in.Env.InstallPath(shell.Fish)
	b, err := os.ReadFile(fishPath)
	if err != nil || !strings.Contains(string(b), "__git_stack_git_args") {
		t.Errorf("fish completion not written: %v", err)
	}
	if _, err := os.ReadFile(in.Env.InstallPath(shell.Bash)); err != nil {
		t.Errorf("bash completion not written: %v", err)
	}
	if v := gittest.Run(t, home, "config", "--global", "alias.ss"); v != "stack submit --stack" {
		t.Errorf("alias ss = %q", v)
	}
	if v := gittest.Run(t, home, "config", "--global", "alias.co"); v != "checkout" {
		t.Errorf("conflicting alias replaced without force/confirmation: %q", v)
	}
	skill, err := os.ReadFile(in.SkillPath())
	if err != nil || !strings.HasPrefix(string(skill), "---\nname: git-stack") {
		t.Errorf("skill not written: %v", err)
	}
	var addArgs string
	for _, c := range f.CallsTo("claude") {
		if len(c.Args) > 1 && c.Args[1] == "add" {
			addArgs = strings.Join(c.Args, " ")
		}
	}
	if addArgs != "mcp add --scope user git-stack -- /opt/bin/git-stack mcp" {
		t.Errorf("claude add = %q", addArgs)
	}
	if !strings.Contains(report, "Codex        not on PATH") || !strings.Contains(report, "Claude Code  registered") {
		t.Errorf("report:\n%s", report)
	}
	managed := gittest.Run(t, home, "config", "--global", "--get-all", "stack.managedFiles")
	if !strings.Contains(managed, fishPath) || !strings.Contains(managed, in.SkillPath()) {
		t.Errorf("managed files = %q", managed)
	}

	// Second run is idempotent.
	out.Reset()
	f.On("claude", "mcp", "get").Reply("git-stack:\n  Command: /opt/bin/git-stack\n")
	if err := in.Run(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already installed") || !strings.Contains(out.String(), "already registered") || !strings.Contains(out.String(), "already set") {
		t.Errorf("second run report:\n%s", out.String())
	}
	if n := strings.Count(gittest.Run(t, home, "config", "--global", "--get-all", "stack.managedAliases"), "\n"); n > len(gitconfigDefaults())-1 {
		t.Errorf("managed aliases duplicated")
	}

	// Force replaces the conflicting alias; a prompter can also approve it.
	force := opts
	force.Force = true
	if err := in.Run(ctx, force); err != nil {
		t.Fatal(err)
	}
	if v := gittest.Run(t, home, "config", "--global", "alias.co"); v != "stack checkout" {
		t.Errorf("force: alias co = %q", v)
	}

	// A foreign completion file is only replaced with consent.
	if err := os.WriteFile(fishPath, []byte("# someone else's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in.Prompter = yesPrompter{answer: false}
	if err := in.Run(ctx, Options{Completion: true, Shells: []shell.Shell{shell.Fish}, Skill: new(bool)}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(fishPath); !strings.HasPrefix(string(b), "# someone else's") {
		t.Error("foreign file replaced without consent")
	}
	in.Prompter = yesPrompter{answer: true}
	if err := in.Run(ctx, Options{Completion: true, Shells: []shell.Shell{shell.Fish}, Skill: new(bool)}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(fishPath); !strings.Contains(string(b), "__git_stack_git_args") {
		t.Error("consented replacement did not happen")
	}

	// Uninstall removes only managed things.
	out.Reset()
	gittest.Run(t, home, "config", "--global", "alias.recent", "!echo mine")
	if err := in.Uninstall(ctx, Options{Aliases: true, Completion: true, Agents: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fishPath); err == nil {
		t.Error("completion file not removed")
	}
	if _, err := os.Stat(in.SkillPath()); err == nil {
		t.Error("skill not removed")
	}
	if v := gittest.Run(t, home, "config", "--global", "alias.recent"); v != "!echo mine" {
		t.Errorf("user alias touched: %q", v)
	}
	if _, err := exec.New().Run(ctx, exec.Cmd{Name: "git", Args: []string{"config", "--global", "alias.ss"}}); err == nil {
		t.Error("managed alias not removed")
	}
	var removed bool
	for _, c := range f.CallsTo("claude") {
		if strings.Join(c.Args, " ") == "mcp remove --scope user git-stack" {
			removed = true
		}
	}
	if !removed || !strings.Contains(out.String(), "Claude Code  unregistered") {
		t.Errorf("agent not unregistered:\n%s", out.String())
	}
}

func gitconfigDefaults() []string {
	return []string{"create", "modify", "restack", "submit", "sync", "up", "down", "top", "bottom", "c", "m", "rs", "ss", "u", "d", "t", "b", "co"}
}

func TestSkillOptInIsAsked(t *testing.T) {
	f := exectest.New()
	var out bytes.Buffer
	in := newInstaller(t, f, &out)
	in.Prompter = yesPrompter{answer: true}
	if err := in.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(in.SkillPath()); err != nil {
		t.Error("skill should be installed after a yes")
	}
	// Non-interactive without an explicit flag: skipped.
	in2 := newInstaller(t, exectest.New(), &out)
	if err := in2.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(in2.SkillPath()); err == nil {
		t.Error("skill must not be installed without consent")
	}
	if !slices.Contains([]string{SkillContent()}, SkillContent()) {
		t.Error("unreachable")
	}
	_ = agents.ServerName
}

// bufReport collects the installer's report the way the piped reporter
// would print it.
type bufReport struct{ w io.Writer }

func (b bufReport) Success(format string, args ...any) { fmt.Fprintf(b.w, "ok: "+format+"\n", args...) }
func (b bufReport) Info(format string, args ...any)    { fmt.Fprintf(b.w, "  "+format+"\n", args...) }
func (b bufReport) Warn(format string, args ...any)    { fmt.Fprintf(b.w, "note: "+format+"\n", args...) }
