// Package version works out which git-stack this binary is.
//
// Three sources are tried in order: the value goreleaser stamps in with
// ldflags at release time, the module version recorded by `go install
// github.com/DomBlack/git-stack@vX.Y.Z`, and finally the VCS details that
// `go build` records for a checkout, so a dev build still says which commit
// it came from.
package version

import (
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

// version is set by the linker for release builds (see .goreleaser.yaml).
// goreleaser passes the tag without its "v"; Resolve adds it back.
var version = ""

// Info describes the running binary.
type Info struct {
	// Version is "vX.Y.Z" for a release, a pseudo version for `go install
	// @main`, or "dev" for a local build.
	Version string `json:"version"`
	// Release is true when Version names a published release that
	// `git stack update` can compare against.
	Release bool `json:"release"`
	// Commit is the short VCS revision when known.
	Commit string `json:"commit,omitempty"`
	// Date is the commit date (YYYY-MM-DD) when known.
	Date string `json:"date,omitempty"`
	// Dirty is true when the checkout had uncommitted changes.
	Dirty bool `json:"dirty,omitempty"`

	GoVersion string `json:"go"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Current resolves the running binary's version.
func Current() Info {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	return Resolve(version, bi)
}

// Resolve builds an Info from the linker value and the build info. It is
// split out from Current so tests can feed in fixed inputs.
func Resolve(ldflags string, bi *debug.BuildInfo) Info {
	info := Info{Version: "dev", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if bi != nil {
		if bi.GoVersion != "" {
			info.GoVersion = bi.GoVersion
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Commit = shortCommit(s.Value)
			case "vcs.time":
				if len(s.Value) >= 10 {
					info.Date = s.Value[:10]
				}
			case "vcs.modified":
				info.Dirty = s.Value == "true"
			}
		}
	}

	mainVersion := ""
	if bi != nil && bi.Main.Version != "(devel)" {
		mainVersion = bi.Main.Version
	}
	switch {
	case ldflags != "":
		info.Version = "v" + strings.TrimPrefix(ldflags, "v")
		info.Release = true
	case mainVersion != "" && !isPseudo(mainVersion):
		// go install github.com/DomBlack/git-stack@vX.Y.Z
		info.Version = mainVersion
		info.Release = true
	case info.Commit != "":
		// go build in a checkout. Go 1.24+ also stamps a pseudo version
		// (v0.0.0-<time>-<commit>+dirty) into Main.Version; the VCS details
		// say the same thing more readably, so prefer them.
	case mainVersion != "":
		// go install @main or @<commit>: a pseudo version with no VCS details.
		info.Version = mainVersion
		if i := strings.LastIndex(mainVersion, "-"); i >= 0 {
			info.Commit = strings.TrimSuffix(mainVersion[i+1:], "+dirty")
		}
	}
	return info
}

// String is the human form: "v1.2.3", or "dev (3a9f2c1, 2026-09-30, dirty)".
func (i Info) String() string {
	if i.Version != "dev" {
		return i.Version
	}
	var parts []string
	if i.Commit != "" {
		parts = append(parts, i.Commit)
	}
	if i.Date != "" {
		parts = append(parts, i.Date)
	}
	if i.Dirty {
		parts = append(parts, "dirty")
	}
	if len(parts) == 0 {
		return "dev"
	}
	return "dev (" + strings.Join(parts, ", ") + ")"
}

// Compare orders two versions: negative when a is older than b, zero when
// equal, positive when newer. Anything that is not vX.Y.Z (a dev build, a
// pseudo version) sorts before every real release.
func Compare(a, b string) int {
	an, aok := parse(a)
	bn, bok := parse(b)
	switch {
	case !aok && !bok:
		return strings.Compare(a, b)
	case !aok:
		return -1
	case !bok:
		return 1
	}
	for i := range 3 {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if strings.ContainsAny(v, "-+") {
		return out, false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func isPseudo(v string) bool {
	_, ok := parse(v)
	return !ok
}

func shortCommit(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}
