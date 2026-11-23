package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/reqmeter"
)

// What this file holds the line on.
//
// The cache is the one piece of this explorer that can be wrong without
// anything reporting it: a stale 429 is loud, a stale block is not. So the
// tests that matter most here are the negative ones. Hit, miss, expiry and
// eviction are the mechanism; "the tip is never served from memory", "block 1
// is never served from memory" and "a reset empties it" are the invariants,
// and each of them is a silent data-corruption bug if it breaks.

// countingBlockServer answers getBlocks and latestBlockHeight from a fixed
// chain height, counting what reached it.
type countingBlockServer struct {
	hits   atomic.Int64
	tip    atomic.Int64
	suffix atomic.Value // string appended to every block hash, so a "reset" is visible
}

func newBlockServer(t *testing.T, tip int) (*Client, *countingBlockServer, *reqmeter.Meter) {
	t.Helper()
	s := &countingBlockServer{}
	s.tip.Store(int64(tip))
	s.suffix.Store("a")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.hits.Add(1)
		var req struct {
			Query string `json:"query"`
		}
		json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "latestBlockHeight"):
			fmt.Fprintf(w, `{"data":{"latestBlockHeight":%d}}`, s.tip.Load())
		case strings.Contains(req.Query, "getBlocks"):
			io.WriteString(w, s.blocks(req.Query))
		default:
			io.WriteString(w, `{"data":{}}`)
		}
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL)
	m := reqmeter.New()
	c.SetMeter(m)
	rc := NewResponseCache(defaultCacheEntries, defaultCacheBytes)
	c.SetCache(rc)
	m.RegisterCache(rc.Stats)
	return c, s, m
}

// blocks answers an eq or a gt/lt getBlocks from the query text, which is
// enough to drive GetBlock and GetBlocksInRange without a GraphQL parser.
func (s *countingBlockServer) blocks(q string) string {
	suffix := s.suffix.Load().(string)
	tip := int(s.tip.Load())
	var lo, hi int
	if i := strings.Index(q, "eq:"); i >= 0 {
		fmt.Sscanf(q[i:], "eq: %d", &lo)
		hi = lo
	} else if i := strings.Index(q, "gt:"); i >= 0 {
		var gt, lt int
		fmt.Sscanf(q[i:], "gt: %d, lt: %d", &gt, &lt)
		lo, hi = gt+1, lt-1
	}
	rows := []string{}
	for h := max(lo, 1); h <= hi && h <= tip; h++ {
		rows = append(rows, fmt.Sprintf(
			`{"height":%d,"chain_id":"test-1","time":"2026-01-01T00:00:%02dZ","hash":"h%d%s"}`,
			h, h%60, h, suffix))
	}
	return `{"data":{"getBlocks":[` + strings.Join(rows, ",") + `]}}`
}

func TestCachedBlockIsAskedOnce(t *testing.T) {
	c, srv, m := newBlockServer(t, 1000)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		b, err := c.GetBlock(ctx, 500)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if b == nil || b.Hash != "h500a" {
			t.Fatalf("call %d: block = %+v, want h500a", i, b)
		}
	}
	if got := srv.hits.Load(); got != 1 {
		t.Errorf("round trips = %d, want 1: a finalised block cannot change", got)
	}

	// The saving has to be readable off /api/indexer/usage, not inferred.
	// Asked is every caller; Served is the four the cache answered.
	s := m.Snapshot()
	if s.All.Asked != 5 || s.All.Sent != 1 || s.All.Served != 4 {
		t.Fatalf("asked/sent/served = %d/%d/%d, want 5/1/4", s.All.Asked, s.All.Sent, s.All.Served)
	}
	// Asked = Sent + Served + Collapsed, with each saving claimed once.
	if s.All.Collapsed != 0 {
		t.Errorf("collapsed = %d, want 0: these were sequential, not concurrent", s.All.Collapsed)
	}
	if s.All.ServedPercent != 80 {
		t.Errorf("served_percent = %v, want 80", s.All.ServedPercent)
	}
	if len(s.Caches) != 1 || s.Caches[0].Hits != 4 || s.Caches[0].Entries != 1 {
		t.Errorf("caches = %+v, want one with 4 hits and 1 entry", s.Caches)
	}
}

