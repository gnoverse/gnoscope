package httpapi

import (
	"context"
	"sync"
	"time"
)

// memo is the in-process value cache this package's upstream reads share.
//
// Every one of those reads used to hand-roll the same shape: a map, a fetched-at
// map, a mutex, lock/read/unlock, fetch, lock/store/unlock. Correct, and wrong
// in the same two ways each time.
//
// **It dogpiled.** Unlocking before the fetch is what keeps a slow upstream from
// blocking readers of other keys, but it also means N concurrent readers of one
// cold key run N identical fetches. On a cold /api/govdao/overview that is N
// copies of thirty-odd ABCI round trips, competing for the same node, so each
// one is slower than it would have been alone. Measured on 2026-09-24: four
// concurrent requests took 2.39s, 2.79s, 3.03s and 3.13s where one took 2.39s.
//
// **It never remembered a failure.** The error paths returned the last good
// value without stamping a time, so an upstream that is down, or a lookup whose
// answer is legitimately "no such thing", was retried on every single request
// forever. resolveGnoUsernameCached was the worst case: an author whose name is
// not in r/sys/users cost one full RPC round trip per proposal per request, with
// nothing ever cached because there was nothing good to cache.
//
// memo fixes both once. A success is served for ttl; a failure holds retries off
// for negativeTTL while still serving the last good value, if there is one; and
// concurrent callers for one key produce one fetch.
type memo[V any] struct {
	mu       sync.Mutex
	entries  map[string]memoEntry[V]
	inflight map[string]chan struct{}

	ttl         time.Duration
	negativeTTL time.Duration
	// staleGrace is how far past ttl a good value may still be served while
	// its replacement is computed behind the caller. Zero keeps the original
	// behaviour, where whoever finds an expired entry waits for the refetch.
	//
	// That wait is the whole reason this exists. Measured against production
	// on 2026-10-20, after the gov/dao call list was given a memo: a proposal
	// page answered in 0.40s while the list was fresh and 13.6s for the one
	// reader who happened to arrive after its 60s lapsed, because that reader
	// ran the 8s scan themselves. The response cache has solved this since it
	// was written (see CacheStaleGrace); the memo under it had not.
	staleGrace time.Duration
}

type memoEntry[V any] struct {
	val  V
	good bool // a real value landed here at least once
	// retryAt is the earliest moment this key is fetched again. A success sets
	// it to now+ttl, a failure to now+negativeTTL, which is the whole
	// difference between the two.
	retryAt time.Time
}

// newMemo returns a memo whose successes live for ttl and whose failures are
// not retried for negativeTTL.
//
// negativeTTL should be well under ttl: the cost of holding a failure too long
// is a page that stays blank after the upstream recovered, and the cost of
// holding it too briefly is the retry storm this type exists to stop.
func newMemo[V any](ttl, negativeTTL time.Duration) *memo[V] {
	return &memo[V]{
		entries:     map[string]memoEntry[V]{},
		inflight:    map[string]chan struct{}{},
		ttl:         ttl,
		negativeTTL: negativeTTL,
	}
}

// newStaleWhileRevalidateMemo is newMemo plus a grace: an expired value is
// served immediately and replaced behind the caller, so the cost of a refetch
// lands on nobody.
//
// For the reads where being a little out of date is cheap and waiting is not.
// Sized in minutes rather than seconds, like CacheStaleGrace and for the same
// reason: the job is to cover the gap between two visitors, and on this
// explorer that gap is minutes. Past ttl+grace the value is treated as too old
// to serve and the caller waits, so a reader returning after an hour away is
// not shown an hour-old answer however fast.
func newStaleWhileRevalidateMemo[V any](ttl, negativeTTL, staleGrace time.Duration) *memo[V] {
	m := newMemo[V](ttl, negativeTTL)
	m.staleGrace = staleGrace
	return m
}

// memoRefreshTimeout bounds a refresh nobody is waiting on. Matches
// backgroundRefreshTimeout, which bounds the response cache's equivalent.
const memoRefreshTimeout = 60 * time.Second

