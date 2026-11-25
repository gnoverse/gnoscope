package reqmeter

import (
	"fmt"
	"testing"
	"time"
)

// A synthetic request series with repeats placed by hand, so the expected hit
// counts can be reasoned about rather than recorded from the implementation.
//
// The case that decides whether this simulates a cache or merely a memory is
// "every four seconds": under a 5s TTL a cache fills at t=0, hits at t=4, and
// has expired by t=8 because its entry is five seconds old and not four. A
// last-sighting counter would call every request after the first a hit and
// report a rate no cache could deliver.
func TestCacheSimWouldHaveHit(t *testing.T) {
	type req struct {
		at        time.Duration
		key       string
		cacheable bool
	}
	ok := func(at time.Duration, key string) req { return req{at: at, key: key, cacheable: true} }

	tests := []struct {
		name string
		reqs []req
		// want is hits per TTL, in the order of TTLs: 5s, 30s, 300s.
		want [len(TTLs)]int64
	}{
		{
			name: "one request can never be a hit",
			reqs: []req{ok(0, "a")},
			want: [len(TTLs)]int64{0, 0, 0},
		},
		{
			name: "never the same question twice",
			reqs: []req{ok(0, "a"), ok(1*time.Second, "b"), ok(2*time.Second, "c")},
			want: [len(TTLs)]int64{0, 0, 0},
		},
		{
			name: "the same question every four seconds, six times",
			reqs: []req{
				ok(0, "a"), ok(4*time.Second, "a"), ok(8*time.Second, "a"),
				ok(12*time.Second, "a"), ok(16*time.Second, "a"), ok(20*time.Second, "a"),
			},
			// 5s: fills at 0, 8 and 16; hits at 4, 12 and 20.
			// 30s and 300s: fills once at 0, hits the other five times.
			want: [len(TTLs)]int64{3, 5, 5},
		},
		{
			name: "exactly on the lifetime is still inside it",
			reqs: []req{ok(0, "a"), ok(5*time.Second, "a")},
			want: [len(TTLs)]int64{1, 1, 1},
		},
		{
			name: "one second past the lifetime is not",
			reqs: []req{ok(0, "a"), ok(6*time.Second, "a")},
			want: [len(TTLs)]int64{0, 1, 1},
		},
		{
			name: "two questions interleaved are two cache entries",
			reqs: []req{
				ok(0, "a"), ok(1*time.Second, "b"),
				ok(2*time.Second, "a"), ok(3*time.Second, "b"),
			},
			want: [len(TTLs)]int64{2, 2, 2},
		},
		{
			name: "a crawler asking the same page every minute for ten minutes",
			reqs: func() []req {
				var out []req
				for i := 0; i < 10; i++ {
					out = append(out, ok(time.Duration(i)*time.Minute, "page"))
				}
				return out
			}(),
			// 5s and 30s expire between every pair, so nothing hits. 300s
			// fills at 0 and at 5 minutes, hitting the four requests after each.
			want: [len(TTLs)]int64{0, 0, 8},
		},
		{
			name: "an error is never a fill, so the retry is not a hit",
			reqs: []req{
				{at: 0, key: "a", cacheable: false},
				{at: 1 * time.Second, key: "a", cacheable: false},
				ok(2*time.Second, "a"),
				ok(3*time.Second, "a"),
			},
			// Only the third request fills anything, so only the fourth hits.
			want: [len(TTLs)]int64{1, 1, 1},
		},
		{
			name: "a success after a failure is cached from the success",
			reqs: []req{
				ok(0, "a"),
				{at: 1 * time.Second, key: "a", cacheable: false},
				ok(2*time.Second, "a"),
			},
			// t=1 hits every TTL (the t=0 fill is still live) and leaves the
			// fill alone, so t=2 hits as well.
			want: [len(TTLs)]int64{2, 2, 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newCacheSim(maxSimKeys)
			var got [len(TTLs)]int64
			for _, r := range tc.reqs {
				hits := s.observe(simKey("https://indexer.example/query", []byte(r.key)), base.Add(r.at), r.cacheable)
				for i, h := range hits {
					if h {
						got[i]++
					}
				}
			}
			if got != tc.want {
				t.Errorf("would-have-hit per TTL %v = %v, want %v", TTLs, got, tc.want)
			}
		})
	}
}

// The simulation's memory is capped, and the cap is allowed to cost accuracy in
// exactly one direction: a key evicted and asked for again reads as a miss a
// larger cache would have hit, so the reported hit rate is a floor. It must
// never read as a hit something that would not have been one.
func TestCacheSimEvictsAndUndercounts(t *testing.T) {
	s := newCacheSim(2)
	k := func(name string) [16]byte { return simKey("https://indexer.example/query", []byte(name)) }

	s.observe(k("a"), base, true)
	s.observe(k("b"), base.Add(time.Second), true)
	// "c" evicts the least recently asked, which is "a".
	s.observe(k("c"), base.Add(2*time.Second), true)

	// "b" survived and is still inside every lifetime, so it hits.
	hits := s.observe(k("b"), base.Add(3*time.Second), true)
	for i, h := range hits {
		if !h {
			t.Errorf("a retained key missed at TTL %s", TTLs[i])
		}
	}
	// "a" did not survive, so it reads as a miss even though an uncapped cache
	// would still have been holding it.
	hits = s.observe(k("a"), base.Add(4*time.Second), true)
	for i, h := range hits {
		if h {
			t.Errorf("an evicted key hit at TTL %s; the simulation must undercount, not overcount", TTLs[i])
		}
	}
	st := s.stats()
	if st.tracked > 2 {
		t.Errorf("tracked %d keys with a capacity of 2", st.tracked)
	}
	if st.evictions == 0 {
		t.Error("nothing was evicted, so the cap is not doing anything")
	}
}

// The identity being simulated is url plus body, so the same query against two
// endpoints is two cache entries. Two chains asked the same question are two
// different answers, and a cache keyed on the body alone would serve one
// network's data for the other.
func TestCacheSimKeysOnEndpointAndBody(t *testing.T) {
	s := newCacheSim(maxSimKeys)
	body := []byte(`{"query":"{ latestBlockHeight }"}`)
	s.observe(simKey("https://indexer.gno.land/graphql/query", body), base, true)
	hits := s.observe(simKey("https://indexer.onyx.testnets.gno.land/graphql/query", body), base.Add(time.Second), true)
	for i, h := range hits {
		if h {
			t.Errorf("the same query at a different endpoint hit at TTL %s", TTLs[i])
		}
	}
}

// Enough traffic to prove the cap holds under the load the budget allows: the
// ceiling is 3,100 requests per 300 seconds, so the working set for the longest
// lifetime cannot exceed that.
func TestCacheSimStaysBounded(t *testing.T) {
	s := newCacheSim(maxSimKeys)
	for i := 0; i < 50_000; i++ {
		s.observe(simKey("https://indexer.example/query", []byte(fmt.Sprint(i))), base.Add(time.Duration(i)*time.Millisecond), true)
	}
	st := s.stats()
	if st.tracked > int64(maxSimKeys) {
		t.Fatalf("tracked %d keys, capacity %d", st.tracked, maxSimKeys)
	}
	if st.observed != 50_000 {
		t.Fatalf("observed %d requests, want 50000", st.observed)
	}
}
