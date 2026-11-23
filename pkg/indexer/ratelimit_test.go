package indexer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The 429 body gno.land's indexer fronting actually sends, copied from the
// production log on 2026-11-23. It is the whole reason the body is parsed at
// all: the number is prose inside a JSON-RPC-shaped message, not a header and
// not a field.
const onyxRateLimitBody = `{"code":-32000,"message":"Per-IP rate limit exceeded; retry after 300 seconds."}`

func TestRetryAfterIsReadFromBothSources(t *testing.T) {
	for _, tc := range []struct {
		name       string
		header     string
		body       string
		wantWait   time.Duration
		wantSource string
	}{
		{
			name:       "the upstream we actually have puts it in the body",
			body:       onyxRateLimitBody,
			wantWait:   300 * time.Second,
			wantSource: "body",
		},
		{
			name:       "a standards-compliant upstream puts it in the header",
			header:     "120",
			body:       `{"error":"slow down"}`,
			wantWait:   120 * time.Second,
			wantSource: "header",
		},
		{
			name:       "the header wins when both are present",
			header:     "45",
			body:       onyxRateLimitBody,
			wantWait:   45 * time.Second,
			wantSource: "header",
		},
		{
			name:       "an HTTP-date header is a deadline, not a count",
			header:     time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat),
			body:       `nope`,
			wantWait:   90 * time.Second,
			wantSource: "header",
		},
		{
			name:       "nginx prose",
			body:       `<html><body>429 Too Many Requests. Please try again in 30 seconds.</body></html>`,
			wantWait:   30 * time.Second,
			wantSource: "body",
		},
		{
			name:       "minutes are a unit too",
			body:       `{"message":"rate limited, retry after 5 minutes"}`,
			wantWait:   5 * time.Minute,
			wantSource: "body",
		},
		{
			name:       "a bare number in the message means seconds",
			body:       `{"message":"Retry-After: 300"}`,
			wantWait:   300 * time.Second,
			wantSource: "body",
		},
		{
			name:       "a server that names no wait names no wait",
			body:       `{"message":"too many requests"}`,
			wantWait:   0,
			wantSource: "",
		},
		{
			name:       "a garbage header falls through to the body",
			header:     "soon",
			body:       onyxRateLimitBody,
			wantWait:   300 * time.Second,
			wantSource: "body",
		},
		{
			name:       "a header of zero is still an answer",
			header:     "0",
			body:       onyxRateLimitBody,
			wantWait:   0,
			wantSource: "header",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set("Retry-After", tc.header)
			}
			e := newRateLimitError("http://example/query", "429 Too Many Requests", h, []byte(tc.body))

			if e.RetryAfterSource != tc.wantSource {
				t.Errorf("source = %q, want %q", e.RetryAfterSource, tc.wantSource)
			}
			// HTTP-date is computed against a moving clock, so compare loosely.
			if d := e.RetryAfter - tc.wantWait; d > time.Second || d < -time.Second {
				t.Errorf("RetryAfter = %s, want %s", e.RetryAfter, tc.wantWait)
			}
			if !errors.Is(e, ErrRateLimited) {
				t.Error("a refusal must answer errors.Is(err, ErrRateLimited)")
			}
			got, ok := RetryAfterOf(e)
			if ok != (tc.wantWait > 0) {
				t.Errorf("RetryAfterOf reported ok=%v for a wait of %s", ok, tc.wantWait)
			}
			if ok && got != e.RetryAfter {
				t.Errorf("RetryAfterOf = %s, want %s", got, e.RetryAfter)
			}
		})
	}
}

