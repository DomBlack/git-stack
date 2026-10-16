package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/update"
	"github.com/DomBlack/git-stack/pkg/version"
)

// releaseServer serves one release with a darwin/arm64 and linux/amd64 (and
// the running platform's) archive containing `body` as the binary.
func releaseServer(t *testing.T, tag, body string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "git-stack", Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	name := fmt.Sprintf("git-stack_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/DomBlack/git-stack/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q, "assets": [{"name": %q, "browser_download_url": %q}, {"name": "checksums.txt", "browser_download_url": %q}]}`,
			tag, name, srv.URL+"/dl/bin", srv.URL+"/dl/sums")
	})
	// The release list update shows notes from: this release, with a
	// breaking change, and the one before it.
	mux.HandleFunc("/repos/DomBlack/git-stack/releases", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"tag_name": %q, "html_url": "https://github.com/DomBlack/git-stack/releases/tag/%s",
			"body": "Summary of %s\n\n## What's Changed\n### Breaking changes\n* Change a default by @DomBlack in https://github.com/DomBlack/git-stack/pull/7\n"},
			{"tag_name": "v9.9.8", "html_url": "https://github.com/DomBlack/git-stack/releases/tag/v9.9.8", "body": "Summary of v9.9.8"},
			{"tag_name": "v1.0.0", "html_url": "https://github.com/DomBlack/git-stack/releases/tag/v1.0.0", "body": "Summary of v1.0.0"}]`, tag, tag, tag)
	})
	mux.HandleFunc("/dl/bin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateCommand(t *testing.T) {
	// The "release" is a script standing in for the new binary, so the
	// hand-off to its install --refresh can be seen.
	newBinary := "#!/bin/sh\necho \"ok: new binary ran $*\"\n"
	srv := releaseServer(t, "v9.9.9", newBinary)
	target := filepath.Join(t.TempDir(), "git-stack")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	newCLI := func() *cli {
		return &cli{
			streams:      testStreams(nil),
			updateTarget: target,
			updateClient: &update.Client{HTTP: srv.Client(), BaseURL: srv.URL, Repo: "DomBlack/git-stack", UserAgent: "test"},
		}
	}
	runUpdate := func(args ...string) (string, error) {
		c := newCLI()
		root := newRootCmd(c)
		root.SetArgs(append([]string{"update"}, args...))
		err := root.Execute()
		// stdout then stderr: notices (dev build, update available) go to stderr.
		return c.streams.Out.(*bytes.Buffer).String() + c.streams.Err.(*bytes.Buffer).String(), err
	}

	// The test binary is a dev build, so a plain update only reports.
	out, err := runUpdate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dev build") || !strings.Contains(out, "v9.9.9") || !strings.Contains(out, "--force") {
		t.Errorf("dev build output = %q", out)
	}
	// A dev build only hears about the release it would get.
	if !strings.Contains(out, "  v9.9.9  Summary of v9.9.9\n") || strings.Contains(out, "v9.9.8") ||
		!strings.Contains(out, "Release notes: https://github.com/DomBlack/git-stack/releases/tag/v9.9.9") {
		t.Errorf("dev build notes = %q", out)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("binary replaced without --force: %q", got)
	}

	out, err = runUpdate("--check")
	if err != nil || !strings.Contains(out, "v9.9.9") {
		t.Errorf("--check = %q, %v", out, err)
	}

	out, err = runUpdate("--force")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Downloading git-stack v9.9.9...") || !strings.Contains(out, "ok: Updated git-stack ") || !strings.Contains(out, "to v9.9.9") || !strings.Contains(out, target) {
		t.Errorf("--force output = %q", out)
	}
	if got, _ := os.ReadFile(target); string(got) != newBinary {
		t.Errorf("binary after --force = %q", got)
	}
	// The new binary then refreshes what install set up, and its output is
	// passed on after ours.
	updated := strings.Index(out, "ok: Updated git-stack ")
	ran := strings.Index(out, "ok: new binary ran install --refresh --no-interactive")
	if ran < 0 || ran < updated {
		t.Errorf("install --refresh not handed to the new binary after the update:\n%s", out)
	}
}

func TestUpdateCommandRendersAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := &cli{
		streams:      testStreams(nil),
		updateTarget: filepath.Join(t.TempDir(), "git-stack"),
		updateClient: &update.Client{HTTP: srv.Client(), BaseURL: srv.URL, Repo: "DomBlack/git-stack", UserAgent: "test"},
	}
	root := newRootCmd(c)
	root.SetArgs([]string{"update"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no releases") {
		t.Fatalf("expected the no releases error, got %v", err)
	}
	var buf bytes.Buffer
	printError(&buf, err)
	if !strings.Contains(buf.String(), "go install github.com/DomBlack/git-stack@latest") {
		t.Errorf("next steps missing: %q", buf.String())
	}
}

func TestUpdateCommandReportsAFailedRefresh(t *testing.T) {
	srv := releaseServer(t, "v9.9.9", "#!/bin/sh\necho 'unknown flag: --refresh' >&2\nexit 1\n")
	target := filepath.Join(t.TempDir(), "git-stack")
	c := &cli{
		streams:      testStreams(nil),
		updateTarget: target,
		updateClient: &update.Client{HTTP: srv.Client(), BaseURL: srv.URL, Repo: "DomBlack/git-stack", UserAgent: "test"},
	}
	root := newRootCmd(c)
	root.SetArgs([]string{"update", "--force"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "refreshing what git stack install set up failed") {
		t.Fatalf("expected the refresh error, got %v", err)
	}
	var buf bytes.Buffer
	printError(&buf, err)
	if !strings.Contains(buf.String(), "unknown flag: --refresh") || !strings.Contains(buf.String(), "git stack install") {
		t.Errorf("error output = %q", buf.String())
	}
}

func TestUpdateShowsEveryReleaseBeingSkipped(t *testing.T) {
	srv := releaseServer(t, "v9.9.9", "new binary")
	c := &cli{
		streams:      testStreams(nil),
		current:      version.Info{Version: "v1.0.0", Release: true},
		updateTarget: filepath.Join(t.TempDir(), "git-stack"),
		updateClient: &update.Client{HTTP: srv.Client(), BaseURL: srv.URL, Repo: "DomBlack/git-stack", UserAgent: "test"},
	}
	root := newRootCmd(c)
	root.SetArgs([]string{"update", "--check"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := c.streams.Out.(*bytes.Buffer).String()
	want := "note: Update available: v9.9.9 is out, you have v1.0.0\n" +
		"  v9.9.9  Summary of v9.9.9\n" +
		"  v9.9.8  Summary of v9.9.8\n" +
		"  breaking change in v9.9.9: Change a default #7\n" +
		"  Release notes: https://github.com/DomBlack/git-stack/releases\n" +
		"  - git stack update installs it\n"
	if out != want {
		t.Errorf("update --check output:\n%s\nwant:\n%s", out, want)
	}
}

func TestUpdateGoesAheadWithoutTheNotes(t *testing.T) {
	srv := releaseServer(t, "v9.9.9", "new binary")
	// Only the latest release endpoint works; the release list fails.
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/DomBlack/git-stack/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	broken := httptest.NewServer(mux)
	t.Cleanup(broken.Close)
	c := &cli{
		streams:      testStreams(nil),
		current:      version.Info{Version: "v1.0.0", Release: true},
		updateTarget: filepath.Join(t.TempDir(), "git-stack"),
		updateClient: &update.Client{HTTP: broken.Client(), BaseURL: broken.URL, Repo: "DomBlack/git-stack", UserAgent: "test"},
	}
	root := newRootCmd(c)
	root.SetArgs([]string{"update", "--check"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := c.streams.Out.(*bytes.Buffer).String() + c.streams.Err.(*bytes.Buffer).String()
	if !strings.Contains(out, "Update available: v9.9.9") || !strings.Contains(out, "Couldn't fetch what changed; the release notes are at https://github.com/DomBlack/git-stack/releases") {
		t.Errorf("output without notes = %q", out)
	}
}
