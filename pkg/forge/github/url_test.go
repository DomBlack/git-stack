package github

import (
	"context"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestWebURL(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"https://github.com/o/r.git", "https://github.com/o/r"},
		{"https://github.com/o/r", "https://github.com/o/r"},
		{"https://user@ghe.example.com:8443/o/r.git", "https://ghe.example.com:8443/o/r"},
		{"http://git.internal:8080/o/r", "http://git.internal:8080/o/r"},
		{"ssh://git@ghe.example.com:2222/o/r.git", "https://ghe.example.com/o/r"},
		{"ssh://git@github.com:22/o/r.git", "https://github.com/o/r"},
		{"ssh://git@[2001:db8::1]:2222/o/r.git", "https://[2001:db8::1]/o/r"},
		{"https://[2001:db8::1]:8443/o/r.git", "https://[2001:db8::1]:8443/o/r"},
		{"git@[2001:db8::1]:o/r.git", "https://[2001:db8::1]/o/r"},
		{"git@github.com:o/r.git", "https://github.com/o/r"},
		{"github.com:o/r", "https://github.com/o/r"},
		{"ssh://git@github.com/o/r.git", "https://github.com/o/r"},
		{"ssh://git@ssh.github.com:443/o/r.git", "https://github.com/o/r"},
		{"/srv/git/r.git", ""},
		{"../local", ""},
		{"https://github.com/just-owner", ""},
	} {
		if got := webURL(tc.remote); got != tc.want {
			t.Errorf("webURL(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}

func TestPickRemoteURLFollowsGh(t *testing.T) {
	e := func(k, v string) git.ConfigEntry { return git.ConfigEntry{Key: k, Value: v} }
	for _, tc := range []struct {
		name    string
		entries []git.ConfigEntry
		want    string
	}{
		{"none", nil, ""},
		{"origin only", []git.ConfigEntry{e("remote.origin.url", "git@github.com:me/r.git")}, "https://github.com/me/r"},
		{"upstream beats origin", []git.ConfigEntry{
			e("remote.origin.url", "git@github.com:me/r.git"),
			e("remote.upstream.url", "git@github.com:org/r.git"),
		}, "https://github.com/org/r"},
		{"gh repo set-default wins", []git.ConfigEntry{
			e("remote.upstream.url", "git@github.com:org/r.git"),
			e("remote.fork.url", "git@github.com:me/r.git"),
			e("remote.fork.gh-resolved", "base"),
		}, "https://github.com/me/r"},
		{"explicit OWNER/REPO through a fork-only remote", []git.ConfigEntry{
			e("remote.origin.url", "git@github.com:me/r.git"),
			e("remote.origin.gh-resolved", "org/r"),
		}, "https://github.com/org/r"},
		{"explicit OWNER/REPO beats upstream and keeps the remote's host", []git.ConfigEntry{
			e("remote.upstream.url", "git@github.com:other/r.git"),
			e("remote.fork.url", "https://ghe.example.com:8443/me/r.git"),
			e("remote.fork.gh-resolved", "team/r"),
		}, "https://ghe.example.com:8443/team/r"},
		{"a nonsense gh-resolved falls back to the remote itself", []git.ConfigEntry{
			e("remote.origin.url", "git@github.com:me/r.git"),
			e("remote.origin.gh-resolved", "not-a-repo"),
		}, "https://github.com/me/r"},
		{"skips remotes that aren't forges", []git.ConfigEntry{
			e("remote.origin.url", "/srv/git/r.git"),
			e("remote.gh.url", "https://github.com/o/r"),
		}, "https://github.com/o/r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickRemoteURL(tc.entries, func(_, raw string) string { return raw }); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Remote URLs go through git, so url.<base>.insteadOf shorthands resolve
// to the real host, and nothing but local config is read.
func TestPullRequestURLFollowsInsteadOf(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "remote", "add", "origin", "gh:o/r")
	gittest.Run(t, dir, "config", "url.git@github.com:.insteadOf", "gh:")
	f := New(exec.New())
	got, err := f.PullRequestURL(context.Background(), git.Repo{TopLevel: dir}, 5)
	if err != nil || got != "https://github.com/o/r/pull/5" {
		t.Errorf("got %q, %v", got, err)
	}
}
