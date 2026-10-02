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
	mux.HandleFunc("/dl/bin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateCommand(t *testing.T) {
	srv := releaseServer(t, "v9.9.9", "new binary")
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
	if !strings.Contains(out, "ok: Updated git-stack ") || !strings.Contains(out, "to v9.9.9") || !strings.Contains(out, target) {
		t.Errorf("--force output = %q", out)
	}
	if got, _ := os.ReadFile(target); string(got) != "new binary" {
		t.Errorf("binary after --force = %q", got)
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