// A miss is a miss: two different heights are two questions.
func TestDifferentBlocksAreDifferentRequests(t *testing.T) {
	c, srv, _ := newBlockServer(t, 1000)
	ctx := context.Background()
	for _, h := range []int{500, 501, 500, 501, 502} {
		if _, err := c.GetBlock(ctx, h); err != nil {
			t.Fatalf("height %d: %v", h, err)
		}
	}
	if got := srv.hits.Load(); got != 3 {
		t.Errorf("round trips = %d, want 3 (one per distinct height)", got)
	}
}

// ⚠️ The invariant that makes the whole thing safe to deploy. A stale tip makes
// the explorer silently lag, which is worse than a 429 because nothing reports
// it, so latestBlockHeight is in no cache class at all.
func TestTipIsNeverServedFromCache(t *testing.T) {
	c, srv, m := newBlockServer(t, 1000)
	ctx := context.Background()

	if h, err := c.LatestBlockHeight(ctx); err != nil || h != 1000 {
		t.Fatalf("tip = %d, %v, want 1000", h, err)
	}
	srv.tip.Store(1001)
	if h, err := c.LatestBlockHeight(ctx); err != nil || h != 1001 {
		t.Fatalf("tip after advance = %d, %v, want 1001: the tip must never be cached", h, err)
	}
	if got := srv.hits.Load(); got != 2 {
		t.Errorf("round trips = %d, want 2", got)
	}
	if s := m.Snapshot(); s.All.Served != 0 {
		t.Errorf("served = %d, want 0: nothing about the tip may be answered from memory", s.All.Served)
	}
}

// ⚠️ Block 1 is the chain's fingerprint, read by Syncer.chainFingerprint and
// API.VerifyRPCChains to notice that the indexer is serving a different chain.
// Cached, a reset becomes undetectable rather than late.
func TestBlockOneIsNeverCached(t *testing.T) {
	c, srv, _ := newBlockServer(t, 1000)
	ctx := context.Background()

	first, err := c.GetBlock(ctx, 1)
	if err != nil || first == nil {
		t.Fatalf("block 1: %+v, %v", first, err)
	}
	// The chain comes back as a different one at the same heights.
	srv.suffix.Store("b")
	second, err := c.GetBlock(ctx, 1)
	if err != nil || second == nil {
		t.Fatalf("block 1 after reset: %+v, %v", second, err)
	}
	if first.Hash == second.Hash {
		t.Fatalf("block 1 hash is still %q after a reset: the reset check would never fire", first.Hash)
	}
	if got := srv.hits.Load(); got != 2 {
		t.Errorf("round trips = %d, want 2", got)
	}
}

// An empty getBlocks is "not there yet" as often as it is "not there at all",
// and syncBlocks' floor probe is written to re-test exactly that verdict.
func TestAbsentBlockIsNotCached(t *testing.T) {
	c, srv, _ := newBlockServer(t, 1000)
	ctx := context.Background()

	b, err := c.GetBlock(ctx, 1001)
	if err != nil || b != nil {
		t.Fatalf("block above tip = %+v, %v, want nil, nil", b, err)
	}
	srv.tip.Store(1001)
	b, err = c.GetBlock(ctx, 1001)
	if err != nil {
		t.Fatalf("block after tip advanced: %v", err)
	}
	if b == nil {
		t.Fatal("block 1001 still absent after the tip reached it: an empty answer was cached")
	}
	if got := srv.hits.Load(); got != 2 {
		t.Errorf("round trips = %d, want 2", got)
	}
}

