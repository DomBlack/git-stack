package shell

import "testing"

func TestArgAndPath(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"src/app", "src/app", true},
		{"my notes.txt", "'my notes.txt'", true},
		{"it's", `'it'\''s'`, true},
		{"$HOME", "'$HOME'", true},
		{"a~b", "a~b", true},
		{"~x", "'~x'", true},
		{"", "''", true},
		{"a\nrm -rf b", "", false},
		{"a\rb", "", false},
		{"a\tb", "", false},
		{"a\x1b[2Jb", "", false},
	} {
		if got, ok := Arg(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("Arg(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"~/src/app", "~/src/app", true},
		{"~/my app", "~/'my app'", true},
		{"/srv/my app", "'/srv/my app'", true},
		{"~/a\nb", "~/", false},
	} {
		if got, ok := Path(tc.in); ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("Path(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
