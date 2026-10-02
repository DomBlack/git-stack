package cmd

import (
	"encoding/json/v2"
	"runtime"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/version"
)

func TestVersionCommand(t *testing.T) {
	out, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	lines := nonEmpty(out)
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "git-stack ") || !strings.Contains(lines[1], runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("version output = %q", out)
	}
	if !strings.HasPrefix(lines[0], "git-stack "+version.Current().String()) {
		t.Errorf("version line %q does not match version.Current()", lines[0])
	}

	out, err = run(t, "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var info version.Info
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", err, out)
	}
	if info.Version == "" || info.OS != runtime.GOOS {
		t.Errorf("--json = %+v", info)
	}

	// --version keeps working and agrees with the command.
	out, err = run(t, "--version")
	if err != nil || strings.TrimSpace(out) != "git-stack "+version.Current().String() {
		t.Errorf("--version = %q, %v", out, err)
	}
}
