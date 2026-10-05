package update

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/version"
)

// knownName is the cache document holding the last release the background
// check saw.
const knownName = "latest-release"

// Defaults for Checker.
const (
	// DefaultCheckTTL is how long one check is trusted before the next
	// command asks GitHub again.
	DefaultCheckTTL = 24 * time.Hour
	// DefaultCheckTimeout bounds one background request; a slow network
	// must never hold a command up.
	DefaultCheckTimeout = 5 * time.Second
)

// Known is what the background check remembers between runs.
type Known struct {
	Latest string `json:"latest"`
}

// DefaultCacheDir is where the user wide cache lives: the OS cache directory
// (XDG_CACHE_HOME or ~/.cache on Linux, ~/Library/Caches on macOS) plus
// git-stack.
func DefaultCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "git-stack"), nil
}

// Checker keeps the latest release up to date in the background. Start
// returns straight away with whatever the last run found; when that is older
// than TTL it asks GitHub once more in a goroutine and records the answer for
// the next run. A command never waits for it.
type Checker struct {
	Client  *Client
	Store   *cache.Store
	TTL     time.Duration // zero: DefaultCheckTTL
	Timeout time.Duration // zero: DefaultCheckTimeout
}

// Check is one run's view of the latest release.
type Check struct {
	// Current is the running binary.
	Current version.Info
	cached  string // the latest release the previous run recorded
	fresh   string // this run's answer, set before done closes
	done    chan struct{}
}

// Start reads the cached answer and, when it is stale or missing, refreshes
// it in the background. The returned Check answers from the cache until the
// refresh lands.
func (c *Checker) Start(ctx context.Context, current version.Info) *Check {
	ch := &Check{Current: current, done: make(chan struct{})}
	ttl := c.TTL
	if ttl == 0 {
		ttl = DefaultCheckTTL
	}
	known, state, _, _ := cache.Read[Known](c.Store, knownName, ttl)
	ch.cached = known.Latest
	if state == cache.Fresh {
		close(ch.done)
		return ch
	}
	// Claim this slot before asking: if the command exits before the answer
	// arrives, the next command must not ask straight away, or short
	// commands would hit GitHub every time without ever recording a result.
	_ = cache.Write(c.Store, knownName, known)
	go func() {
		defer close(ch.done)
		timeout := c.Timeout
		if timeout == 0 {
			timeout = DefaultCheckTimeout
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		rel, err := c.Client.Latest(ctx)
		if err != nil || rel.Tag == "" {
			return
		}
		ch.fresh = rel.Tag
		_ = cache.Write(c.Store, knownName, Known{Latest: rel.Tag})
	}()
	return ch
}

// Available reports the newer release worth mentioning, if there is one:
// this run's answer when it has already arrived, otherwise the previous
// run's. Dev builds are left alone (they were built that way on purpose, and
// update would not replace them anyway), as are binaries ahead of the latest
// release.
func (ch *Check) Available() (latest string, ok bool) {
	if ch == nil {
		return "", false
	}
	latest = ch.cached
	select {
	case <-ch.done:
		if ch.fresh != "" {
			latest = ch.fresh
		}
	default:
	}
	if latest == "" || !ch.Current.Release || version.Compare(ch.Current.Version, latest) >= 0 {
		return "", false
	}
	return latest, true
}

// Done is closed once the background refresh has finished (or was not
// needed). Commands never wait on it; tests do.
func (ch *Check) Done() <-chan struct{} { return ch.done }
