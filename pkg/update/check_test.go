package update

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/cache"
	"github.com/DomBlack/git-stack/pkg/version"
)

func wait(t *testing.T, ch *Check) {
	t.Helper()
	select {
	case <-ch.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("background check did not finish")
	}
}

func TestCheckerRecordsAndReportsALaterRelease(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	store := cache.At(t.TempDir())
	c := &Checker{Client: gh.client(), Store: store}

	// First run: nothing is known yet, so nothing is reported while the
	// request is in flight; once it lands the answer is there.
	ch := c.Start(context.Background(), release("v1.2.0"))
	wait(t, ch)
	if latest, ok := ch.Available(); !ok || latest != "v1.3.0" {
		t.Fatalf("after refresh: %q %v", latest, ok)
	}
	if n := len(gh.hits); n != 1 {
		t.Errorf("one request expected, got %d", n)
	}

	// Second run: the cache is fresh, so no request and an instant answer.
	ch = c.Start(context.Background(), release("v1.2.0"))
	if latest, ok := ch.Available(); !ok || latest != "v1.3.0" {
		t.Errorf("from cache: %q %v", latest, ok)
	}
	select {
	case <-ch.Done():
	default:
		t.Error("a fresh cache needs no background work")
	}
	if n := len(gh.hits); n != 1 {
		t.Errorf("cached run must not hit GitHub, got %d requests", n)
	}

	// Up to date, ahead and dev builds have nothing to say.
	for _, cur := range []version.Info{release("v1.3.0"), release("v1.4.0"), {Version: "dev", Commit: "abc1234"}} {
		if latest, ok := c.Start(context.Background(), cur).Available(); ok {
			t.Errorf("%s: unexpected notice about %s", cur, latest)
		}
	}
}

func TestCheckerClaimsTheSlotEvenWhenGitHubFails(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	gh.status = http.StatusForbidden
	store := cache.At(t.TempDir())
	c := &Checker{Client: gh.client(), Store: store}

	ch := c.Start(context.Background(), release("v1.2.0"))
	wait(t, ch)
	if _, ok := ch.Available(); ok {
		t.Error("a failed check must not report anything")
	}
	// The failure is remembered for the TTL so the next command doesn't ask again.
	ch = c.Start(context.Background(), release("v1.2.0"))
	wait(t, ch)
	if n := len(gh.hits); n != 1 {
		t.Errorf("failed check should not be retried within the TTL, got %d requests", n)
	}
}

func TestCheckerRefreshesAfterTTL(t *testing.T) {
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	store := cache.At(t.TempDir())
	c := &Checker{Client: gh.client(), Store: store, TTL: time.Nanosecond}

	wait(t, c.Start(context.Background(), release("v1.2.0")))
	time.Sleep(time.Millisecond)
	ch := c.Start(context.Background(), release("v1.2.0"))
	// The previous answer is reported straight away, before the refresh lands.
	if latest, ok := ch.Available(); !ok || latest != "v1.3.0" {
		t.Errorf("stale cache should still answer: %q %v", latest, ok)
	}
	wait(t, ch)
	if n := len(gh.hits); n != 2 {
		t.Errorf("stale cache should refresh, got %d requests", n)
	}
}

func TestNilCheckIsQuiet(t *testing.T) {
	var ch *Check
	if _, ok := ch.Available(); ok {
		t.Error("nil check must report nothing")
	}
}
