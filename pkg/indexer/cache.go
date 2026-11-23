package indexer

// The response cache #525 asked for, and the reason most of it never expires.
//
// #529 measured where the budget goes. Over the first hour of a production
// instance: 35,235 outbound requests, 92% of them `getBlocks`, 85 to 87% of
// those a repeat of a question already answered within five minutes, and only
// 0.16% collapsed by singleflight. The duplicates are not concurrent; they are
// the same block asked again on the next sync tick. So the fix is a cache and
// not more collapsing.
//
// What makes it easy: a finalised block is immutable. `getBlocks` at a height
// below the tip can never change its answer, so the right lifetime for that
// traffic is not five seconds and not five minutes, it is indefinite, bounded
// only by memory. Tendermint2 finalises in one slot, so there is no reorg to
// invalidate against either. What there IS is a chain reset, which keeps the
// chain ID and comes back with different blocks at the same heights, and that
// is what Purge and the syncer's reset check are for.
//
// Nothing is cached by default. A rule is attached at the call site, by a
// caller that knows what it is asking for, and the default rule caches nothing
// at all. A cache that had to guess from query text which answers are
// immutable would be one typo away from serving stale chain data, which is a
// worse failure than a 429 because nothing reports it.

import (
	"container/list"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/gnoverse/gnoscope/pkg/reqmeter"
)

// cacheForever marks an answer that cannot change. It is a sentinel and not a
// very large duration: "a finalised block is immutable" is a statement about
// the chain, and writing it as a hundred years would invite someone to read it
// as a tuning parameter.
const cacheForever time.Duration = -1

// What bounds the cache. No flag: these are the whole memory story, and an
// operator who cannot see them in the response has no way to reason about
// them, so Stats reports every one of them beside the live figures.
const (
	// defaultCacheEntries caps how many answers are held. Measured on
	// production, one hour: 1,418 distinct request digests behind 35,235
	// requests. 20,000 holds a day of that working set with room over.
	defaultCacheEntries = 20000
	// defaultCacheBytes caps the total. A single-block answer is ~310 bytes
	// measured (6.6 MB over 21,684 requests), so the entry cap binds first on
	// the traffic that matters and this is the backstop for the rest.
	defaultCacheBytes int64 = 32 << 20
	// maxEntryBytes keeps one answer from evicting thousands of others. It
	// also draws a useful line by itself: a 5,000-block backfill page is
	// ~684 KB and is walked exactly once, so storing it would be pure cost,
	// while a reader's 50-block page of /blocks is ~7 KB and is walked by
	// every crawler that ever pages back through history.
	maxEntryBytes = 256 << 10
)

// cacheRule says what may be kept from one query's answer, and for how long.
//
// The zero value caches nothing, which is why every query path that has not
// thought about immutability gets exactly the behaviour it had before.
type cacheRule struct {
	// ttl is 0 to bypass the cache entirely, cacheForever for an answer that
	// cannot change, or a positive lifetime.
	ttl time.Duration
	// store inspects the raw `data` object before it is kept, and is the
	// difference between "this query is cacheable" and "this ANSWER is
	// cacheable". A block that is not there yet and a range that stops short
	// of the tip are both answers that will be different tomorrow. nil keeps
	// any successful answer.
	store func(json.RawMessage) bool
}

func (r cacheRule) on() bool { return r.ttl != 0 }

// keeps reports whether an answer may be stored under this rule.
func (r cacheRule) keeps(data json.RawMessage) bool {
	if !r.on() || len(data) == 0 || len(data) > maxEntryBytes {
		return false
	}
	return r.store == nil || r.store(data)
}

// blocksFound keeps an answer only when the indexer actually returned a block.
//
// An empty `getBlocks` is not "there is no block here forever". It is also what
// a height above the tip, a replica still catching up and a pruned floor all
// look like, and the first of those becomes a real block a few seconds later.
// Caching it would freeze syncBlocks' floor probe on a verdict it is explicitly
// written to re-test.
func blocksFound(data json.RawMessage) bool { return countBlocks(data) > 0 }

