//go:build shellintegration

package shell_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// harness drives an interactive zsh through zsh/zpty: it configures fpath and
// compinit, types $1 followed by Tab, and prints whatever the pty echoed.
const zshHarness = `
zmodload zsh/zpty
zpty -b z zsh -f -i
sleep 0.3
zpty -w z 'fpath=('"$ZFUNC"' $fpath); autoload -Uz compinit; compinit -u; setopt nolistambiguous nolistbeep; zstyle ":completion:*" menu no; PROMPT="P> "'
sleep 0.7
drain() { typeset c; while zpty -r -t z c; do :; done }
drain
zpty -w -n z "$1"$'\t'
capture() { typeset c i; buf=""; for i in {1..15}; do sleep 0.2; while zpty -r -t z c; do buf+="$c"; done; done }
capture
print -r -- "$buf"
zpty -d z
`

var (
	reANSI  = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	reSpace = regexp.MustCompile(`[ \t]+`)
)

// TestZshGitIntegration checks that zsh's bundled _git dispatches
// `git stack …` and the aliases to the installed _git-stack, including the
// first Tab after autoload (which needs the funcstack guard in the hook).
func TestZshGitIntegration(t *testing.T) {
	if _, err := exec.New().Run(context.Background(), exec.Cmd{Name: "zsh", Args: []string{"-fc", "zmodload zsh/zpty"}}); err != nil {
		t.Skip("zsh with zpty not available")
	}
	e := setup(t)
	script := filepath.Join(e.home, "harness.zsh")
	if err := os.WriteFile(script, []byte(zshHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	complete := func(line string) string {
		res, _ := exec.New().Run(context.Background(), exec.Cmd{
			Name: "zsh", Args: []string{"-f", script, line}, Dir: e.repo,
			Env: []string{"ZFUNC=" + filepath.Dir(e.files["zsh"]), "TERM=xterm", "COLUMNS=160", "LINES=40"},
		})
		out := reANSI.ReplaceAllString(strings.ReplaceAll(res.Out(), "\r", ""), "")
		// zsh pads the listing into columns; compare on single spaces.
		return reSpace.ReplaceAllString(out, " ")
	}
	cases := []struct {
		line string
		want []string
		not  []string
	}{
		{"git stack ", []string{"checkout -- Switch to a branch", "create --", "submit --"}, nil},
		{"git-stack ", []string{"checkout -- Switch to a branch", "create --"}, nil},
		{"git co ", []string{"feat-b", "feat-a", "main"}, []string{"AGENTS"}},
		{"git co fe", []string{"feat-a", "feat-b"}, nil},
		// A single match is inserted directly on the line.
		{"git ss --dry", []string{"git ss --dry-run"}, nil},
		{"git u ", []string{"1"}, nil},
	}
	for _, tc := range cases {
		out := complete(tc.line)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%q: missing %q in output:\n%s", tc.line, w, out)
			}
		}
		for _, n := range tc.not {
			if strings.Contains(out, n) {
				t.Errorf("%q: unexpected %q in output:\n%s", tc.line, n, out)
			}
		}
	}
}
