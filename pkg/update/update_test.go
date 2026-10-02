package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/version"
)

// fakeGitHub serves the releases API and the release assets for one tag.
type fakeGitHub struct {
	t        *testing.T
	tag      string
	archive  []byte
	checksum string // overrides the real sha256 when set (to test mismatches)
	status   int    // non-zero forces this status on the API call
	srv      *httptest.Server
	hits     []string
}

func newFakeGitHub(t *testing.T, tag, binary string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{t: t, tag: tag, archive: tarGz(t, map[string]string{"git-stack": binary, "LICENSE": "MIT"})}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/DomBlack/git-stack/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		f.hits = append(f.hits, r.URL.Path)
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		ver := strings.TrimPrefix(tag, "v")
		fmt.Fprintf(w, `{"tag_name": %q, "assets": [
			{"name": "git-stack_%s_linux_amd64.tar.gz", "browser_download_url": "%s/dl/other.tar.gz"},
			{"name": "git-stack_%s_darwin_arm64.tar.gz", "browser_download_url": "%s/dl/bin.tar.gz"},
			{"name": "checksums.txt", "browser_download_url": "%s/dl/checksums.txt"}]}`,
			tag, ver, f.srv.URL, ver, f.srv.URL, f.srv.URL)
	})
	mux.HandleFunc("/dl/bin.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		f.hits = append(f.hits, r.URL.Path)
		_, _ = w.Write(f.archive)
	})
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		f.hits = append(f.hits, r.URL.Path)
		sum := f.checksum
		if sum == "" {
			h := sha256.Sum256(f.archive)
			sum = hex.EncodeToString(h[:])
		}
		ver := strings.TrimPrefix(tag, "v")
		fmt.Fprintf(w, "%s  git-stack_%s_linux_amd64.tar.gz\n%s  git-stack_%s_darwin_arm64.tar.gz\n", strings.Repeat("0", 64), ver, sum, ver)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) client() *Client {
	return &Client{HTTP: f.srv.Client(), BaseURL: f.srv.URL, Repo: "DomBlack/git-stack", UserAgent: "git-stack-test"}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func target(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "git-stack")
	if err := os.WriteFile(p, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func release(v string) version.Info { return version.Info{Version: v, Release: true} }

func TestRunUpdatesAnOlderRelease(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	tgt := target(t)
	res, err := gh.client().Run(context.Background(), Options{Current: release("v1.2.0"), Target: tgt, GOOS: "darwin", GOARCH: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusUpdated || res.Latest != "v1.3.0" || res.Current != "v1.2.0" || res.Target != tgt {
		t.Errorf("Run() = %+v", res)
	}
	got, err := os.ReadFile(tgt)
	if err != nil || string(got) != "new binary" {
		t.Errorf("binary = %q, %v", got, err)
	}
	fi, _ := os.Stat(tgt)
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("binary is not executable: %v", fi.Mode())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(tgt), ".git-stack*")); len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestRunUpToDateAndAheadDoNothing(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	tgt := target(t)
	for _, tc := range []struct {
		cur  string
		want Status
	}{{"v1.3.0", StatusUpToDate}, {"v1.4.0", StatusAhead}} {
		res, err := gh.client().Run(context.Background(), Options{Current: release(tc.cur), Target: tgt, GOOS: "darwin", GOARCH: "arm64"})
		if err != nil || res.Status != tc.want {
			t.Errorf("%s: Run() = %+v, %v; want %v", tc.cur, res, err, tc.want)
		}
	}
	if got, _ := os.ReadFile(tgt); string(got) != "old binary" {
		t.Errorf("binary should be untouched, got %q", got)
	}
	for _, h := range gh.hits {
		if strings.HasPrefix(h, "/dl/") {
			t.Errorf("nothing should be downloaded, but %s was", h)
		}
	}
}

func TestRunCheckOnlyReportsAvailable(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	tgt := target(t)
	res, err := gh.client().Run(context.Background(), Options{Current: release("v1.2.0"), Target: tgt, GOOS: "darwin", GOARCH: "arm64", Check: true})
	if err != nil || res.Status != StatusAvailable {
		t.Fatalf("Run() = %+v, %v", res, err)
	}
	if got, _ := os.ReadFile(tgt); string(got) != "old binary" {
		t.Errorf("check must not replace the binary, got %q", got)
	}
}

func TestRunDevBuildNeedsForce(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	tgt := target(t)
	dev := version.Info{Version: "dev", Commit: "abc1234"}
	res, err := gh.client().Run(context.Background(), Options{Current: dev, Target: tgt, GOOS: "darwin", GOARCH: "arm64"})
	if err != nil || res.Status != StatusAvailable || !res.DevBuild {
		t.Fatalf("dev build without --force: %+v, %v", res, err)
	}
	if got, _ := os.ReadFile(tgt); string(got) != "old binary" {
		t.Errorf("dev build must not be replaced without force, got %q", got)
	}
	res, err = gh.client().Run(context.Background(), Options{Current: dev, Target: tgt, GOOS: "darwin", GOARCH: "arm64", Force: true})
	if err != nil || res.Status != StatusUpdated {
		t.Fatalf("dev build with --force: %+v, %v", res, err)
	}
	if got, _ := os.ReadFile(tgt); string(got) != "new binary" {
		t.Errorf("--force should replace the binary, got %q", got)
	}
}

func TestRunForceReinstallsSameVersion(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	tgt := target(t)
	res, err := gh.client().Run(context.Background(), Options{Current: release("v1.3.0"), Target: tgt, GOOS: "darwin", GOARCH: "arm64", Force: true})
	if err != nil || res.Status != StatusUpdated {
		t.Fatalf("Run() = %+v, %v", res, err)
	}
}

func TestRunChecksumMismatchLeavesBinaryAlone(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	gh.checksum = strings.Repeat("f", 64)
	tgt := target(t)
	_, err := gh.client().Run(context.Background(), Options{Current: release("v1.2.0"), Target: tgt, GOOS: "darwin", GOARCH: "arm64"})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected a checksum error, got %v", err)
	}
	if got, _ := os.ReadFile(tgt); string(got) != "old binary" {
		t.Errorf("binary must be untouched after a bad checksum, got %q", got)
	}
}

func TestRunNoAssetForPlatform(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	_, err := gh.client().Run(context.Background(), Options{Current: release("v1.2.0"), Target: target(t), GOOS: "windows", GOARCH: "arm64"})
	if !errors.Is(err, &stack.Error{Kind: stack.KindUnsupported}) {
		t.Fatalf("expected unsupported, got %v", err)
	}
}

func TestLatestErrors(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	gh.status = http.StatusNotFound
	_, err := gh.client().Latest(context.Background())
	var se *stack.Error
	if !errors.As(err, &se) || se.Kind != stack.KindAPIFailure || !strings.Contains(se.Msg, "no releases") {
		t.Errorf("404 should explain that nothing is published yet, got %v", err)
	}
	gh.status = http.StatusForbidden
	_, err = gh.client().Latest(context.Background())
	if !errors.Is(err, &stack.Error{Kind: stack.KindAPIFailure}) {
		t.Errorf("403 should be an API failure, got %v", err)
	}
	gh.srv.Close()
	_, err = gh.client().Latest(context.Background())
	if !errors.Is(err, &stack.Error{Kind: stack.KindAPIFailure}) {
		t.Errorf("connection failure should be an API failure, got %v", err)
	}
}

func TestRunUnwritableTargetHasNextSteps(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := gh.client().Run(context.Background(), Options{Current: release("v1.2.0"), Target: filepath.Join(dir, "git-stack"), GOOS: "darwin", GOARCH: "arm64"})
	var se *stack.Error
	if !errors.As(err, &se) || len(se.NextSteps) == 0 {
		t.Fatalf("expected a stack.Error with next steps, got %v", err)
	}
}

func TestAssetName(t *testing.T) {
	if got := assetName("v1.2.3", "darwin", "arm64"); got != "git-stack_1.2.3_darwin_arm64.tar.gz" {
		t.Errorf("assetName = %q", got)
	}
}