// rangeComplete keeps a range only when every height in it came back.
//
// This is what makes an indefinite lifetime safe without the caller knowing the
// tip. Blocks are contiguous, so a range that returned all of its heights lies
// wholly below the tip and is final. A range that returned fewer either runs
// past the tip and will grow, or sits under a pruned floor that a different
// endpoint may yet answer for. Neither is kept.
func rangeComplete(want int) func(json.RawMessage) bool {
	return func(data json.RawMessage) bool {
		return want > 0 && countBlocks(data) == want
	}
}

// countBlocks counts the rows in a getBlocks answer, or -1 if it will not parse.
func countBlocks(data json.RawMessage) int {
	var v struct {
		GetBlocks []json.RawMessage `json:"getBlocks"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return -1
	}
	return len(v.GetBlocks)
}

// ResponseCache holds decoded-ready GraphQL `data` objects keyed by the exact
// request body that would have been sent.
//
// Keyed by body and not by (endpoint, body), unlike the singleflight key one
// layer down. The endpoints of one pool are interchangeable members proven to
// serve the same chain — selectEndpoint excludes any that disagrees — so a
// finalised block fetched from one is the same bytes from another, and leaving
// the URL out means a failover does not re-pay for history it already has.
// Queries that are pinned to an endpoint for its schema carry different field
// sets, so they carry different bodies and key apart on their own.
type ResponseCache struct {
	name string

	maxEntries int
	maxBytes   int64

	mu    sync.Mutex
	ll    *list.List
	byKey map[string]*list.Element
	bytes int64

	hits      int64
	misses    int64
	stores    int64
	evictions int64
	expired   int64
	purges    int64
	purgedAt  time.Time

	// now is the clock, swappable so expiry can be tested at the boundary
	// rather than by sleeping through it.
	now func() time.Time
}

type cacheEntry struct {
	key  string
	data json.RawMessage
	// expires is the zero time for an answer that cannot change.
	expires time.Time
	size    int
}

// NewResponseCache returns an empty cache with the given bounds. Exported for
// tests, which want one of their own rather than a pool's shared instance.
func NewResponseCache(maxEntries int, maxBytes int64) *ResponseCache {
	return &ResponseCache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		ll:         list.New(),
		byKey:      make(map[string]*list.Element),
		now:        time.Now,
	}
}

// get returns a stored answer. A nil cache misses, so a Client built as a
// literal needs no wiring.
func (c *ResponseCache) get(key string) (json.RawMessage, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[key]
	if !ok {
		c.misses++
		return nil, false
	}
	e := el.Value.(*cacheEntry)
	if !e.expires.IsZero() && c.now().After(e.expires) {
		c.drop(el)
		c.expired++
		c.misses++
		return nil, false
	}
	c.ll.MoveToFront(el)
	c.hits++
	return e.data, true
}

// put stores an answer. ttl is cacheForever for one that cannot change.
func (c *ResponseCache) put(key string, data json.RawMessage, ttl time.Duration) {
	if c == nil || len(data) == 0 || len(data) > maxEntryBytes {
		return
	}
	// The answer is handed to every later reader, so it is copied rather than
	// aliased: json.RawMessage is a slice into a buffer this caller still owns.
	stored := make(json.RawMessage, len(data))
	copy(stored, data)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Inside the lock, like every other read of c.now, so a test swapping the
	// clock has one place to think about rather than two.
	var expires time.Time
	if ttl > 0 {
		expires = c.now().Add(ttl)
	}
	if el, ok := c.byKey[key]; ok {
		e := el.Value.(*cacheEntry)
		c.bytes += int64(len(stored) - e.size)
		e.data, e.size, e.expires = stored, len(stored), expires
		c.ll.MoveToFront(el)
		c.stores++
		c.trim()
		return
	}
	e := &cacheEntry{key: key, data: stored, expires: expires, size: len(stored)}
	c.byKey[key] = c.ll.PushFront(e)
	c.bytes += int64(e.size)
	c.stores++
	c.trim()
}

// trim evicts least-recently-used entries until both bounds hold. Called with
// c.mu held.
func (c *ResponseCache) trim() {
	for c.ll.Len() > c.maxEntries || c.bytes > c.maxBytes {
		back := c.ll.Back()
		if back == nil {
			return
		}
		c.drop(back)
		c.evictions++
	}
}

// drop removes one element. Called with c.mu held.
func (c *ResponseCache) drop(el *list.Element) {
	e := el.Value.(*cacheEntry)
	c.ll.Remove(el)
	delete(c.byKey, e.key)
	c.bytes -= int64(e.size)
}

// Purge empties the cache.
//
// Called when the chain under it changed identity. A cache keyed on a block
// range that survives a testnet reset serves the old chain's blocks,
// confidently and with nothing anywhere saying so, which is the same shape of
// failure #525 recorded one layer down for a stale node defining a pool's
// identity. The syncer's reset check is the one caller; see checkChainReset.
func (c *ResponseCache) Purge() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.byKey = make(map[string]*list.Element)
	c.bytes = 0
	c.purges++
	c.purgedAt = c.now().UTC()
}

// Stats is what the cache reports about itself, in the shape /api/indexer/usage
// serves it.
func (c *ResponseCache) Stats() reqmeter.CacheStats {
	if c == nil {
		return reqmeter.CacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := reqmeter.CacheStats{
		Name:       c.name,
		Entries:    c.ll.Len(),
		Bytes:      c.bytes,
		MaxEntries: c.maxEntries,
		MaxBytes:   c.maxBytes,
		Hits:       c.hits,
		Misses:     c.misses,
		Stores:     c.stores,
		Evictions:  c.evictions,
		Expired:    c.expired,
		Purges:     c.purges,
	}
	if total := c.hits + c.misses; total > 0 {
		s.HitPercent = float64(int64(10000*float64(c.hits)/float64(total)+0.5)) / 100
	}
	if !c.purgedAt.IsZero() {
		s.LastPurge = c.purgedAt.Format(time.RFC3339)
	}
	return s
}

// One cache per pool, shared by every client built on the same endpoints.
//
// The serve client and the sync client for a network are deliberately separate
// Clients (separate timeouts, separate breakers) but they read one chain, and
// the sync loop is what fetches most of the history a page later asks for. A
// per-client cache would make the reader pay again for a block the syncer
// fetched a second earlier.
//
// Looked up here rather than wired in main.go on purpose. #528 shipped a
// feature that recorded nothing until a flag was added to the unit file, and a
// cache that silently does nothing because one constructor was missed is the
// same failure. There is nothing to wire and nothing to forget.
var (
	poolCachesMu sync.Mutex
	poolCaches   = map[string]*ResponseCache{}
)

// maxPoolCaches bounds the registry. Pools come from the operator's network
// config, so the real bound is the config; this is the backstop, and it matches
// the meter's own endpoint cap.
const maxPoolCaches = 16

func cacheForPool(urls []string) *ResponseCache {
	key := strings.Join(urls, "\x00")
	poolCachesMu.Lock()
	defer poolCachesMu.Unlock()
	if c, ok := poolCaches[key]; ok {
		return c
	}
	if len(poolCaches) >= maxPoolCaches {
		// Past the cap a pool gets its own unregistered cache rather than
		// nothing: correctness does not depend on the registry, only sharing
		// and reporting do.
		return NewResponseCache(defaultCacheEntries, defaultCacheBytes)
	}
	c := NewResponseCache(defaultCacheEntries, defaultCacheBytes)
	c.name = poolName(urls)
	poolCaches[key] = c
	reqmeter.Default().RegisterCache(c.Stats)
	return c
}

// poolName labels a cache by the host of its first endpoint, which is the name
// an operator reading /api/indexer/usage recognises.
func poolName(urls []string) string {
	if len(urls) == 0 {
		return "(no endpoint)"
	}
	u := urls[0]
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	return u
}
