package indexer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/reqmeter"
)

// The meter has to be wired to the one place requests actually leave, not to
// the forty call sites above it. These tests go through the public query
// methods, so a refactor that moves the round trip somewhere the meter cannot
// see it fails here rather than reporting zero forever.
//
// pkg/reqmeter holds the tests for what the meter computes; these are about
// whether it is fed.

func meteredClient(t *testing.T, h http.Handler) (*Client, *reqmeter.Meter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	m := reqmeter.New()
	c.SetMeter(m)
	return c, m, srv
}

func jsonHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

func TestMeterSeesEveryRoundTrip(t *testing.T) {
	const answer = `{"data":{"latestBlockHeight":445027}}`
	c, m, srv := meteredClient(t, jsonHandler(http.StatusOK, answer))

	for i := 0; i < 3; i++ {
		if _, err := c.LatestBlockHeight(context.Background()); err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
	}

	s := m.Snapshot()
	if s.All.Sent != 3 {
		t.Fatalf("sent = %d, want 3", s.All.Sent)
	}
	if s.All.Asked != 3 {
		t.Errorf("asked = %d, want 3", s.All.Asked)
	}
	if s.All.Bytes != int64(3*len(answer)) {
		t.Errorf("bytes = %d, want %d", s.All.Bytes, 3*len(answer))
	}
	if len(s.Endpoints) != 1 || s.Endpoints[0].URL != srv.URL {
		t.Fatalf("endpoints = %+v, want one for %s", s.Endpoints, srv.URL)
	}
	groups := s.Endpoints[0].Groups
	if len(groups) != 1 || groups[0].Label != "latestBlockHeight" {
		t.Fatalf("groups = %+v, want one labelled latestBlockHeight", groups)
	}
	// Three identical requests seconds apart: a cache of any lifetime would
	// have answered two of them. This is the figure #525 is waiting on.
	for i, ttl := range reqmeter.TTLs {
		if groups[0].WouldHit[i] != 2 {
			t.Errorf("would-have-hit at %s = %d, want 2", ttl, groups[0].WouldHit[i])
		}
	}
}

// What singleflight already saves, as the meter sees it: eight callers, one
// round trip, seven collapsed. The upside of a response cache has to be
// measured against this and not against the raw demand, or it would be counted
// twice.
func TestMeterCountsWhatSingleflightSaved(t *testing.T) {
	const callers = 8
	b, srv := newBlockingIndexer(t, ok(`{"data":{"latestBlockHeight":1}}`))
	c := NewClient(srv.URL)
	m := reqmeter.New()
	c.SetMeter(m)

	b.runConcurrently(callers, func(int) { c.LatestBlockHeight(context.Background()) })

	s := m.Snapshot()
	if got := atomic.LoadInt64(&b.hits); got != 1 {
		t.Fatalf("the fixture made %d round trips, want 1", got)
	}
	if s.All.Asked != callers {
		t.Errorf("asked = %d, want %d", s.All.Asked, callers)
	}
	if s.All.Sent != 1 {
		t.Errorf("sent = %d, want 1", s.All.Sent)
	}
	if s.All.Collapsed != callers-1 {
		t.Errorf("collapsed = %d, want %d", s.All.Collapsed, callers-1)
	}
}

// A refusal is counted as a refusal, which is the one outcome the budget is
// about. It is also still a request: it was made, and the upstream spent the
// budget on refusing it.
func TestMeterCountsRefusals(t *testing.T) {
	c, m, _ := meteredClient(t, jsonHandler(http.StatusTooManyRequests,
		`{"code":-32000,"message":"Per-IP rate limit exceeded; retry after 300 seconds."}`))

	if _, err := c.LatestBlockHeight(context.Background()); err == nil {
		t.Fatal("a 429 produced no error")
	}
	s := m.Snapshot()
	if s.All.Refused != 1 {
		t.Errorf("refused = %d, want 1", s.All.Refused)
	}
	if s.All.Sent != 1 {
		t.Errorf("sent = %d, want 1", s.All.Sent)
	}
	if s.All.Failed != 0 {
		t.Errorf("failed = %d, want 0: a refusal is not a transport failure", s.All.Failed)
	}
}

