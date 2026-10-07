package github

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/DomBlack/git-stack/pkg/git"
)

// PullRequestURL builds https://<host>/<owner>/<repo>/pull/<number> from the
// repository's remotes, picking the repository the way gh does: the one `gh
// repo set-default` chose (remote.<name>.gh-resolved is "base" for that
// remote's own repository, or an explicit OWNER/REPO on that remote's host,
// e.g. the upstream when only a fork is a remote), then upstream, github,
// origin, then the first remote. Remote URLs are resolved the way git
// resolves them (url.<base>.insteadOf applies), and nothing but local git
// config is read.
func (f *Forge) PullRequestURL(ctx context.Context, repo git.Repo, number int) (string, error) {
	base, err := f.repoURL(ctx, repo)
	if err != nil || base == "" {
		return "", err
	}
	return base + "/pull/" + strconv.Itoa(number), nil
}

// repoURL is the repository's web address, worked out once per checkout.
func (f *Forge) repoURL(ctx context.Context, repo git.Repo) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.repoURLs[repo.TopLevel]; ok {
		return u, nil
	}
	entries, err := git.New(f.run).ConfigGetRegexp(ctx, repo, git.ScopeMerged, `^remote\..*\.(url|gh-resolved)$`)
	if err != nil {
		return "", err
	}
	g := git.New(f.run)
	u := pickRemoteURL(entries, func(name, raw string) string {
		// git remote get-url applies insteadOf; fall back to the raw value
		// if git can't say (a remote with only a pushurl, say).
		if resolved, err := g.RemoteURL(ctx, repo, name); err == nil && resolved != "" {
			return resolved
		}
		return raw
	})
	if f.repoURLs == nil {
		f.repoURLs = map[string]string{}
	}
	f.repoURLs[repo.TopLevel] = u
	return u, nil
}

// pickRemoteURL chooses the remote and turns its fetch URL into a web URL.
// resolve maps a remote's name and configured url to the URL git actually
// uses; it's only called for remotes in the order they're tried.
func pickRemoteURL(entries []git.ConfigEntry, resolve func(name, raw string) string) string {
	type remote struct {
		name, url string
		// resolved is gh-resolved: "base", or the OWNER/REPO chosen
		// through this remote.
		resolved string
		order    int
	}
	byName := map[string]*remote{}
	var all []*remote
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Key, "remote.")
		if !ok {
			continue
		}
		i := strings.LastIndexByte(rest, '.')
		if i < 0 {
			continue
		}
		name, key := rest[:i], rest[i+1:]
		r := byName[name]
		if r == nil {
			r = &remote{name: name, order: len(all)}
			byName[name] = r
			all = append(all, r)
		}
		switch strings.ToLower(key) {
		case "url":
			if r.url == "" {
				r.url = e.Value
			}
		case "gh-resolved":
			r.resolved = strings.TrimSpace(e.Value)
		}
	}
	rank := func(r *remote) int {
		switch {
		case r.resolved != "":
			return 0
		case r.name == "upstream":
			return 1
		case r.name == "github":
			return 2
		case r.name == "origin":
			return 3
		}
		return 4
	}
	slices.SortStableFunc(all, func(a, b *remote) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a.order, b.order))
	})
	for _, r := range all {
		if r.url == "" {
			continue
		}
		web, ok := parseRemote(resolve(r.name, r.url))
		if !ok {
			continue
		}
		if r.resolved != "base" && isOwnerRepo(r.resolved) {
			// An explicit OWNER/REPO lives on the same host as the remote
			// it was chosen through.
			web.path = r.resolved
		}
		return web.String()
	}
	return ""
}

// isOwnerRepo reports whether s looks like OWNER/REPO.
func isOwnerRepo(s string) bool {
	owner, repo, ok := strings.Cut(s, "/")
	return ok && owner != "" && repo != "" && !strings.Contains(repo, "/")
}

// webRepo is where a repository lives on the web.
type webRepo struct {
	scheme, host, path string // path is OWNER/REPO
}

func (w webRepo) String() string { return w.scheme + "://" + w.host + "/" + w.path }

// webURL turns a git remote URL into the repository's web address, or "".
func webURL(remote string) string {
	w, ok := parseRemote(remote)
	if !ok {
		return ""
	}
	return w.String()
}

// parseRemote reads a git remote URL (https, http, ssh:// or scp-like
// git@host:owner/repo). An http(s) remote keeps its scheme and port, since
// the web UI is served there too; an ssh remote's port is the ssh
// transport's (ssh.github.com:443, host:22), so the web address is plain
// https on the host.
func parseRemote(remote string) (webRepo, bool) {
	w := webRepo{scheme: "https"}
	var path string
	switch {
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil {
			return webRepo{}, false
		}
		w.host, path = bracketIPv6(u.Hostname()), u.Path
		if u.Scheme == "http" || u.Scheme == "https" {
			w.scheme, w.host = u.Scheme, u.Host // Host keeps the port, never the user
		}
	default:
		// scp-like: [user@]host:owner/repo, the host bracketed if IPv6
		rest := remote
		if at := strings.IndexByte(rest, '@'); at >= 0 && at < strings.IndexAny(rest+":", ":[") {
			rest = rest[at+1:]
		}
		hostEnd := 0
		if strings.HasPrefix(rest, "[") {
			hostEnd = strings.Index(rest, "]") + 1
			if hostEnd == 0 {
				return webRepo{}, false
			}
		}
		i := strings.IndexByte(rest[hostEnd:], ':')
		if i < 0 {
			return webRepo{}, false
		}
		w.host, path = rest[:hostEnd+i], rest[hostEnd+i+1:]
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if w.host == "" || !isOwnerRepo(path) {
		return webRepo{}, false
	}
	// ssh.github.com is GitHub's ssh over 443 host; the web lives on github.com.
	if w.host == "ssh.github.com" {
		w.host = "github.com"
	}
	w.path = path
	return w, true
}

// bracketIPv6 puts the brackets url.Hostname strips back round an IPv6
// address, so it can go in a URL again.
func bracketIPv6(host string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}
