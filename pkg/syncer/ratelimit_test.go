package syncer

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/indexer"
)

// The onyx 429, as it arrived in the production log on 2026-11-23.
const onyxRateLimitBody = `{"code":-32000,"message":"Per-IP rate limit exceeded; retry after 300 seconds."}`

// The background sync loop is the traffic that earns the 429s, and it was the
// traffic that kept re-arming them: a 30 second ticker against a 300 second
// penalty, with eighteen passes per tick each discovering the breaker for
// itself.
//
// 07:44:10 [onyx] sync users: ... 429 ... retry after 300 seconds
// 07:44:40 [onyx] sync users: ... 429 ... retry after 300 seconds
// 07:44:52 [onyx] unreachable, pausing for 1m0s
func TestSyncAllStandsDownForTheWholePassWhileRateLimited(t *testing.T) {
	s, fake, _ := newTestSyncer(t, "onyx")
	fake.Status = http.StatusTooManyRequests
	fake.StatusBody = onyxRateLimitBody

	// The pass that meets the refusal. It asks once, gets the verdict, and
	// stops.
	if err := s.SyncAll(context.Background()); err == nil {
		t.Fatal("want an error from a pass against a refusing upstream")
	}
	asked := fake.QueryCount()
	if asked == 0 {
		t.Fatal("the first pass made no request at all")
	}

	until, paused := s.client.PausedUntil()
	if !paused {
		t.Fatal("one 429 must shut the sync client's breaker")
	}
	if wait := time.Until(until); wait < 299*time.Second {
		t.Errorf("standing down for %s, want the 300s the server asked for", wait.Round(time.Second))
	}

	// Every subsequent tick inside the penalty window must be free. This is the
	// whole fix: not one query, not one log line per pass, nothing.
	for i := 0; i < 10; i++ {
		err := s.SyncAll(context.Background())
		if !errors.Is(err, indexer.ErrUnavailable) {
			t.Fatalf("tick %d returned %v, want ErrUnavailable", i, err)
		}
	}
	if got := fake.QueryCount(); got != asked {
		t.Errorf("ten ticks inside the penalty window cost %d extra requests, want 0", got-asked)
	}
}

// And the first pass itself must not spend its eighteen sub-passes earning
// eighteen refusals. The client breaker shuts on the first one and the rest
// short-circuit.
func TestOnePassDoesNotSpendEighteenRequestsOnOneRefusal(t *testing.T) {
	s, fake, _ := newTestSyncer(t, "onyx")
	fake.Status = http.StatusTooManyRequests
	fake.StatusBody = onyxRateLimitBody

	_ = s.SyncAll(context.Background())

	if got := fake.QueryCount(); got != 1 {
		t.Errorf("one pass against a refusing upstream made %d requests, want 1: a 429 is a verdict, not a sample", got)
	}
}

// A healthy upstream must be entirely unaffected: the stand-down is a gate, not
// a tax.
func TestSyncAllIsUngatedWhenTheUpstreamIsHealthy(t *testing.T) {
	s, fake, _ := newTestSyncer(t, "onyx")
	fake.SeedChain(1, 5)

	if err := s.SyncAll(context.Background()); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	if _, paused := s.client.PausedUntil(); paused {
		t.Error("a healthy pass left the breaker shut")
	}
	before := fake.QueryCount()
	if err := s.SyncAll(context.Background()); err != nil {
		t.Fatalf("second SyncAll: %v", err)
	}
	if fake.QueryCount() <= before {
		t.Error("the second pass made no requests; the gate is on when it should not be")
	}
}
