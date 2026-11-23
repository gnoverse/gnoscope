package indexer

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// What to do when an upstream says "not now".
//
// A 429 is not a transport failure and must not share one with a timeout. A
// timeout is us guessing that the indexer is unwell; a 429 is the indexer
// telling us it has already made the decision, usually with a number attached.
// Measured on the gnoscope.com instance over 24 hours on 2026-11-23: 277
// upstream 429s, every one of them on onyx, and 38 breaker trips behind them.
// The upstream was explicit about the penalty,
//
//	{"code":-32000,"message":"Per-IP rate limit exceeded; retry after 300 seconds."}
//
// and the flat 60s (serve) and 30s (client) cooldowns brought us back inside
// the 300s window, which re-armed it. We amplified our own punishment.
//
// So: read what the server asked for, pause for at least that long, and widen
// the pause on every consecutive refusal from the same upstream.

// ErrRateLimited reports that an upstream refused the request because we are
// over its budget. It describes a decision the server made about us, not the
// server's health, and it carries a wait when the server named one.
var ErrRateLimited = errors.New("indexer rate limited")

// Bounds on a stand-down. The floor keeps a server that answers
// `Retry-After: 0` from becoming a hot loop; the ceiling keeps a server that
// asks for a day from taking a network off the site until a restart.
const (
	RateLimitMinPause = 5 * time.Second
	RateLimitMaxPause = 15 * time.Minute

	// RateLimitDefaultPause is the stand-down for a 429 that named no wait.
	// Deliberately longer than the generic breaker cooldowns it replaces: the
	// cheapest way to find out we guessed low is another 429.
	RateLimitDefaultPause = 60 * time.Second

	// rateLimitJitterFraction spreads the retries of several clients, and of
	// the serve and sync pools inside one process, that were all refused at the
	// same instant. One-sided and upward: subtracting from a wait the server
	// asked for is the bug this file exists to fix.
	rateLimitJitterFraction = 0.10
)

// RateLimitError is a refusal, with the wait the server asked for.
//
// The wait has two possible sources and both are read, because the only one
// that is standardised is not the one this upstream uses. See RetryAfterSource.
type RateLimitError struct {
	// URL is the endpoint that refused, not the pool it belongs to.
	URL string
	// Status is the HTTP status line, kept so the message reads like the one
	// operators already grep for.
	Status string
	// Body is the first part of the response body, which is where this
	// upstream puts the number.
	Body string
	// RetryAfter is how long the server asked us to wait. Zero means it named
	// no wait at all, which is not the same as asking for zero.
	RetryAfter time.Duration
	// RetryAfterSource names where RetryAfter came from: "header", "body", or
	// "" when the server named no wait.
	RetryAfterSource string
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("indexer returned %s (retry after %s, from the %s): %s",
			e.Status, e.RetryAfter, e.RetryAfterSource, e.Body)
	}
	return fmt.Sprintf("indexer returned %s (no retry-after given): %s", e.Status, e.Body)
}

// Unwrap lets every caller ask errors.Is(err, ErrRateLimited) without knowing
// this type exists.
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// RetryAfterOf reports the wait an upstream asked for, and whether it asked.
//
// Exported because the HTTP layer's own per-network breaker has to set its
// pause from the same number the client pool does. Two implementations of
// "how long should we stand down" is how one of them stays at 60 seconds.
func RetryAfterOf(err error) (time.Duration, bool) {
	var rl *RateLimitError
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		return rl.RetryAfter, true
	}
	return 0, false
}

// RateLimitPause is how long to stand down after the consecutive'th refusal in
// a row from one upstream, where consecutive counts from 1.
//
// The server's hint is the base, doubling per consecutive refusal, clamped to
// [RateLimitMinPause, RateLimitMaxPause] and then jittered upward. Doubling
// matters because a server that keeps refusing is telling us its number was an
// underestimate for our traffic, and a fixed cadence of retries against a
// sliding window never drains.
func RateLimitPause(hint time.Duration, consecutive int) time.Duration {
	base := hint
	if base <= 0 {
		base = RateLimitDefaultPause
	}
	if consecutive < 1 {
		consecutive = 1
	}
	// Shift rather than multiply, and cap the exponent before shifting: a
	// network refused for an hour reaches consecutive values that overflow a
	// time.Duration outright, and an overflowed pause is a negative one, which
	// reads as "retry immediately".
	if shift := consecutive - 1; shift > 0 {
		if shift > 20 {
			shift = 20
		}
		base <<= uint(shift)
		if base <= 0 {
			base = RateLimitMaxPause
		}
	}
	if base < RateLimitMinPause {
		base = RateLimitMinPause
	}
	if base > RateLimitMaxPause {
		base = RateLimitMaxPause
	}
	return base + time.Duration(rand.Int63n(int64(float64(base)*rateLimitJitterFraction)+1))
}

// newRateLimitError reads a refusal off the wire.
func newRateLimitError(url, status string, header http.Header, body []byte) *RateLimitError {
	e := &RateLimitError{
		URL:    url,
		Status: status,
		Body:   string(body[:min(200, len(body))]),
	}
	// Header first: it is the standardised answer, so an upstream that grows
	// one later starts being honoured without another change here.
	if d, ok := parseRetryAfterHeader(header.Get("Retry-After")); ok {
		e.RetryAfter, e.RetryAfterSource = d, "header"
		return e
	}
	if d, ok := parseRetryAfterBody(e.Body); ok {
		e.RetryAfter, e.RetryAfterSource = d, "body"
	}
	return e
}

// parseRetryAfterHeader reads RFC 9110's two forms: delta-seconds, and an
// HTTP-date to wait until.
func parseRetryAfterHeader(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		// A literal zero is the server saying "now". Report that it answered,
		// so the caller distinguishes it from silence; the floor in
		// RateLimitPause is what keeps it from becoming a hot loop.
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// retryAfterInBody matches the phrasings an upstream writes into an error
// message. gno.land's indexer fronting puts it in a JSON-RPC-shaped body,
//
//	{"code":-32000,"message":"Per-IP rate limit exceeded; retry after 300 seconds."}
//
// which is prose, not a field, so this is a scan over the text rather than a
// decode. Matching loosely is the right trade here: the cost of a false
// positive is pausing too long once, and the cost of a miss is the 60-second
// retry loop this whole file exists to stop.
var retryAfterInBody = regexp.MustCompile(
	`(?i)(?:retry[\s_-]*(?:after|in)|try again in|wait)\s*:?\s*(\d+)\s*(ms|millis(?:econds?)?|s|secs?|seconds?|m|mins?|minutes?)?`)

func parseRetryAfterBody(body string) (time.Duration, bool) {
	m := retryAfterInBody.FindStringSubmatch(body)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 0 {
		return 0, false
	}
	// Seconds when the unit is absent: every observed phrasing spells seconds,
	// and reading a bare number as milliseconds would turn a 300-second penalty
	// into a third of a second.
	unit := time.Second
	switch strings.ToLower(m[2]) {
	case "ms", "milli", "millis", "millisecond", "milliseconds":
		unit = time.Millisecond
	case "m", "min", "mins", "minute", "minutes":
		unit = time.Minute
	}
	return time.Duration(n) * unit, true
}
