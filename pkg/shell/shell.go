// Package shell composes the completion scripts git-stack installs: cobra's
// generated script for the git-stack binary plus the hooks that make
// `git stack <TAB>` and git aliases such as `git co <TAB>` complete through
// the same engine.
package shell

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"
)

// Shell is a supported shell.
type Shell string

const (
	Bash Shell = "bash"
	Zsh  Shell = "zsh"
	Fish Shell = "fish"
)

// All lists the supported shells.
var All = []Shell{Bash, Zsh, Fish}

// Parse validates a shell name.
func Parse(name string) (Shell, error) {
	switch Shell(strings.ToLower(name)) {
	case Bash:
		return Bash, nil
	case Zsh:
		return Zsh, nil
	case Fish:
		return Fish, nil
	default:
		return "", fmt.Errorf("unsupported shell %q (expected bash, zsh or fish)", name)
	}
}

// Detect guesses the login shell from $SHELL.
func Detect(shellEnv string) (Shell, bool) {
	s, err := Parse(filepath.Base(shellEnv))
	return s, err == nil
}

//go:embed hooks/bash.sh
var bashHook string

//go:embed hooks/zsh.zsh
var zshHook string

//go:embed hooks/fish.fish
var fishHook string

// cobraZshFunc is cobra's generated function name; the zsh hook wraps it.
const cobraZshFunc = "_git-stack"

// Compose returns the script to install for sh, given cobra's generated
// script for the git-stack binary.
func Compose(sh Shell, cobraScript string) (string, error) {
	switch sh {
	case Bash:
		return strings.TrimRight(cobraScript, "\n") + "\n" + bashHook, nil
	case Zsh:
		body := strings.ReplaceAll(cobraScript, cobraZshFunc, "__git_stack_cobra")
		// Drop cobra's own #compdef / compdef lines; the hook registers ours.
		var lines []string
		for line := range strings.SplitSeq(body, "\n") {
			if strings.HasPrefix(line, "#compdef ") || strings.HasPrefix(line, "compdef ") {
				continue
			}
			lines = append(lines, line)
		}
		header := "#compdef git-stack\n#description stacked branches and stacked pull requests\n"
		return header + strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n" + zshHook, nil
	case Fish:
		const from = "set -l args (commandline -opc)"
		if !strings.Contains(cobraScript, from) {
			return "", fmt.Errorf("cobra's fish script changed shape; expected %q", from)
		}
		patched := strings.Replace(cobraScript, from, "set -l args (__git_stack_git_args (commandline -opc))", 1)
		return fishHook + patched, nil
	default:
		return "", fmt.Errorf("unsupported shell %q", sh)
	}
}

// Env is the subset of the environment path resolution needs.
type Env struct {
	Home          string
	XDGConfigHome string
	XDGDataHome   string
}

// InstallPath is where the composed script lives for sh.
func (e Env) InstallPath(sh Shell) string {
	config := e.XDGConfigHome
	if config == "" {
		config = filepath.Join(e.Home, ".config")
	}
	data := e.XDGDataHome
	if data == "" {
		data = filepath.Join(e.Home, ".local", "share")
	}
	switch sh {
	case Bash:
		return filepath.Join(data, "bash-completion", "completions", "git-stack")
	case Zsh:
		return filepath.Join(e.Home, ".zfunc", "_git-stack")
	case Fish:
		return filepath.Join(config, "fish", "completions", "git-stack.fish")
	default:
		return ""
	}
}

// Hint explains anything the user must do by hand after installing.
func (e Env) Hint(sh Shell) string {
	switch sh {
	case Bash:
		return "bash-completion 2 loads it on first use of `git stack`; without bash-completion, source the file from ~/.bashrc"
	case Zsh:
		return "add `fpath=(~/.zfunc $fpath)` before `compinit` in ~/.zshrc (once), then restart zsh"
	case Fish:
		return "fish picks it up automatically; restart the shell if a completion was already cached"
	default:
		return ""
	}
}