func TestRateLimitPauseHonoursTheHintAndWidens(t *testing.T) {
	for _, tc := range []struct {
		name            string
		hint            time.Duration
		consecutive     int
		wantAtLeast     time.Duration
		wantAtMost      time.Duration
		wantStrictlyGTE time.Duration // the old flat cooldown this replaces
	}{
		{
			name:            "the 300 second penalty we were ignoring",
			hint:            300 * time.Second,
			consecutive:     1,
			wantAtLeast:     300 * time.Second,
			wantAtMost:      330 * time.Second,
			wantStrictlyGTE: 60 * time.Second,
		},
		{
			name:        "a second refusal in a row doubles it",
			hint:        300 * time.Second,
			consecutive: 2,
			wantAtLeast: 600 * time.Second,
			wantAtMost:  660 * time.Second,
		},
		{
			name:        "no hint falls back to the default, not to zero",
			hint:        0,
			consecutive: 1,
			wantAtLeast: RateLimitDefaultPause,
			wantAtMost:  RateLimitDefaultPause + RateLimitDefaultPause/10,
		},
		{
			name:        "a zero hint is floored, not obeyed",
			hint:        0,
			consecutive: 1,
			wantAtLeast: RateLimitMinPause,
			wantAtMost:  RateLimitDefaultPause * 2,
		},
		{
			name:        "a one second hint is floored",
			hint:        time.Second,
			consecutive: 1,
			wantAtLeast: RateLimitMinPause,
			wantAtMost:  RateLimitMinPause * 2,
		},
		{
			name:        "the ceiling holds",
			hint:        24 * time.Hour,
			consecutive: 1,
			wantAtLeast: RateLimitMaxPause,
			wantAtMost:  RateLimitMaxPause * 2,
		},
		{
			name:        "a long run of refusals cannot overflow into a negative pause",
			hint:        300 * time.Second,
			consecutive: 500,
			wantAtLeast: RateLimitMaxPause,
			wantAtMost:  RateLimitMaxPause * 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Jittered, so sample it rather than asserting one value.
			for i := 0; i < 50; i++ {
				got := RateLimitPause(tc.hint, tc.consecutive)
				if got < tc.wantAtLeast || got > tc.wantAtMost {
					t.Fatalf("RateLimitPause(%s, %d) = %s, want within [%s, %s]",
						tc.hint, tc.consecutive, got, tc.wantAtLeast, tc.wantAtMost)
				}
				if tc.wantStrictlyGTE > 0 && got <= tc.wantStrictlyGTE {
					t.Fatalf("RateLimitPause(%s, %d) = %s, which is still inside the old %s cooldown",
						tc.hint, tc.consecutive, got, tc.wantStrictlyGTE)
				}
			}
		})
	}
}

// refusing serves nothing but 429s and counts how many it was asked.
func refusing(t *testing.T, status int, body string, header map[string]string) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The headline case, and the one the production log is a record of: one 429
// carrying a 300 second penalty must take the pool off the air for 300 seconds,
// on the first refusal, without a second request.
func TestA429PausesForWhatTheServerAskedAndTripsOnTheFirstOne(t *testing.T) {
	srv, hits := refusing(t, http.StatusTooManyRequests, onyxRateLimitBody, nil)
	c := NewClient(srv.URL)

	err := c.query(context.Background(), `{ latestBlockHeight }`, nil, new(struct{}))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("first query returned %v, want an error that is ErrRateLimited", err)
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Fatalf("upstream was asked %d times for one query", got)
	}

	until, paused := c.PausedUntil()
	if !paused {
		t.Fatal("the breaker is shut after a 429; a refusal is the server's verdict, not a sample")
	}
	wait := time.Until(until)
	if wait <= clientBreakerCooldown {
		t.Errorf("paused for %s, which is inside the old flat %s cooldown: this is the amplification",
			wait.Round(time.Second), clientBreakerCooldown)
	}
	if wait < 299*time.Second || wait > 331*time.Second {
		t.Errorf("paused for %s, want about the 300s the server asked for", wait.Round(time.Second))
	}

	// And nothing else goes out while it holds.
	if err := c.query(context.Background(), `{ latestBlockHeight }`, nil, new(struct{})); !errors.Is(err, ErrUnavailable) {
		t.Errorf("second query returned %v, want ErrUnavailable", err)
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Errorf("upstream was asked %d times; the pause did not hold", got)
	}
}

