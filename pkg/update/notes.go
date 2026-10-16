package update

import (
	"context"
	"encoding/json/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/version"
)

// ReleaseNote is what update shows for one release: the one line summary
// its notes open with and the pull requests listed under breaking changes.
type ReleaseNote struct {
	Tag     string
	Summary string
	URL     string
	// Breaking lists the pull requests in the release's "Breaking changes"
	// section, the one GitHub's generated notes give PRs labelled
	// breaking-change.
	Breaking []PullRequest
}

// PullRequest is one entry in a release's list of changes.
type PullRequest struct {
	Number int
	Title  string
	URL    string
}

// ReleasesURL is the page listing every release, for when more than one is
// being skipped.
func (c *Client) ReleasesURL() string {
	return "https://github.com/" + c.Repo + "/releases"
}

// Notes returns the releases newer than from, up to and including to, newest
// first: every version someone on from gets by updating to to. A from that
// isn't a release (a dev build, "") gets to alone, since there's no telling
// what it already has. Drafts and prereleases are left out.
func (c *Client) Notes(ctx context.Context, from, to string) ([]ReleaseNote, error) {
	url := strings.TrimSuffix(c.BaseURL, "/") + "/repos/" + c.Repo + "/releases?per_page=100"
	body, status, err := c.get(ctx, url, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, stack.Newf(stack.KindAPIFailure, "GitHub returned HTTP %d for %s", status, url)
	}
	var payload []struct {
		TagName    string `json:"tag_name"`
		Body       string `json:"body"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, stack.New(stack.KindAPIFailure, "could not parse the GitHub releases response").WithCause(err)
	}
	released := version.IsRelease(from)
	var notes []ReleaseNote
	for _, r := range payload {
		if r.Draft || r.Prerelease {
			continue
		}
		if released && (version.Compare(r.TagName, from) <= 0 || version.Compare(r.TagName, to) > 0) {
			continue
		}
		if !released && r.TagName != to {
			continue
		}
		summary, breaking := parseNotes(r.Body)
		notes = append(notes, ReleaseNote{Tag: r.TagName, Summary: summary, URL: r.HTMLURL, Breaking: breaking})
	}
	slices.SortFunc(notes, func(a, b ReleaseNote) int { return version.Compare(b.Tag, a.Tag) })
	return notes, nil
}

// rePRLine matches one line of GitHub's generated list:
// "* <title> by @<login> in https://github.com/<repo>/pull/<n>".
var rePRLine = regexp.MustCompile(`^\* (.+) by @\S+ in (\S+/pull/(\d+))\s*$`)

// parseNotes reads a release body as goreleaser writes it: the notes from the
// tag, whose first line is a one sentence summary, then GitHub's list of pull
// requests in sections. An older body that opens with a heading or a list
// has no summary.
func parseNotes(body string) (summary string, breaking []PullRequest) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "<!--") {
			continue
		}
		if !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "* ") {
			summary = l
		}
		break
	}
	in := false
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			in = strings.EqualFold(strings.TrimSpace(strings.TrimLeft(l, "#")), "Breaking changes")
			continue
		}
		if !in {
			continue
		}
		if m := rePRLine.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[3])
			breaking = append(breaking, PullRequest{Number: n, Title: m[1], URL: m[2]})
		}
	}
	return summary, breaking
}
