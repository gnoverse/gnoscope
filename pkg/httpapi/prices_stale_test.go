package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/price"
)

// resetPriceCache clears the package-level cache so one test cannot see
// another's quotes. Keyed per network everywhere, but the tests here share a
// process.
func resetPriceCache(t *testing.T) {
	t.Helper()
	assetPrices.mu.Lock()
	defer assetPrices.mu.Unlock()
	assetPrices.result = map[string]*price.Result{}
	assetPrices.resultAt = map[string]time.Time{}
	assetPrices.inflight = map[string]*sync.Mutex{}
}

// seedPrice puts a good result in the cache, dated age ago.
func seedPrice(network string, age time.Duration) *price.Result {
	res := &price.Result{Network: network}
	assetPrices.mu.Lock()
	defer assetPrices.mu.Unlock()
	assetPrices.result[network] = res
	assetPrices.resultAt[network] = time.Now().Add(-age)
	return res
}

// A quote past its TTL is handed over immediately rather than recomputed on the
// reader. The refresh is dozens of RPC reads: measured server-side across 24h
// of production traffic on 2026-10-21, /api/prices averaged 9.5s of handler
// time, and /api/holders and the address defi tab read this too.
func TestAStaleQuoteIsServedWithoutWaiting(t *testing.T) {
	resetPriceCache(t)
	const network = "prices-stale"
	want := seedPrice(network, priceQuoteTTL+time.Second)

	// No RPC is configured for this network, so a refresh fails fast and
	// cannot be what makes this return.
	a := &API{}
	start := time.Now()
	got, err := a.pricesFor(context.Background(), network)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %v, want the last good result", got)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("took %v, want an immediate answer", elapsed)
	}

	// A failed background refresh keeps the last good result rather than
	// replacing it with nothing.
	time.Sleep(50 * time.Millisecond)
	assetPrices.mu.Lock()
	still := assetPrices.result[network]
	assetPrices.mu.Unlock()
	if still != want {
		t.Errorf("the failed refresh dropped the last good result")
	}
}

// Inside the TTL nothing is refreshed; stale but inside the grace is handed
// over and refreshed behind; past the grace the quote is too old to hand over
// and the reader waits for a real answer, the same rule the response cache and
// the gov/dao memos follow.
//
// Whether the reader waited is what is asserted, because the value does not
// distinguish the three: a refresh that fails still returns the last good
// result, which is the long-standing "degrade to an old price rather than to an
// empty page" behaviour this must not change. Only the blocking path runs a
// refresh in front of the reader, and only that path can return its error.
func TestPriceFreshnessBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     time.Duration
		wantErr bool // true when the reader waited on a refresh
	}{
		{"fresh", priceQuoteTTL / 2, false},
		{"stale but inside the grace", priceQuoteTTL + time.Second, false},
		{"past the grace", priceQuoteTTL + priceStaleGrace + time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetPriceCache(t)
			network := "prices-" + tc.name
			want := seedPrice(network, tc.age)

			// No RPC for this network, so a refresh run in front of the reader
			// fails and says so, and one run behind them cannot.
			a := &API{}
			got, err := a.pricesFor(context.Background(), network)

			if got != want {
				t.Errorf("got %v, want the last good result either way", got)
			}
			if tc.wantErr && err == nil {
				t.Errorf("no error, so the reader was served without a refresh at %v old", tc.age)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("err = %v, so the reader waited on a refresh at %v old", err, tc.age)
			}
		})
	}
}

// Several readers arriving on a stale quote start one refresh between them.
// A refresh is expensive enough that a fan-out is the thing being prevented.
func TestStaleQuotesRefreshOnce(t *testing.T) {
	resetPriceCache(t)
	const network = "prices-once"
	seedPrice(network, priceQuoteTTL+time.Second)

	a := &API{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.pricesFor(context.Background(), network)
		}()
	}
	wg.Wait()

	// The per-network lock is the single-flight. Whoever holds it is the one
	// refresh; everybody else returned the held value and started nothing.
	assetPrices.mu.Lock()
	lock := assetPrices.inflight[network]
	assetPrices.mu.Unlock()
	if lock == nil {
		t.Fatal("no refresh was ever claimed")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if lock.TryLock() {
			lock.Unlock()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the refresh never finished, so more than one may have been started")
}
