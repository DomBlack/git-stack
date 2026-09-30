package git_test

import "github.com/DomBlack/git-stack/pkg/exec"

func gitCmd(dir string, args ...string) exec.Cmd {
	return exec.Cmd{Name: "git", Args: args, Dir: dir}
}