// A range is only final when it came back whole. One that stops short of the
// tip will grow, and must be asked again.
func TestOnlyCompleteRangesAreCached(t *testing.T) {
	c, srv, _ := newBlockServer(t, 1000)
	ctx := context.Background()

	// 995..1005 overruns the tip, so it comes back short and is not kept.
	for i := 0; i < 3; i++ {
		if _, err := c.GetBlocksInRange(ctx, 995, 1005); err != nil {
			t.Fatalf("overrunning range %d: %v", i, err)
		}
	}
	if got := srv.hits.Load(); got != 3 {
		t.Fatalf("round trips for an incomplete range = %d, want 3", got)
	}

	// 100..200 is wholly below the tip and comes back whole.
	srv.hits.Store(0)
	for i := 0; i < 3; i++ {
		blocks, err := c.GetBlocksInRange(ctx, 100, 200)
		if err != nil {
			t.Fatalf("complete range %d: %v", i, err)
		}
		if len(blocks) != 101 {
			t.Fatalf("complete range %d returned %d blocks, want 101", i, len(blocks))
		}
	}
	if got := srv.hits.Load(); got != 1 {
		t.Errorf("round trips for a complete range = %d, want 1", got)
	}
}

// A cached answer outlives the breaker, on purpose: a block that cannot change
// is as true during a 300-second blackout as outside one.
func TestCacheAnswersWhileTheBreakerIsShut(t *testing.T) {
	c, srv, _ := newBlockServer(t, 1000)
	ctx := context.Background()

	if _, err := c.GetBlock(ctx, 500); err != nil {
		t.Fatalf("warm: %v", err)
	}
	c.pauseUntil(time.Now().Add(time.Hour))

	if _, err := c.LatestBlockHeight(ctx); err == nil {
		t.Fatal("tip answered while the breaker was shut, want ErrUnavailable")
	}
	b, err := c.GetBlock(ctx, 500)
	if err != nil || b == nil {
		t.Fatalf("cached block while the breaker was shut = %+v, %v, want the block", b, err)
	}
	if got := srv.hits.Load(); got != 1 {
		t.Errorf("round trips = %d, want 1", got)
	}
}

// A chain reset keeps the heights and changes what is at them, so every stored
// answer is now about a chain that no longer exists.
func TestPurgeDropsEverything(t *testing.T) {
	c, srv, _ := newBlockServer(t, 1000)
	ctx := context.Background()

	first, err := c.GetBlock(ctx, 500)
	if err != nil || first == nil {
		t.Fatalf("before reset: %+v, %v", first, err)
	}
	srv.suffix.Store("b")
	c.PurgeCache()

	second, err := c.GetBlock(ctx, 500)
	if err != nil || second == nil {
		t.Fatalf("after reset: %+v, %v", second, err)
	}
	if first.Hash == second.Hash {
		t.Fatalf("height 500 still reads %q after a purge", first.Hash)
	}
	if st := c.Cache().Stats(); st.Purges != 1 || st.Entries != 1 {
		t.Errorf("stats = %+v, want 1 purge and the one refilled entry", st)
	}
}

