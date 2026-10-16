package update

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// releasesServer serves GET /releases with the given releases, as GitHub
// lists them (newest first, but nothing should rely on that).
func releasesServer(t *testing.T, releases []map[string]any) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/DomBlack/git-stack/releases", func(w http.ResponseWriter, _ *http.Request) {
		b, err := json.Marshal(releases)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Client{HTTP: srv.Client(), BaseURL: srv.URL, Repo: "DomBlack/git-stack", UserAgent: "test"}
}

func releaseJSON(tag, body string) map[string]any {
	return map[string]any{"tag_name": tag, "body": body, "html_url": "https://github.com/DomBlack/git-stack/releases/tag/" + tag}
}

const v032 = `Submit can open drafts per branch, and sync is quicker on big repos

## New

Something long.

## What's Changed
### Breaking changes
* Make stack.submit.default apply per branch by @DomBlack in https://github.com/DomBlack/git-stack/pull/52
### Fixes
* Fix a thing by @someone in https://github.com/DomBlack/git-stack/pull/53

**Full Changelog**: https://github.com/DomBlack/git-stack/compare/v0.3.1...v0.3.2`

func TestNotesCoversEveryReleaseBeingSkipped(t *testing.T) {
	rc := releaseJSON("v0.4.0-rc.1", "Next one")
	rc["prerelease"] = true
	draft := releaseJSON("v0.3.3", "Not out yet")
	draft["draft"] = true
	c := releasesServer(t, []map[string]any{
		rc, draft,
		releaseJSON("v0.3.0", "Absorb, reorder, move and init"),
		releaseJSON("v0.3.2", v032),
		releaseJSON("v0.3.1", "Update keeps your install up to date\r\n\r\nMore."),
		releaseJSON("v0.2.3", "Older"),
	})

	notes, err := c.Notes(context.Background(), "v0.3.0", "v0.3.2")
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, n := range notes {
		tags = append(tags, n.Tag)
	}
	if !slices.Equal(tags, []string{"v0.3.2", "v0.3.1"}) {
		t.Fatalf("tags = %v, want v0.3.2 then v0.3.1", tags)
	}
	if notes[0].Summary != "Submit can open drafts per branch, and sync is quicker on big repos" || notes[1].Summary != "Update keeps your install up to date" {
		t.Errorf("summaries = %q, %q", notes[0].Summary, notes[1].Summary)
	}
	want := []PullRequest{{Number: 52, Title: "Make stack.submit.default apply per branch", URL: "https://github.com/DomBlack/git-stack/pull/52"}}
	if !slices.Equal(notes[0].Breaking, want) || len(notes[1].Breaking) != 0 {
		t.Errorf("breaking = %+v / %+v", notes[0].Breaking, notes[1].Breaking)
	}
	if notes[0].URL != "https://github.com/DomBlack/git-stack/releases/tag/v0.3.2" {
		t.Errorf("url = %q", notes[0].URL)
	}

	// A dev build can't say what it already has: only the release it gets.
	notes, err = c.Notes(context.Background(), "dev (3a9f2c1)", "v0.3.2")
	if err != nil || len(notes) != 1 || notes[0].Tag != "v0.3.2" {
		t.Errorf("dev build notes = %+v, %v", notes, err)
	}
}

func TestParseNotesWithoutWrittenNotes(t *testing.T) {
	// A body that is only goreleaser's old commit list has no summary.
	summary, breaking := parseNotes("## Changelog\n* 6dd5243 Add a leased, atomic git push\n")
	if summary != "" || breaking != nil {
		t.Errorf("parseNotes = %q, %v", summary, breaking)
	}
	summary, _ = parseNotes("<!-- Release notes generated using configuration in .github/release.yml -->\n\nFirst line\n")
	if summary != "First line" {
		t.Errorf("summary after a comment = %q", summary)
	}
}

func TestNotesErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, Repo: "DomBlack/git-stack", UserAgent: "test"}
	if _, err := c.Notes(context.Background(), "v0.3.0", "v0.3.1"); err == nil {
		t.Error("a 404 should be an error")
	}
}
