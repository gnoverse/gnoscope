package reqmeter

import (
	"container/list"
	"sync"
	"time"
)

// The cache that was not built.
//
// #525 asks whether a short-TTL response cache would remove the rate-limit
// problem outright. The honest way to answer that is not to argue about which
// read paths are immutable; it is to count how many requests a cache would have
// served, against the traffic that actually happened. So every wire request is
// run past a simulation of a cache that was never installed, at three
// lifetimes, and the answer is the hit rate that cache would have had.
//
// What is simulated is a cache, not a memory. The distinction decides the
// number. A naive "have I seen this in the last N seconds" counts a request at
// t=0, t=4 and t=8 as two hits under a 5s TTL; a real 5s cache fills at t=0,
// hits at t=4 and has expired by t=8, so the answer is one hit and a refill.
// Tracking the fill instant rather than the last sighting is what makes this
// report a rate the cache could actually deliver.
//
// The figure is a floor, for two reasons worth stating because both make it
// read low rather than high: the LRU is capped (see maxSimKeys), so a key
// evicted and asked for again counts as a miss a larger cache would have hit,
// and only requests that reached the wire are simulated, since the ones
// singleflight already collapsed are savings the cache cannot claim twice.
type cacheSim struct {
	mu   sync.Mutex
	cap  int
	ll   *list.List
	keys map[[16]byte]*list.Element

	observed  int64
	evictions int64
}

// simEntry is one request identity. It holds no body and no URL, only the
// digest of both, so the memory cost per key does not depend on how large the
// queries are.
type simEntry struct {
	key [16]byte
	// fill is, per TTL, when a cache of that lifetime last stored an answer to
	// this request. Zero means it holds nothing.
	fill [len(TTLs)]time.Time
}

func newCacheSim(capacity int) *cacheSim {
	return &cacheSim{
		cap:  capacity,
		ll:   list.New(),
		keys: make(map[[16]byte]*list.Element, capacity),
	}
}

// observe runs one wire request past the simulation and reports, per TTL,
// whether a cache of that lifetime would have answered it instead.
//
// cacheable says whether the response that came back is one a cache would have
// stored. A refusal or an error is still observed (it is still a request that
// would have been made, or not, under a cache) but it never becomes a fill: a
// simulation that cached errors would promise a hit rate on repeated failures,
// which is both wrong and the opposite of useful.
func (s *cacheSim) observe(key [16]byte, now time.Time, cacheable bool) (hits [len(TTLs)]bool) {
	if s == nil {
		return hits
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed++

	el, ok := s.keys[key]
	if !ok {
		// Nothing known about this request, so nothing could have been served
		// from a cache. Only remember it if there is now something to remember.
		if !cacheable {
			return hits
		}
		e := &simEntry{key: key}
		for i := range e.fill {
			e.fill[i] = now
		}
		s.keys[key] = s.ll.PushFront(e)
		s.evict()
		return hits
	}

	s.ll.MoveToFront(el)
	e := el.Value.(*simEntry)
	for i, ttl := range TTLs {
		if !e.fill[i].IsZero() && !now.After(e.fill[i].Add(ttl)) {
			// Inside the lifetime: the cache answers, and its entry keeps the
			// fill instant it already had. Refreshing it here is the bug that
			// turns a cache into a memory.
			hits[i] = true
			continue
		}
		if cacheable {
			e.fill[i] = now
		} else {
			e.fill[i] = time.Time{}
		}
	}
	return hits
}

// evict drops the least recently asked key. Called with s.mu held.
func (s *cacheSim) evict() {
	for s.ll.Len() > s.cap {
		back := s.ll.Back()
		if back == nil {
			return
		}
		s.ll.Remove(back)
		delete(s.keys, back.Value.(*simEntry).key)
		s.evictions++
	}
}

type simStats struct {
	observed  int64
	tracked   int64
	evictions int64
	capacity  int
}

func (s *cacheSim) stats() simStats {
	if s == nil {
		return simStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return simStats{
		observed:  s.observed,
		tracked:   int64(s.ll.Len()),
		evictions: s.evictions,
		capacity:  s.cap,
	}
}