// The control. An ordinary server error still needs two samples and still gets
// the flat cooldown, because nothing about it says how long to wait.
func TestAnOrdinaryFailureKeepsTheOldThresholdAndCooldown(t *testing.T) {
	srv, hits := refusing(t, http.StatusInternalServerError, "boom", nil)
	c := NewClient(srv.URL)

	if err := c.query(context.Background(), `{ latestBlockHeight }`, nil, new(struct{})); err == nil {
		t.Fatal("want an error from a 500")
	}
	if _, paused := c.PausedUntil(); paused {
		t.Fatal("one 500 must not trip the breaker; that is what the threshold is for")
	}
	if err := c.query(context.Background(), `{ latestBlockHeight }`, nil, new(struct{})); err == nil {
		t.Fatal("want an error from a 500")
	}
	until, paused := c.PausedUntil()
	if !paused {
		t.Fatal("two 500s must trip the breaker")
	}
	if wait := time.Until(until); wait > clientBreakerCooldown {
		t.Errorf("paused for %s, want no more than the flat %s", wait, clientBreakerCooldown)
	}
	if got := atomic.LoadInt64(hits); got != 2 {
		t.Errorf("upstream was asked %d times, want 2", got)
	}
}

// A 429 with no number at all still has to be worse than a timeout, because
// the cheapest way to learn we guessed low is another 429.
func TestA429WithNoHintStillOutlastsTheFlatCooldown(t *testing.T) {
	srv, _ := refusing(t, http.StatusTooManyRequests, `{"message":"too many requests"}`, nil)
	c := NewClient(srv.URL)

	if err := c.query(context.Background(), `{ latestBlockHeight }`, nil, new(struct{})); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
	until, paused := c.PausedUntil()
	if !paused {
		t.Fatal("a 429 trips on the first one even when it names no wait")
	}
	if wait := time.Until(until); wait < RateLimitDefaultPause {
		t.Errorf("paused for %s, want at least %s", wait, RateLimitDefaultPause)
	}
}

// A Retry-After header is honoured even though the upstream we have does not
// send one, so an upstream that grows one later needs no further change.
func TestA429HeaderIsHonoured(t *testing.T) {
	srv, _ := refusing(t, http.StatusTooManyRequests, `{"message":"too many requests"}`,
		map[string]string{"Retry-After": "600"})
	c := NewClient(srv.URL)

	err := c.query(context.Background(), `{ latestBlockHeight }`, nil, new(struct{}))
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("got %v, want a *RateLimitError", err)
	}
	if rl.RetryAfterSource != "header" || rl.RetryAfter != 600*time.Second {
		t.Fatalf("read %s from the %q; want 600s from the header", rl.RetryAfter, rl.RetryAfterSource)
	}
	until, _ := c.PausedUntil()
	if wait := time.Until(until); wait < 600*time.Second {
		t.Errorf("paused for %s, want at least the 600s the header asked for", wait)
	}
}

// A refusal must never be shortened by an ordinary failure landing on top of
// it. Overwriting a 300 second stand-down with the 30 second cooldown is the
// same amplification wearing a different hat.
func TestAnOrdinaryFailureDoesNotShortenARateLimitPause(t *testing.T) {
	c := NewClient("http://127.0.0.1:1/query")

	c.recordResult(&RateLimitError{RetryAfter: 300 * time.Second, RetryAfterSource: "body"})
	before, _ := c.PausedUntil()

	c.recordResult(fmt.Errorf("dial tcp: connection refused"))
	c.recordResult(fmt.Errorf("dial tcp: connection refused"))

	after, paused := c.PausedUntil()
	if !paused {
		t.Fatal("breaker closed")
	}
	if after.Before(before) {
		t.Errorf("pause moved in from %s to %s", before, after)
	}
}

// And a success clears the ladder, so a network that recovers is not punished
// by the exponent its bad afternoon built up.
func TestSuccessResetsTheRateLimitLadder(t *testing.T) {
	c := NewClient("http://127.0.0.1:1/query")
	for i := 0; i < 4; i++ {
		c.recordResult(&RateLimitError{RetryAfter: 300 * time.Second})
	}
	c.recordResult(nil)
	if _, paused := c.PausedUntil(); paused {
		t.Fatal("a success must open the breaker")
	}

	c.recordResult(&RateLimitError{RetryAfter: 300 * time.Second})
	until, _ := c.PausedUntil()
	if wait := time.Until(until); wait > 331*time.Second {
		t.Errorf("first refusal after a success paused for %s; the exponent was not reset", wait)
	}
}
