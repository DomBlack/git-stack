package app

import (
	"strings"
	"testing"
)

// Next steps that are commands quote what they name for the shell, and say
// what to do instead when a name can't be pasted safely at all.
func TestCommandStepsQuotePaths(t *testing.T) {
	t.Setenv("HOME", "/home/dom")
	for _, tc := range []struct{ got, want string }{
		{gitAddStep([]string{"a.go", "my notes.txt", "it's"}), `git add a.go 'my notes.txt' 'it'\''s'`},
		{gitAddStep([]string{"a.go", "evil\nrm -rf ~"}), "git add each of those files once it's resolved"},
		{cdStep("/home/dom/src/app"), "cd ~/src/app"},
		{cdStep("/home/dom/my app"), "cd ~/'my app'"},
		{cdStep("/srv/a\nb"), `change to the worktree at "/srv/a\nb"`},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
	lockStep := func(lock string) string {
		_, steps := NotUpdated{Name: "main", Reason: NotUpdatedLocked, LockKind: "ref", Lock: lock}.explain()
		return steps[len(steps)-1]
	}
	if got := lockStep("/home/dom/my repo/.git/refs/heads/main.lock"); !strings.Contains(got, "rm ~/'my repo/.git/refs/heads/main.lock', then") {
		t.Errorf("lock step = %q", got)
	}
	if got := lockStep("/srv/a\nb/.git/index.lock"); strings.Contains(got, "rm ") || !strings.Contains(got, `"/srv/a\nb/.git/index.lock"`) {
		t.Errorf("lock step with a newline = %q", got)
	}
}