// get returns the value for key, running fetch at most once across concurrent
// callers.
//
// fetch reports whether its result is worth storing. Returning false is not an
// error signal so much as a "do not overwrite what you have with this": a
// partial render, an RPC timeout, a name that does not resolve. memo keeps the
// previous good value in that case and returns it, so a page that was working a
// moment ago does not blank out over one bad round trip.
//
// A caller that arrives while another is fetching waits for it, and honours its
// own ctx while waiting: a reader who closes the tab stops waiting, and does not
// cancel the fetch, which belongs to whoever started it.
func (m *memo[V]) get(ctx context.Context, key string, fetch func(context.Context) (V, bool)) V {
	for {
		m.mu.Lock()
		entry, held := m.entries[key]
		if held && time.Now().Before(entry.retryAt) {
			m.mu.Unlock()
			return entry.val
		}
		// Expired, but good and not yet too old: hand it over and replace it
		// behind this caller. The refresh runs on its own context, because the
		// caller already has their answer and may be gone a millisecond later,
		// and cancelling the refresh they triggered would leave the next
		// caller to pay for it instead.
		if held && entry.good && m.staleGrace > 0 && time.Now().Before(entry.retryAt.Add(m.staleGrace)) {
			if _, running := m.inflight[key]; !running {
				done := make(chan struct{})
				m.inflight[key] = done
				go m.refresh(key, done, fetch)
			}
			m.mu.Unlock()
			return entry.val
		}
		if wait, running := m.inflight[key]; running {
			m.mu.Unlock()
			select {
			case <-wait:
				// Loop rather than read the entry directly: the leader may
				// have stored nothing, in which case this caller becomes the
				// next leader instead of returning a zero value.
				m.mu.Lock()
				next, ok := m.entries[key]
				_, stillRunning := m.inflight[key]
				m.mu.Unlock()
				if ok && (next.good || time.Now().Before(next.retryAt)) {
					return next.val
				}
				if stillRunning {
					continue
				}
			case <-ctx.Done():
				return entry.val
			}
		}
		done := make(chan struct{})
		m.inflight[key] = done
		m.mu.Unlock()

		val, ok := fetch(ctx)

		m.mu.Lock()
		m.store(key, val, ok)
		entry = m.entries[key]
		delete(m.inflight, key)
		close(done)
		m.mu.Unlock()
		return entry.val
	}
}

// refresh recomputes one key in the background, on a context of its own, and
// stores the result under the same rules a foreground fetch uses. The caller
// holds m.mu and has already claimed the key in m.inflight.
func (m *memo[V]) refresh(key string, done chan struct{}, fetch func(context.Context) (V, bool)) {
	ctx, cancel := context.WithTimeout(context.Background(), memoRefreshTimeout)
	defer cancel()
	val, ok := fetch(ctx)

	m.mu.Lock()
	m.store(key, val, ok)
	delete(m.inflight, key)
	close(done)
	m.mu.Unlock()
}

// store writes a fetch result under the hit/miss rules. The caller holds m.mu.
func (m *memo[V]) store(key string, val V, ok bool) {
	entry := m.entries[key]
	now := time.Now()
	switch {
	case ok:
		entry = memoEntry[V]{val: val, good: true, retryAt: now.Add(m.ttl)}
	case entry.good:
		// Keep the last good value, but do not keep asking for a
		// replacement on every request.
		entry.retryAt = now.Add(m.negativeTTL)
	default:
		// Nothing good has ever landed here. Remember the failure itself,
		// so "this name does not resolve" costs one round trip per
		// negativeTTL rather than one per request.
		entry = memoEntry[V]{val: val, retryAt: now.Add(m.negativeTTL)}
	}
	m.entries[key] = entry
}

// reset drops every entry. For tests, and for a caller that knows the world
// changed under it in a way no TTL can express.
func (m *memo[V]) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = map[string]memoEntry[V]{}
}

// expireAll makes every key eligible for a fetch on the next get without
// dropping the values it holds. That distinction is the whole last-good
// behaviour, so it is also the only way to test it.
func (m *memo[V]) expireAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	past := time.Now().Add(-time.Second)
	for k, e := range m.entries {
		e.retryAt = past
		m.entries[k] = e
	}
}

// peek returns the stored value without fetching. For callers that want to know
// whether a key is already warm, and for tests.
func (m *memo[V]) peek(key string) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	return e.val, ok && e.good
}
