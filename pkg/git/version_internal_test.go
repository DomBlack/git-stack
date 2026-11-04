package git

import "testing"

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]string{
		"git version 2.47.1":                 "2.47.1",
		"git version 2.39.5 (Apple Git-154)": "2.39.5",
		"git version 2.45.2.windows.1":       "2.45.2",
		"something else":                     "",
	} {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