// A 200 carrying a GraphQL error is an answer, and the budget was spent on it.
// It must not be counted as a failure, and it must not be counted as a cache
// fill either: a cache that stored errors would report a hit rate on repeated
// failures that it could never deliver.
func TestMeterTreatsGraphQLErrorsAsAnsweredButNotCacheable(t *testing.T) {
	c, m, _ := meteredClient(t, jsonHandler(http.StatusOK,
		`{"errors":[{"message":"something went wrong upstream"}]}`))

	for i := 0; i < 2; i++ {
		if _, err := c.LatestBlockHeight(context.Background()); err == nil {
			t.Fatal("a GraphQL error produced no error")
		}
	}
	s := m.Snapshot()
	if s.All.Sent != 2 {
		t.Fatalf("sent = %d, want 2", s.All.Sent)
	}
	if s.All.Failed != 0 || s.All.Refused != 0 {
		t.Errorf("failed = %d, refused = %d, want 0 and 0", s.All.Failed, s.All.Refused)
	}
	for i, ttl := range reqmeter.TTLs {
		if got := s.Endpoints[0].Groups[0].WouldHit[i]; got != 0 {
			t.Errorf("an error response counted as a cache fill at %s: %d would-have-hits", ttl, got)
		}
	}
}

// A transport failure is a failure. The client has no fallback endpoint here,
// so this is one logical request and one attempt.
func TestMeterCountsTransportFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewClient(url)
	m := reqmeter.New()
	c.SetMeter(m)
	if _, err := c.LatestBlockHeight(context.Background()); err == nil {
		t.Fatal("a closed server produced no error")
	}
	s := m.Snapshot()
	if s.All.Failed != 1 {
		t.Errorf("failed = %d, want 1", s.All.Failed)
	}
}

// The endpoint probe runs through doQuery like everything else, so it is
// metered like everything else. It is worth seeing: it is a request this
// process makes on its own behalf, and a pool that reprobes often would show
// up here as traffic nobody asked for.
func TestMeterSeesTheEndpointProbe(t *testing.T) {
	var probes int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(raw), "getBlocks") {
			atomic.AddInt64(&probes, 1)
			io.WriteString(w, `{"data":{"latestBlockHeight":10,"getBlocks":[{"chain_id":"test-1","hash":"abc"}]}}`)
			return
		}
		io.WriteString(w, `{"data":{"latestBlockHeight":10}}`)
	})
	// Two endpoints, so the pool probes rather than taking the first.
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	srv2 := httptest.NewServer(h)
	t.Cleanup(srv2.Close)

	c := NewClient(srv.URL, srv2.URL)
	m := reqmeter.New()
	c.SetMeter(m)
	if _, err := c.LatestBlockHeight(context.Background()); err != nil {
		t.Fatalf("query: %v", err)
	}

	s := m.Snapshot()
	if atomic.LoadInt64(&probes) == 0 {
		t.Fatal("the fixture was never probed, so this proves nothing")
	}
	var probeRequests int64
	for _, ep := range s.Endpoints {
		for _, g := range ep.Groups {
			probeRequests += g.Sent
		}
	}
	if probeRequests != s.All.Sent {
		t.Errorf("the groups account for %d of %d requests", probeRequests, s.All.Sent)
	}
	if s.All.Sent <= 1 {
		t.Errorf("sent = %d, want the probe requests as well as the query", s.All.Sent)
	}
}

// A Client built as a struct literal has no meter, which is how several tests
// and tools in this repo build one. It must still work.
func TestClientWithoutMeterStillQueries(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(http.StatusOK, `{"data":{"latestBlockHeight":7}}`))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	c.SetMeter(nil)
	h, err := c.LatestBlockHeight(context.Background())
	if err != nil || h != 7 {
		t.Fatalf("height = %d, err = %v", h, err)
	}
}
