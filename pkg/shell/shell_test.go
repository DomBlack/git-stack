package shell

import (
	"strings"
	"testing"
)

const fakeFish = "function __git_stack_perform_completion\n    set -l args (commandline -opc)\n    set -l lastArg x\nend\ncomplete -c git-stack -f\n"
const fakeZsh = "#compdef git-stack\ncompdef _git-stack git-stack\n_git-stack()\n{\n    requestComp=\"${words[1]} __complete ${words[2,-1]}\"\n}\nif [ \"$funcstack[1]\" = \"_git-stack\" ]; then\n    _git-stack\nfi\n"
const fakeBash = "__start_git-stack() { :; }\ncomplete -F __start_git-stack git-stack\n"

func TestCompose(t *testing.T) {
	fish, err := Compose(Fish, fakeFish)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fish, "set -l args (__git_stack_git_args (commandline -opc))") || !strings.Contains(fish, "function __git_stack_git_args") {
		t.Errorf("fish:\n%s", fish)
	}
	if _, err := Compose(Fish, "something else"); err == nil {
		t.Error("fish composition must fail loudly when cobra's script changes")
	}

	zsh, err := Compose(Zsh, fakeZsh)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(zsh, "#compdef git-stack\n#description ") || strings.Contains(zsh, "compdef __git_stack_cobra") ||
		!strings.Contains(zsh, "__git_stack_cobra()") || !strings.Contains(zsh, "_git-stack() {") || !strings.Contains(zsh, "_git_stack() {") ||
		!strings.Contains(zsh, "compdef _git-stack git-stack") || !strings.Contains(zsh, `"$funcstack[1]" == "_git-stack"`) {
		t.Errorf("zsh:\n%s", zsh)
	}
	if strings.Count(zsh, "#compdef") != 1 {
		t.Error("exactly one #compdef line")
	}

	bash, err := Compose(Bash, fakeBash)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bash, "complete -F __start_git-stack git-stack") || !strings.Contains(bash, "_git_stack() {") || !strings.Contains(bash, "__start_git-stack\n}") {
		t.Errorf("bash:\n%s", bash)
	}
}

func TestPathsAndDetect(t *testing.T) {
	e := Env{Home: "/h"}
	if e.InstallPath(Fish) != "/h/.config/fish/completions/git-stack.fish" || e.InstallPath(Bash) != "/h/.local/share/bash-completion/completions/git-stack" || e.InstallPath(Zsh) != "/h/.zfunc/_git-stack" {
		t.Errorf("paths: %s %s %s", e.InstallPath(Fish), e.InstallPath(Bash), e.InstallPath(Zsh))
	}
	e = Env{Home: "/h", XDGConfigHome: "/c", XDGDataHome: "/d"}
	if e.InstallPath(Fish) != "/c/fish/completions/git-stack.fish" || e.InstallPath(Bash) != "/d/bash-completion/completions/git-stack" {
		t.Errorf("xdg paths: %s %s", e.InstallPath(Fish), e.InstallPath(Bash))
	}
	if s, ok := Detect("/opt/homebrew/bin/fish"); !ok || s != Fish {
		t.Errorf("detect fish: %v %v", s, ok)
	}
	if _, ok := Detect("/bin/tcsh"); ok {
		t.Error("tcsh should not be detected")
	}
	if _, err := Parse("ZSH"); err != nil {
		t.Error("parse is case-insensitive")
	}
	for _, sh := range All {
		if e.Hint(sh) == "" {
			t.Errorf("no hint for %s", sh)
		}
	}
}
