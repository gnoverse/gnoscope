package httpapi

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/indexer"
)

// The per-network breaker in front of the merged fan-out, and the one number
// that was wrong on the production instance: 277 upstream 429s in 24 hours, all
// on onyx, each asking for 300 seconds and each answered with a 60 second
// pause that put the next request back inside the penalty window.
func TestHealthTrackerStandsDownForWhatTheServerAsked(t *testing.T) {
	refusal := func(secs int) error {
		return &indexer.RateLimitError{
			Status:           "429 Too Many Requests",
			Body:             fmt.Sprintf(`{"code":-32000,"message":"Per-IP rate limit exceeded; retry after %d seconds."}`, secs),
			RetryAfter:       time.Duration(secs) * time.Second,
			RetryAfterSource: "body",
		}
	}

	for _, tc := range []struct {
		name string
		errs []error
		// want bounds on the remaining pause after the last error.
		wantPaused  bool
		wantAtLeast time.Duration
		wantAtMost  time.Duration
	}{
		{
			name:        "one 429 with a 300 second hint pauses for 300, not 60",
			errs:        []error{refusal(300)},
			wantPaused:  true,
			wantAtLeast: 299 * time.Second,
			wantAtMost:  331 * time.Second,
		},
		{
			name:        "a second refusal widens it rather than repeating it",
			errs:        []error{refusal(300), refusal(300)},
			wantPaused:  true,
			wantAtLeast: 599 * time.Second,
			wantAtMost:  661 * time.Second,
		},
		{
			name:        "a 429 naming no wait still beats the flat cooldown",
			errs:        []error{&indexer.RateLimitError{Status: "429 Too Many Requests", Body: "too many requests"}},
			wantPaused:  true,
			wantAtLeast: indexer.RateLimitDefaultPause - time.Second,
			wantAtMost:  2 * indexer.RateLimitDefaultPause,
		},
		{
			name:       "one ordinary failure still does not trip it",
			errs:       []error{errors.New("context deadline exceeded")},
			wantPaused: false,
		},
		{
			name:        "two ordinary failures still get the flat cooldown",
			errs:        []error{errors.New("boom"), errors.New("boom")},
			wantPaused:  true,
			wantAtLeast: breakerCooldown - time.Second,
			wantAtMost:  breakerCooldown,
		},
		{
			name:        "an ordinary failure may not shorten a refusal's stand-down",
			errs:        []error{refusal(300), errors.New("boom"), errors.New("boom")},
			wantPaused:  true,
			wantAtLeast: 299 * time.Second,
			wantAtMost:  331 * time.Second,
		},
		{
			name:       "a success clears it",
			errs:       []error{refusal(300), nil},
			wantPaused: false,
		},
		{
			name:        "and clears the exponent with it",
			errs:        []error{refusal(300), refusal(300), refusal(300), nil, refusal(300)},
			wantPaused:  true,
			wantAtLeast: 299 * time.Second,
			wantAtMost:  331 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHealthTracker()
			for _, err := range tc.errs {
				h.record("onyx", err)
			}

			if got := h.shouldSkip("onyx"); got != tc.wantPaused {
				t.Fatalf("shouldSkip = %v, want %v", got, tc.wantPaused)
			}
			if !tc.wantPaused {
				return
			}

			h.mu.Lock()
			wait := time.Until(h.state["onyx"].skipUntil)
			h.mu.Unlock()
			if wait < tc.wantAtLeast || wait > tc.wantAtMost {
				t.Errorf("paused for %s, want within [%s, %s]", wait.Round(time.Second), tc.wantAtLeast, tc.wantAtMost)
			}
		})
	}
}

// The first-occurrence rule, stated on its own because it is the half that is
// easy to lose: a timeout is us guessing, a 429 is the server telling us.
func TestA429TripsTheNetworkBreakerOnTheFirstFailure(t *testing.T) {
	h := newHealthTracker()
	h.record("onyx", &indexer.RateLimitError{RetryAfter: 300 * time.Second, RetryAfterSource: "body"})

	if !h.shouldSkip("onyx") {
		t.Fatalf("one 429 did not trip the breaker; breakerThreshold is %d, and a refusal must not wait for it", breakerThreshold)
	}
	if h.shouldSkip("gnoland1") {
		t.Error("the pause leaked onto another network")
	}
}
