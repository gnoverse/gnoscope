// Package reqmeter counts what this process spends at the indexer endpoints.
//
// The public gno indexers sit behind one Cloudflare rate-limit bucket:
// BudgetRequests requests per BudgetWindow per IP, counted across every
// `indexer.` hostname under gno.land together, not per hostname and not per
// network. Going one over costs a BudgetWindow blackout, because the rule's
// block cannot be shorter than its own period. That is about 10.3 requests per
// second sustained, shared between every chain this explorer reads.
//
// Nothing in this binary knew how much of that it was spending. #526 removed
// the 429s by honouring Retry-After and collapsing concurrent identical
// queries, and the question left open on #525 is whether a short-TTL response
// cache would remove the problem outright rather than treat it. That question
// cannot be answered without the number, so this package produces it: not once,
// permanently, from the one place every outbound GraphQL request goes through.
//
// Four things, in the order they get asked:
//
//  1. How many requests, and how many of those never reached the wire because
//     singleflight collapsed them. The second figure is the upside of caching
//     that is already being realised.
//  2. At what rate. A cumulative counter since process start answers the wrong
//     question: the budget is spent in bursts, and a daily average under the
//     ceiling while a single minute goes 4x over is exactly the shape that
//     earns a 429. So: a rolling window, and the peak within it.
//  3. Which queries. Grouped by the GraphQL operation name, or the first root
//     field where there is none, which is what this codebase's queries
//     actually carry. A cache that covers the top three may be the whole fix,
//     and without the grouping nobody can tell which three those are.
//  4. What a cache would have saved. For each group, how many requests asked
//     for an identical url plus body that had already been answered within the
//     last TTLs[i] seconds. That is the hit rate the cache would have had,
//     measured without building the cache.
//
// The meter must not itself be a cost. Everything here is bounded: see Bounds
// and the constants it reports.
package reqmeter