// The cache mechanics, away from the chain: hit, miss, expiry, both bounds.
func TestResponseCacheMechanics(t *testing.T) {
	clock := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	data := func(n int) json.RawMessage { return json.RawMessage(strings.Repeat("x", n)) }

	tests := []struct {
		name string
		run  func(t *testing.T, c *ResponseCache, tick func(time.Duration))
	}{
		{
			name: "a stored answer is returned",
			run: func(t *testing.T, c *ResponseCache, _ func(time.Duration)) {
				c.put("k", data(10), cacheForever)
				if got, ok := c.get("k"); !ok || len(got) != 10 {
					t.Fatalf("get = %q, %v, want the stored answer", got, ok)
				}
			},
		},
		{
			name: "an unknown key misses",
			run: func(t *testing.T, c *ResponseCache, _ func(time.Duration)) {
				if _, ok := c.get("nope"); ok {
					t.Fatal("get on an empty cache reported a hit")
				}
				if st := c.Stats(); st.Misses != 1 || st.Hits != 0 {
					t.Fatalf("stats = %+v, want one miss", st)
				}
			},
		},
		{
			name: "a finite lifetime expires at its boundary and not before",
			run: func(t *testing.T, c *ResponseCache, tick func(time.Duration)) {
				c.put("k", data(10), 30*time.Second)
				tick(30 * time.Second)
				if _, ok := c.get("k"); !ok {
					t.Fatal("expired at exactly its lifetime, want still live")
				}
				tick(time.Second)
				if _, ok := c.get("k"); ok {
					t.Fatal("still live one second past its lifetime")
				}
				if st := c.Stats(); st.Expired != 1 {
					t.Fatalf("stats = %+v, want one expiry", st)
				}
			},
		},
		{
			name: "an answer that cannot change has no lifetime to run out",
			run: func(t *testing.T, c *ResponseCache, tick func(time.Duration)) {
				c.put("k", data(10), cacheForever)
				tick(365 * 24 * time.Hour)
				if _, ok := c.get("k"); !ok {
					t.Fatal("an immutable answer expired")
				}
			},
		},
		{
			name: "the entry bound evicts least-recently-used",
			run: func(t *testing.T, c *ResponseCache, _ func(time.Duration)) {
				c.maxEntries = 3
				for _, k := range []string{"a", "b", "c"} {
					c.put(k, data(10), cacheForever)
				}
				c.get("a") // a is now the most recent
				c.put("d", data(10), cacheForever)
				if _, ok := c.get("b"); ok {
					t.Error("b survived, want the least recently used evicted")
				}
				if _, ok := c.get("a"); !ok {
					t.Error("a was evicted despite being the most recently used")
				}
				if st := c.Stats(); st.Entries != 3 || st.Evictions != 1 {
					t.Fatalf("stats = %+v, want 3 entries and 1 eviction", st)
				}
			},
		},
		{
			name: "the byte bound evicts even when there is room for the entry",
			run: func(t *testing.T, c *ResponseCache, _ func(time.Duration)) {
				c.maxBytes = 100
				for i := 0; i < 5; i++ {
					c.put(fmt.Sprintf("k%d", i), data(40), cacheForever)
				}
				st := c.Stats()
				if st.Bytes > 100 {
					t.Fatalf("holding %d bytes against a bound of 100", st.Bytes)
				}
				if st.Entries != 2 {
					t.Fatalf("entries = %d, want 2 (40 bytes each under 100)", st.Entries)
				}
			},
		},
		{
			name: "one oversized answer is refused rather than flushing the rest",
			run: func(t *testing.T, c *ResponseCache, _ func(time.Duration)) {
				c.put("small", data(10), cacheForever)
				c.put("huge", data(maxEntryBytes+1), cacheForever)
				if _, ok := c.get("huge"); ok {
					t.Error("an answer past maxEntryBytes was stored")
				}
				if _, ok := c.get("small"); !ok {
					t.Error("the oversized answer evicted the small one")
				}
			},
		},
		{
			name: "a purge empties it and says so",
			run: func(t *testing.T, c *ResponseCache, _ func(time.Duration)) {
				c.put("k", data(10), cacheForever)
				c.Purge()
				if _, ok := c.get("k"); ok {
					t.Error("an answer survived a purge")
				}
				if st := c.Stats(); st.Entries != 0 || st.Bytes != 0 || st.Purges != 1 || st.LastPurge == "" {
					t.Fatalf("stats = %+v, want empty with one dated purge", st)
				}
			},
		},
		{
			name: "a nil cache misses and stores nothing",
			run: func(t *testing.T, _ *ResponseCache, _ func(time.Duration)) {
				var c *ResponseCache
				c.put("k", data(10), cacheForever)
				if _, ok := c.get("k"); ok {
					t.Error("a nil cache reported a hit")
				}
				c.Purge()
				if st := c.Stats(); st != (reqmeter.CacheStats{}) {
					t.Errorf("stats on a nil cache = %+v, want zero", st)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := clock
			c := NewResponseCache(defaultCacheEntries, defaultCacheBytes)
			c.now = func() time.Time { return now }
			tc.run(t, c, func(d time.Duration) { now = now.Add(d) })
		})
	}
}

// The stored bytes must not alias the caller's buffer: json.RawMessage is a
// slice into a decode buffer that the caller is free to reuse.
func TestStoredAnswerIsCopied(t *testing.T) {
	c := NewResponseCache(defaultCacheEntries, defaultCacheBytes)
	buf := []byte(`{"getBlocks":[]}`)
	c.put("k", buf, cacheForever)
	copy(buf, `{"CORRUPTED":[]}`)
	got, ok := c.get("k")
	if !ok || string(got) != `{"getBlocks":[]}` {
		t.Fatalf("stored answer = %q, want the bytes as they were at put", got)
	}
}
