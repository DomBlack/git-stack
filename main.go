// Command git-stack is a Graphite-style stacked-branch CLI. Installed on PATH,
// git exposes it as `git stack <cmd>`.
package main

import (
	"os"

	"github.com/DomBlack/git-stack/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