import (
	"crypto/sha256"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The ceiling this is all measured against. From the gno infrastructure
// configuration, recorded on #525: one Cloudflare rule, per IP, counting every
// `indexer.` hostname under gno.land together.
const (
	BudgetRequests = 3100
	BudgetWindow   = 300 * time.Second
)

// TTLs are the cache lifetimes the simulation answers for. Three, because one
// number would not show the shape: 5s says how much of the traffic is a
// genuine stampede, 300s says how much of it is a crawler or a polling tab
// asking the same question all afternoon, and 30s is the value a cautious
// first cut would actually ship with.
var TTLs = [3]time.Duration{5 * time.Second, 30 * time.Second, 300 * time.Second}

// sharedBucketSuffix is the DNS suffix whose indexer hostnames share the
// budget above.
//
// Why a suffix and not "every endpoint": mainnet's reads can be served by
// onbloc, which is a different origin and off that Cloudflare edge entirely.
// That is measured, and it is why mainnet never 429s while onyx always did. An
// instance configured with onbloc first spends almost nothing against the
// shared bucket while making plenty of requests, so a meter that summed every
// endpoint into one verdict would report a budget problem that does not exist.
const sharedBucketSuffix = "gno.land"

// What bounds this meter's memory. Every one of these is a hard cap, and none
// of them is keyed by anything an outsider controls.
const (
	// maxEndpoints caps the per-endpoint breakdown. Endpoint URLs come from
	// the operator's own network config, so the real bound is the config; this
	// is the backstop that makes that not matter.
	maxEndpoints = 16
	// maxGroups caps the per-endpoint operation breakdown. Labels are read
	// from query text this repo writes, and label() skips string literals so a
	// value interpolated into a query cannot become one. This is the backstop
	// for that too.
	maxGroups = 48
	// maxSimKeys caps the would-have-hit simulation's LRU. At the 10.3 req/s
	// the budget allows, the working set for the longest TTL (300s) is at most
	// about 3,100 distinct requests, so this holds it with room over.
	maxSimKeys = 8192

	overflowEndpoint = "(other endpoints)"
	overflowGroup    = "(other)"
)

// Outcome is what the wire said.
type Outcome int

const (
	// OutcomeOK is any HTTP 200. A 200 carrying a GraphQL-level error counts
	// here and not under Failed: the upstream answered, and the budget was
	// spent either way, which is the only thing this package is measuring.
	OutcomeOK Outcome = iota
	// OutcomeRefused is a 429. Counted apart from every other failure because
	// it is the one the budget is about.
	OutcomeRefused
	// OutcomeFailed is a transport error, a non-200 that is not a 429, or a
	// body that would not decode.
	OutcomeFailed
)

// Request is one round trip that actually happened.
type Request struct {
	// URL is the endpoint it went to.
	URL string
	// Label names the operation; see Label.
	Label string
	// Body is the exact request bytes. Hashed, never retained.
	Body []byte
	// Bytes is the size of the response body.
	Bytes int
	// Outcome is what came back.
	Outcome Outcome
	// Cacheable says whether a response cache would have stored this answer.
	// A refusal, a transport failure and a GraphQL error would not be stored,
	// so they must not be allowed to count as a cache fill: a cache that
	// remembered errors would report a hit rate it could never deliver.
	Cacheable bool
}

// Meter is the counter. The zero value is not usable; call New.
type Meter struct {
	mu sync.Mutex
	// now is the clock, swappable so the rolling window can be tested at a
	// bucket boundary rather than by sleeping through one.
	now   func() time.Time
	since time.Time

	eps   map[string]*endpointCounts
	order []string

	// allWin and sharedWin are aggregate windows, kept rather than derived.
	// The peak of a sum is not the sum of the peaks: two endpoints whose busy
	// minutes do not coincide would report a combined peak far above anything
	// that ever happened if their individual peaks were added.
	allWin    window
	sharedWin window

	sim *cacheSim
}

type endpointCounts struct {
	url    string
	shared bool
	win    window
	totals totals
	groups map[string]*groupCounts
	order  []string
}

type groupCounts struct {
	label  string
	totals totals
	// hits is, per TTL, how many wire requests would have been answered from a
	// cache of that lifetime instead.
	hits [len(TTLs)]int64
}

type totals struct {
	asked     int64
	sent      int64
	refused   int64
	failed    int64
	bytes     int64
	collapsed int64
}

// New returns a meter reading the wall clock.
func New() *Meter { return newMeter(time.Now) }

func newMeter(now func() time.Time) *Meter {
	return &Meter{
		now:   now,
		since: now().UTC(),
		eps:   make(map[string]*endpointCounts, maxEndpoints),
		sim:   newCacheSim(maxSimKeys),
	}
}

// defaultMeter is the process-wide meter, which is the right scope because the
// budget is per IP: one bucket for the serve client, the sync client and every
// configured network together. A per-client meter would answer a question
// nobody asked.
var (
	defaultOnce sync.Once
	defaultM    *Meter
)

// Default returns the process-wide meter.
func Default() *Meter {
	defaultOnce.Do(func() { defaultM = New() })
	return defaultM
}

// Asked records one logical request: a caller wanting an answer, before
// singleflight has decided whether that costs a round trip.
//
// Asked minus Sent is what collapsing saved, which is why both are counted
// rather than only the one on the wire.
func (m *Meter) Asked(endpoint, label string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ep := m.endpoint(endpoint)
	g := ep.group(label)
	ep.totals.asked++
	g.totals.asked++
	m.recollapse(ep, g)
}

// recollapse keeps the collapsed figure as the difference it is. Called with
// m.mu held, after either counter moves.
func (m *Meter) recollapse(ep *endpointCounts, g *groupCounts) {
	ep.totals.collapsed = max(0, ep.totals.asked-ep.totals.sent)
	g.totals.collapsed = max(0, g.totals.asked-g.totals.sent)
}

// Sent records one round trip that reached the wire.
func (m *Meter) Sent(r Request) {
	if m == nil {
		return
	}
	now := m.now()
	hits := m.sim.observe(simKey(r.URL, r.Body), now, r.Cacheable)

	m.mu.Lock()
	defer m.mu.Unlock()
	ep := m.endpoint(r.URL)
	g := ep.group(r.Label)

	refused := r.Outcome == OutcomeRefused
	for _, t := range []*totals{&ep.totals, &g.totals} {
		t.sent++
		t.bytes += int64(r.Bytes)
		switch r.Outcome {
		case OutcomeRefused:
			t.refused++
		case OutcomeFailed:
			t.failed++
		}
	}
	for i, hit := range hits {
		if hit {
			g.hits[i]++
		}
	}
	ep.win.add(now, refused)
	m.allWin.add(now, refused)
	if ep.shared {
		m.sharedWin.add(now, refused)
	}
	m.recollapse(ep, g)
}

// endpoint finds or creates an endpoint's counters. Called with m.mu held.
func (m *Meter) endpoint(raw string) *endpointCounts {
	if ep, ok := m.eps[raw]; ok {
		return ep
	}
	// Fold at one below the cap, so the overflow bucket is itself the last
	// slot rather than one past it.
	if len(m.eps) >= maxEndpoints-1 {
		raw = overflowEndpoint
		if ep, ok := m.eps[raw]; ok {
			return ep
		}
	}
	ep := &endpointCounts{
		url:    raw,
		shared: OnSharedBucket(raw),
		groups: make(map[string]*groupCounts, 8),
	}
	m.eps[raw] = ep
	m.order = append(m.order, raw)
	return ep
}

func (e *endpointCounts) group(label string) *groupCounts {
	if label == "" {
		label = UnknownLabel
	}
	if g, ok := e.groups[label]; ok {
		return g
	}
	if len(e.groups) >= maxGroups-1 {
		label = overflowGroup
		if g, ok := e.groups[label]; ok {
			return g
		}
	}
	g := &groupCounts{label: label}
	e.groups[label] = g
	e.order = append(e.order, label)
	return g
}

// OnSharedBucket reports whether an endpoint spends the shared per-IP budget.
//
// An endpoint whose URL will not parse counts as shared: over-reporting what we
// spend against a ceiling is the safe direction to be wrong in.
func OnSharedBucket(raw string) bool {
	if raw == overflowEndpoint {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return true
	}
	h := strings.ToLower(u.Hostname())
	return h == sharedBucketSuffix || strings.HasSuffix(h, "."+sharedBucketSuffix)
}

func simKey(endpoint string, body []byte) [16]byte {
	h := sha256.New()
	h.Write([]byte(endpoint))
	h.Write([]byte{0})
	h.Write(body)
	var out [16]byte
	copy(out[:], h.Sum(nil))
	return out
}
