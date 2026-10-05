//go:build shellintegration

package shell_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
	"github.com/DomBlack/git-stack/pkg/gitconfig"
	"github.com/DomBlack/git-stack/pkg/shell"
)

// env builds a git-stack binary, an isolated HOME with the managed aliases,
// a repository with a stack, and the composed completion scripts.
type env struct {
	t     *testing.T
	bin   string // directory holding git-stack
	home  string
	repo  string
	files map[shell.Shell]string
}

func setup(t *testing.T) *env {
	t.Helper()
	// Isolate moves HOME, and with no GOPATH set Go would derive its module
	// cache and build cache from the new HOME. The go build below would then
	// fill the temp dir with read-only module files that TempDir can't clean
	// up. Pin the caches to wherever they were before HOME moved.
	run := exec.New()
	ctx := context.Background()
	res, err := run.Run(ctx, exec.Cmd{Name: "go", Args: []string{"env", "GOPATH", "GOMODCACHE", "GOCACHE"}})
	if err != nil {
		t.Fatalf("go env: %v\n%s", err, res.Err())
	}
	goEnv := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	if len(goEnv) != 3 {
		t.Fatalf("unexpected go env output: %q", res.Stdout)
	}
	gittest.Isolate(t)
	t.Setenv("GOPATH", goEnv[0])
	t.Setenv("GOMODCACHE", goEnv[1])
	t.Setenv("GOCACHE", goEnv[2])
	home := os.Getenv("HOME")
	xdg := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if res, err := run.Run(ctx, exec.Cmd{Name: "go", Args: []string{"build", "-o", filepath.Join(bin, "git-stack"), "../.."}}); err != nil {
		t.Fatalf("build: %v\n%s", err, res.Err())
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	g := git.New(run)
	plan, err := gitconfig.New(g).Plan(ctx, gitconfig.DefaultAliases)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := gitconfig.New(g).Apply(ctx, plan, true); err != nil {
		t.Fatal(err)
	}

	repo := gittest.InitRepo(t)
	for _, b := range []string{"feat-a", "feat-b"} {
		gittest.Run(t, repo, "switch", "-q", "-c", b)
		gittest.Commit(t, repo, b+".txt", b, b)
	}
	gittest.Run(t, repo, "switch", "-q", "feat-a")
	gitDir := gittest.Run(t, repo, "rev-parse", "--absolute-git-dir")
	meta := `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"feat-a"},{"branch":"feat-b"}]}]}`
	if err := os.WriteFile(filepath.Join(gitDir, "gh-stack"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &env{t: t, bin: bin, home: home, repo: repo, files: map[shell.Shell]string{}}
	senv := shell.Env{Home: home, XDGConfigHome: xdg, XDGDataHome: filepath.Join(home, ".local", "share")}
	for _, sh := range shell.All {
		res, err := run.Run(ctx, exec.Cmd{Name: filepath.Join(bin, "git-stack"), Args: []string{"completion", string(sh)}})
		if err != nil {
			t.Fatalf("completion %s: %v", sh, err)
		}
		path := senv.InstallPath(sh)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, res.Stdout, 0o644); err != nil {
			t.Fatal(err)
		}
		e.files[sh] = path
	}
	return e
}

func (e *env) sh(name string, args ...string) (string, string) {
	e.t.Helper()
	res, err := exec.New().Run(context.Background(), exec.Cmd{Name: name, Args: args, Dir: e.repo})
	if err != nil {
		var ee *exec.ExitError
		if !errorsAs(err, &ee) {
			e.t.Fatalf("%s %v: %v", name, args, err)
		}
	}
	return res.Out(), res.Err()
}

func errorsAs(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}

func hasLine(out, prefix string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func TestFishGitIntegration(t *testing.T) {
	if _, err := exec.New().Run(context.Background(), exec.Cmd{Name: "fish", Args: []string{"--version"}}); err != nil {
		t.Skip("fish not installed")
	}
	e := setup(t)
	cases := []struct {
		line string
		want []string
		not  []string
	}{
		{"git-stack ", []string{"checkout\t", "create\t", "submit\t"}, nil},
		{"git stack ", []string{"checkout\t", "create\t", "up\t"}, nil},
		{"git stack co ", []string{"feat-a\t", "feat-b\t", "main\t"}, nil},
		{"git co ", []string{"feat-a\t", "feat-b\t", "main\t"}, nil},
		{"git co fe", []string{"feat-a\t", "feat-b\t"}, []string{"main"}},
		{"git ss --", []string{"--dry-run\t", "--publish\t"}, []string{"checkout"}},
		{"git ss --dry", []string{"--dry-run\t"}, []string{"--publish"}},
		{"git u ", []string{"1\tfeat-b"}, nil},
		{"git c ", nil, []string{"checkout", "feat-a"}},
		{"git stack install --shell ", []string{"fish\t", "bash\t", "zsh\t"}, nil},
	}
	for _, tc := range cases {
		out, errOut := e.sh("fish", "-c", `complete -C "`+tc.line+`"`)
		for _, w := range tc.want {
			if !hasLine(out, w) {
				t.Errorf("%q: missing %q in:\n%s\n%s", tc.line, w, out, errOut)
			}
		}
		for _, n := range tc.not {
			if hasLine(out, n) {
				t.Errorf("%q: unexpected %q in:\n%s", tc.line, n, out)
			}
		}
	}
	// After completing an alias, a direct git-stack completion must not
	// inherit the alias tail (the hook clears fish's cached git context).
	script := `complete -C "git ss --" >/dev/null; complete -C "git-stack "`
	out, _ := e.sh("fish", "-c", script)
	if !hasLine(out, "checkout\t") || hasLine(out, "--dry-run") {
		t.Errorf("stale alias context leaked:\n%s", out)
	}
}

func TestBashGitIntegration(t *testing.T) {
	gitComp := ""
	for _, c := range []string{
		"/opt/homebrew/etc/bash_completion.d/git-completion.bash",
		"/usr/local/etc/bash_completion.d/git-completion.bash",
		"/usr/share/bash-completion/completions/git",
		"/usr/share/git-core/contrib/completion/git-completion.bash",
	} {
		if _, err := os.Stat(c); err == nil {
			gitComp = c
			break
		}
	}
	if gitComp == "" {
		t.Skip("git-completion.bash not found")
	}
	e := setup(t)
	complete := func(words ...string) string {
		line := strings.Join(words, " ")
		var b bytes.Buffer
		b.WriteString("source " + gitComp + "\n")
		b.WriteString("source " + e.files[shell.Bash] + "\n")
		b.WriteString("COMP_WORDS=(" + strings.Join(words, " ") + ")\n")
		b.WriteString("COMP_CWORD=" + itoa(len(words)-1) + "\n")
		b.WriteString("COMP_LINE=" + shellQuote(line) + "\nCOMP_POINT=${#COMP_LINE}\n")
		b.WriteString("__git_wrap__git_main 2>/dev/null\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n")
		out, _ := e.sh("bash", "-c", b.String())
		return out
	}
	if out := complete("git", "stack", "''"); !hasLine(out, "checkout") || !hasLine(out, "create") {
		t.Errorf("git stack <TAB>:\n%s", out)
	}
	if out := complete("git", "co", "''"); !hasLine(out, "feat-a") || !hasLine(out, "main") {
		t.Errorf("git co <TAB>:\n%s", out)
	}
	if out := complete("git", "ss", "--dry"); !hasLine(out, "--dry-run") || hasLine(out, "--publish") {
		t.Errorf("git ss --dry<TAB>:\n%s", out)
	}
	if out := complete("git", "continue", "--"); !hasLine(out, "--all") {
		t.Errorf("git continue --<TAB>:\n%s", out)
	}
	if out := complete("git", "u", "''"); !hasLine(out, "1") {
		t.Errorf("git u <TAB>:\n%s", out)
	}
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+n))) }

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
