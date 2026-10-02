package version

import (
	"runtime/debug"
	"testing"
)

func buildInfo(mainVersion string, settings map[string]string) *debug.BuildInfo {
	bi := &debug.BuildInfo{GoVersion: "go1.27.1"}
	bi.Main.Version = mainVersion
	for k, v := range settings {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return bi
}

func TestResolve(t *testing.T) {
	vcs := map[string]string{
		"vcs.revision": "3a9f2c1d4e5f60718293a4b5c6d7e8f901234567",
		"vcs.time":     "2026-09-30T16:13:24Z",
		"vcs.modified": "true",
	}
	cases := []struct {
		name    string
		ldflags string
		bi      *debug.BuildInfo
		want    Info
		str     string
	}{
		{
			name:    "goreleaser build: ldflags win and get a v prefix",
			ldflags: "1.2.3",
			bi:      buildInfo("(devel)", vcs),
			want:    Info{Version: "v1.2.3", Release: true, Commit: "3a9f2c1", Date: "2026-09-30", Dirty: true},
			str:     "v1.2.3",
		},
		{
			name:    "go install @version: module version is the release",
			ldflags: "",
			bi:      buildInfo("v0.4.0", nil),
			want:    Info{Version: "v0.4.0", Release: true},
			str:     "v0.4.0",
		},
		{
			name:    "go build from a checkout (Go 1.24+ stamps a pseudo version too): dev with vcs details",
			ldflags: "",
			bi:      buildInfo("v0.0.0-20260930161635-3a9f2c1d4e5f+dirty", vcs),
			want:    Info{Version: "dev", Commit: "3a9f2c1", Date: "2026-09-30", Dirty: true},
			str:     "dev (3a9f2c1, 2026-09-30, dirty)",
		},
		{
			name:    "older toolchain: (devel) with vcs details",
			ldflags: "",
			bi:      buildInfo("(devel)", vcs),
			want:    Info{Version: "dev", Commit: "3a9f2c1", Date: "2026-09-30", Dirty: true},
			str:     "dev (3a9f2c1, 2026-09-30, dirty)",
		},
		{
			name:    "clean checkout",
			ldflags: "",
			bi:      buildInfo("(devel)", map[string]string{"vcs.revision": "abcdef0123", "vcs.time": "2026-09-30T16:13:24Z", "vcs.modified": "false"}),
			want:    Info{Version: "dev", Commit: "abcdef0", Date: "2026-09-30"},
			str:     "dev (abcdef0, 2026-09-30)",
		},
		{
			name:    "no build info at all",
			ldflags: "",
			bi:      nil,
			want:    Info{Version: "dev"},
			str:     "dev",
		},
		{
			name:    "pseudo version from go install @main is not a release",
			ldflags: "",
			bi:      buildInfo("v0.0.0-20260930161324-3a9f2c1d4e5f", nil),
			want:    Info{Version: "v0.0.0-20260930161324-3a9f2c1d4e5f", Commit: "3a9f2c1d4e5f"},
			str:     "v0.0.0-20260930161324-3a9f2c1d4e5f",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.ldflags, tc.bi)
			// GoVersion/OS/Arch come from the running toolchain; compare the rest.
			got.GoVersion, got.OS, got.Arch = "", "", ""
			if got != tc.want {
				t.Errorf("Resolve() = %+v, want %+v", got, tc.want)
			}
			if s := Resolve(tc.ldflags, tc.bi).String(); s != tc.str {
				t.Errorf("String() = %q, want %q", s, tc.str)
			}
		})
	}
}

func TestCurrentFillsToolchainFields(t *testing.T) {
	got := Current()
	if got.GoVersion == "" || got.OS == "" || got.Arch == "" {
		t.Errorf("Current() = %+v; toolchain fields missing", got)
	}
	if got.Version == "" {
		t.Error("Version must never be empty")
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.3", "v1.2.4", -1},
		{"v1.10.0", "v1.9.9", 1},
		{"v2.0.0", "v1.99.99", 1},
		{"1.2.3", "v1.2.3", 0},
		{"dev", "v1.2.3", -1},
		{"v1.2.3", "dev", 1},
	}
	for _, tc := range cases {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
