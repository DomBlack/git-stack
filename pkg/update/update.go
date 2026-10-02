// Package update replaces the running git-stack binary with the latest
// GitHub release.
//
// It talks plain HTTPS to the GitHub releases API and the release assets, so
// it works without gh being logged in. Assets are the archives goreleaser
// publishes (git-stack_<ver>_<os>_<arch>.tar.gz plus checksums.txt); the
// archive is verified against checksums.txt before anything is written.
package update

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/DomBlack/git-stack/pkg/stack"
	"github.com/DomBlack/git-stack/pkg/version"
)

// DefaultRepo is the GitHub repository releases are published from.
const DefaultRepo = "DomBlack/git-stack"

// Client fetches releases. The zero value is not usable; use New.
type Client struct {
	HTTP      *http.Client
	BaseURL   string // GitHub API base, https://api.github.com
	Repo      string // owner/name
	UserAgent string
}

// New returns a client for the public GitHub API.
func New(current version.Info) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 2 * time.Minute},
		BaseURL:   "https://api.github.com",
		Repo:      DefaultRepo,
		UserAgent: "git-stack/" + current.Version,
	}
}

// Release is a published release and its downloadable assets.
type Release struct {
	Tag    string
	Assets map[string]string // asset name -> download URL
}

// Options control Run.
type Options struct {
	// Current is the running binary's version.
	Current version.Info
	// Target is the file to replace, normally os.Executable with symlinks resolved.
	Target string
	// GOOS and GOARCH pick the asset.
	GOOS, GOARCH string
	// Check only reports; nothing is downloaded.
	Check bool
	// Force replaces the binary even when it is up to date, ahead, or a dev build.
	Force bool
}

// Status says what Run did.
type Status int

const (
	// StatusUpToDate: the binary already is the latest release.
	StatusUpToDate Status = iota
	// StatusAhead: the binary is newer than the latest release.
	StatusAhead
	// StatusAvailable: a release is available but nothing was changed (Check,
	// or a dev build without Force).
	StatusAvailable
	// StatusUpdated: the binary was replaced.
	StatusUpdated
)

// Result reports the outcome of Run.
type Result struct {
	Status  Status
	Current string
	Latest  string
	// DevBuild is true when the running binary is not a release.
	DevBuild bool
	Target   string
}

// Latest fetches the newest release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := strings.TrimSuffix(c.BaseURL, "/") + "/repos/" + c.Repo + "/releases/latest"
	body, status, err := c.get(ctx, url, "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	switch {
	case status == http.StatusNotFound:
		return Release{}, stack.Newf(stack.KindAPIFailure, "no releases have been published for %s yet", c.Repo).
			WithSteps("install from source: go install github.com/" + c.Repo + "@latest")
	case status < 200 || status >= 300:
		return Release{}, stack.Newf(stack.KindAPIFailure, "GitHub returned HTTP %d for %s", status, url).
			WithDetail(strings.TrimSpace(string(body)))
	}
	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Release{}, stack.New(stack.KindAPIFailure, "could not parse the GitHub release response").WithCause(err)
	}
	rel := Release{Tag: payload.TagName, Assets: make(map[string]string, len(payload.Assets))}
	for _, a := range payload.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// Run compares the running binary with the latest release and, unless told
// otherwise, downloads and installs it.
func (c *Client) Run(ctx context.Context, o Options) (Result, error) {
	rel, err := c.Latest(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{Current: o.Current.Version, Latest: rel.Tag, DevBuild: !o.Current.Release, Target: o.Target}
	if !o.Force {
		switch {
		case res.DevBuild:
			res.Status = StatusAvailable
			return res, nil
		case version.Compare(o.Current.Version, rel.Tag) == 0:
			res.Status = StatusUpToDate
			return res, nil
		case version.Compare(o.Current.Version, rel.Tag) > 0:
			res.Status = StatusAhead
			return res, nil
		}
	}
	if o.Check {
		res.Status = StatusAvailable
		return res, nil
	}

	name := assetName(rel.Tag, o.GOOS, o.GOARCH)
	archiveURL, ok := rel.Assets[name]
	if !ok {
		return Result{}, stack.Newf(stack.KindUnsupported, "release %s has no build for %s/%s (%s)", rel.Tag, o.GOOS, o.GOARCH, name).
			WithSteps("install from source: go install github.com/" + c.Repo + "@" + rel.Tag)
	}
	sumsURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return Result{}, stack.Newf(stack.KindAPIFailure, "release %s has no checksums.txt; refusing to install an unverified binary", rel.Tag)
	}

	sums, status, err := c.get(ctx, sumsURL, "")
	if err != nil {
		return Result{}, err
	}
	if status != http.StatusOK {
		return Result{}, stack.Newf(stack.KindAPIFailure, "downloading checksums.txt failed with HTTP %d", status)
	}
	want, ok := checksumFor(sums, name)
	if !ok {
		return Result{}, stack.Newf(stack.KindAPIFailure, "checksums.txt in release %s has no entry for %s", rel.Tag, name)
	}

	archive, status, err := c.get(ctx, archiveURL, "")
	if err != nil {
		return Result{}, err
	}
	if status != http.StatusOK {
		return Result{}, stack.Newf(stack.KindAPIFailure, "downloading %s failed with HTTP %d", name, status)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return Result{}, stack.Newf(stack.KindUnknown, "checksum mismatch for %s; the download may be corrupt", name).
			WithSteps("run git stack update again", "or install from source: go install github.com/"+c.Repo+"@"+rel.Tag)
	}

	binary, err := extractBinary(archive, "git-stack")
	if err != nil {
		return Result{}, stack.Newf(stack.KindUnknown, "could not read %s", name).WithCause(err)
	}
	if err := replace(o.Target, binary); err != nil {
		return Result{}, stack.Newf(stack.KindUnknown, "could not replace %s", o.Target).
			WithCause(err).
			WithDetail(err.Error()).
			WithSteps(
				"check you can write to "+filepath.Dir(o.Target)+" (or rerun with sudo)",
				"or install from source: go install github.com/"+c.Repo+"@"+rel.Tag,
			)
	}
	res.Status = StatusUpdated
	return res, nil
}

func (c *Client) get(ctx context.Context, url, accept string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, stack.New(stack.KindAPIFailure, "bad release URL").WithCause(err)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, 0, err
		}
		return nil, 0, stack.Newf(stack.KindAPIFailure, "could not reach GitHub (%s)", url).
			WithCause(err).
			WithDetail(err.Error()).
			WithSteps("check your network connection and try again")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, stack.Newf(stack.KindAPIFailure, "reading the response from %s failed", url).WithCause(err)
	}
	return body, resp.StatusCode, nil
}

// assetName matches goreleaser's archive name_template.
func assetName(tag, goos, goarch string) string {
	return fmt.Sprintf("git-stack_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// checksumFor finds the sha256 for name in a goreleaser checksums.txt
// ("<hex>  <name>" per line).
func checksumFor(sums []byte, name string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == name {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

// extractBinary returns the contents of the entry called name in a tar.gz.
func extractBinary(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("archive has no %s entry", name)
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == name {
			return io.ReadAll(tr)
		}
	}
}

// replace writes the new binary next to target and renames it into place so
// the swap is atomic and a running process keeps its old inode.
func replace(target string, binary []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".git-stack-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(binary); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return err
	}
	return nil
}
