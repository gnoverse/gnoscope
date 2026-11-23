package reqmeter

import (
	"fmt"
	"sort"
	"time"
)

// What the meter reports, and the words it reports it in.
//
// The number alone is not actionable, so none of it is served naked: every rate
// here comes with the ceiling it is measured against and a one-word verdict, and
// the verdict is written here rather than in the browser so that the API, the
// page and anything reading this later all say the same thing about the same
// traffic.

// Verdicts. Deliberately three rather than two: "near" is the state an operator
// can still act on, and a boolean over/under would only ever go true after the
// 300-second blackout had already started.
const (
	VerdictUnder = "under"
	VerdictNear  = "near"
	VerdictOver  = "over"
)

// nearFraction is where "under" becomes "near": 70% of the budget consumed in
// one window. Below that a burst has to roughly double before it costs
// anything; above it, one unlucky crawler does it.
const nearFraction = 0.70

// Snapshot is one reading of the meter.
type Snapshot struct {
	GeneratedAt string `json:"generated_at"`
	// Since is when this process started counting. It is what keeps every
	// figure below honest: "peak minute in the last 24 hours" from a meter that
	// has been up for eleven minutes is a peak over eleven minutes, and reads
	// identically.
	Since         string `json:"since"`
	UptimeSeconds int    `json:"uptime_seconds"`

	// Budget is the ceiling, restated in the response so a reader does not have
	// to know it.
	Budget Budget `json:"budget"`

	// Shared is the traffic that actually spends the shared per-IP budget, and
	// is the figure the verdict is drawn from. All is every endpoint, including
	// origins that are off that edge.
	Shared Usage `json:"shared"`
	All    Usage `json:"all"`

	Endpoints []EndpointUsage `json:"endpoints"`

	// Caches is what the response caches actually did, beside the simulation
	// of the cache that did not exist. An empty list on a running instance
	// means no cache is registered, which is a wiring fault and not a hit rate
	// of zero; CacheNote says so in words.
	Caches    []CacheStats `json:"caches"`
	CacheNote string       `json:"cache_note"`

	// TTLSeconds names the cache lifetimes the would-have-hit figures answer
	// for, in the order they appear in every Hits array below.
	TTLSeconds []int `json:"ttl_seconds"`

	// Bounds is what stops this meter from being a cost of its own.
	Bounds Bounds `json:"bounds"`

	// Summary is the sentence. Someone reading /sanity during an incident
	// should be able to tell from this line alone whether we are the cause.
	Summary string `json:"summary"`
}

// Budget restates the ceiling.
type Budget struct {
	Requests int `json:"requests"`
	Seconds  int `json:"seconds"`
	// PerSecond is the sustained rate the ceiling works out to, which is the
	// form the figure is usually quoted in.
	PerSecond float64 `json:"per_second"`
	Note      string  `json:"note"`
}

// Usage is one subject's traffic: the counters, the rates, and the verdict.
type Usage struct {
	// Asked is logical requests: callers wanting an answer. Sent is the ones
	// that reached the wire. Collapsed is the difference, which is what
	// singleflight already saves.
	Asked int64 `json:"asked"`
	Sent  int64 `json:"sent"`
	// Served is demand answered from the response cache. Asked is Sent plus
	// Served plus Collapsed, so the three savings never overlap and the
	// arithmetic can be checked from the response itself.
	Served    int64 `json:"served"`
	Collapsed int64 `json:"collapsed"`
	Refused   int64 `json:"refused"`
	Failed    int64 `json:"failed"`
	Bytes     int64 `json:"bytes"`

	// CollapsedPercent is Collapsed over Asked: the share of demand that never
	// cost a request because an identical one was already in flight.
	// ServedPercent is the same for demand the cache answered, and is the real
	// hit rate the WouldHit figures below were a simulation of.
	CollapsedPercent float64 `json:"collapsed_percent"`
	ServedPercent    float64 `json:"served_percent"`

	Last60s  int64 `json:"last_60s"`
	Last300s int64 `json:"last_300s"`
	Last1h   int64 `json:"last_1h"`

	PerSec60s  float64 `json:"per_sec_60s"`
	PerSec300s float64 `json:"per_sec_300s"`
	PerSec1h   float64 `json:"per_sec_1h"`

	// PeakMinute is the busiest single minute in the retained day, and
	// Peak300s the busiest five consecutive minutes. The second is the one the
	// ceiling is about, because the ceiling counts requests per window and not
	// a rate.
	PeakMinute     int64   `json:"peak_minute"`
	PeakMinuteAt   string  `json:"peak_minute_at,omitempty"`
	PeakMinuteRate float64 `json:"peak_minute_per_sec"`
	Peak300s       int64   `json:"peak_300s"`
	Peak300sAt     string  `json:"peak_300s_at,omitempty"`

	// Percent figures are against BudgetRequests, so 100 means the ceiling was
	// reached exactly.
	CurrentPercent float64 `json:"current_percent"`
	PeakPercent    float64 `json:"peak_percent"`

	// Verdict grades PeakPercent. CurrentVerdict grades the window in progress,
	// which is the one an operator reading this during an incident is looking at.
	Verdict        string `json:"verdict"`
	CurrentVerdict string `json:"current_verdict"`
}

// EndpointUsage is one endpoint's share, and the queries behind it.
type EndpointUsage struct {
	URL string `json:"url"`
	// SharedBucket says whether this endpoint spends the per-IP budget. An
	// endpoint that does not (mainnet served by onbloc, say) still costs
	// latency and still shows up here; it just does not count against the
	// ceiling.
	SharedBucket bool `json:"shared_bucket"`
	// Usage is embedded, so an endpoint's counters sit flat beside its URL.
	Usage
	Groups []GroupUsage `json:"groups"`
}

// GroupUsage is one operation's share, which is the answer to "which queries
// dominate" and therefore to "what would a cache have to cover".
type GroupUsage struct {
	Label     string `json:"label"`
	Asked     int64  `json:"asked"`
	Sent      int64  `json:"sent"`
	Served    int64  `json:"served"`
	Collapsed int64  `json:"collapsed"`
	Refused   int64  `json:"refused"`
	Failed    int64  `json:"failed"`
	Bytes     int64  `json:"bytes"`

	// SentPercent is this group's share of the endpoint's wire requests.
	// ServedPercent is the cache's measured hit rate for this operation, which
	// is the figure WouldHitPercent below was predicting before the cache
	// existed. Reading the two side by side is the point.
	SentPercent   float64 `json:"sent_percent"`
	ServedPercent float64 `json:"served_percent"`

	// WouldHit is, per entry of TTLSeconds, how many of this group's wire
	// requests a response cache of that lifetime would have answered instead.
	// WouldHitPercent is the same figure as a hit rate.
	//
	// A floor in both directions named in cachesim.go: evictions read as
	// misses, and requests singleflight already collapsed are excluded, since
	// a cache cannot claim a saving that has already been made.
	WouldHit        []int64   `json:"would_hit"`
	WouldHitPercent []float64 `json:"would_hit_percent"`
}

// Bounds is the meter's own cost, reported rather than asserted.
type Bounds struct {
	MaxEndpoints  int    `json:"max_endpoints"`
	MaxGroups     int    `json:"max_groups_per_endpoint"`
	SecondBuckets int    `json:"second_buckets_per_endpoint"`
	MinuteBuckets int    `json:"minute_buckets_per_endpoint"`
	SimCapacity   int    `json:"cache_sim_keys"`
	SimTracked    int64  `json:"cache_sim_tracked"`
	SimEvictions  int64  `json:"cache_sim_evictions"`
	Note          string `json:"note"`
}

// CacheStats is one response cache's own reading of itself: what it holds,
// what bounds it, and what it has done.
type CacheStats struct {
	// Name is the pool the cache belongs to, by the host of its first
	// endpoint.
	Name    string `json:"name"`
	Entries int    `json:"entries"`
	Bytes   int64  `json:"bytes"`
	// MaxEntries and MaxBytes are the eviction bounds, restated here so the
	// memory story is readable off the response rather than out of the source.
	MaxEntries int   `json:"max_entries"`
	MaxBytes   int64 `json:"max_bytes"`

	Hits   int64 `json:"hits"`
	Misses int64 `json:"misses"`
	Stores int64 `json:"stores"`
	// Evictions is entries dropped to stay inside the bounds; Expired is
	// entries dropped because their lifetime ran out. Separate because they
	// say different things: evictions mean the cache is too small for the
	// working set, expiries mean it is doing what it was told.
	Evictions int64 `json:"evictions"`
	Expired   int64 `json:"expired"`
	// Purges is how many times the chain under this cache changed identity and
	// took the cache with it. See ResponseCache.Purge.
	Purges    int64  `json:"purges"`
	LastPurge string `json:"last_purge,omitempty"`

	HitPercent float64 `json:"hit_percent"`
}

const cacheNote = "a finalised block is immutable, so block answers are kept indefinitely and " +
	"evicted by least-recent-use rather than by age; a chain reset purges the cache that was " +
	"keyed on the old chain's heights. The chain tip is deliberately never cached, and neither " +
	"is block 1, which is the fingerprint the reset check reads."

const noCacheNote = "no response cache is registered on this instance, so every figure above is " +
	"uncached traffic. This is a wiring fault, not a hit rate of zero."

const budgetNote = "one Cloudflare rule, per IP, counting every indexer hostname under gno.land " +
	"together rather than per hostname or per network. Going one over costs a 300 second blackout, " +
	"because the rule's block cannot be shorter than its own period."

const boundsNote = "every counter here is in fixed memory: a ring per endpoint rather than a " +
	"timestamp per request, a capped group table keyed by query text this repo writes, and an LRU " +
	"of request digests for the cache simulation. Nothing is keyed by anything an outsider controls."

// Snapshot reads the meter.
func (m *Meter) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	sim := m.sim.stats()

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()

	snap := Snapshot{
		GeneratedAt:   now.Format(time.RFC3339),
		Since:         m.since.Format(time.RFC3339),
		UptimeSeconds: int(now.Sub(m.since).Seconds()),
		Budget: Budget{
			Requests:  BudgetRequests,
			Seconds:   int(BudgetWindow.Seconds()),
			PerSecond: round2(float64(BudgetRequests) / BudgetWindow.Seconds()),
			Note:      budgetNote,
		},
		Bounds: Bounds{
			MaxEndpoints:  maxEndpoints,
			MaxGroups:     maxGroups,
			SecondBuckets: secBuckets,
			MinuteBuckets: minBuckets,
			SimCapacity:   sim.capacity,
			SimTracked:    sim.tracked,
			SimEvictions:  sim.evictions,
			Note:          boundsNote,
		},
	}
	for _, ttl := range TTLs {
		snap.TTLSeconds = append(snap.TTLSeconds, int(ttl.Seconds()))
	}
	snap.Caches = make([]CacheStats, 0, len(m.cacheSrcs))
	for _, src := range m.cacheSrcs {
		snap.Caches = append(snap.Caches, src())
	}
	snap.CacheNote = cacheNote
	if len(snap.Caches) == 0 {
		snap.CacheNote = noCacheNote
	}

	var all, shared totals
	for _, key := range m.order {
		ep := m.eps[key]
		all.add(ep.totals)
		if ep.shared {
			shared.add(ep.totals)
		}
		snap.Endpoints = append(snap.Endpoints, EndpointUsage{
			URL:          ep.url,
			SharedBucket: ep.shared,
			Usage:        usageOf(ep.totals, &ep.win, now),
			Groups:       groupsOf(ep),
		})
	}
	// Busiest endpoint first: an operator reading this wants the one spending
	// the budget, not the one listed first in the config.
	sort.SliceStable(snap.Endpoints, func(i, j int) bool {
		return snap.Endpoints[i].Sent > snap.Endpoints[j].Sent
	})

	snap.All = usageOf(all, &m.allWin, now)
	snap.Shared = usageOf(shared, &m.sharedWin, now)
	snap.Summary = summarize(snap)
	return snap
}

func (t *totals) add(o totals) {
	t.asked += o.asked
	t.sent += o.sent
	t.served += o.served
	t.collapsed += o.collapsed
	t.refused += o.refused
	t.failed += o.failed
	t.bytes += o.bytes
}

func usageOf(t totals, w *window, now time.Time) Usage {
	last60, _ := w.lastSeconds(now, 60)
	last300, _ := w.lastSeconds(now, int64(BudgetWindow.Seconds()))
	last1h, _ := w.lastMinutes(now, 60)
	peakMin, peakMinAt := w.peakRun(now, 1)
	peak300, peak300At := w.peakRun(now, int(BudgetWindow.Minutes()))

	u := Usage{
		Asked:     t.asked,
		Sent:      t.sent,
		Served:    t.served,
		Collapsed: t.collapsed,
		Refused:   t.refused,
		Failed:    t.failed,
		Bytes:     t.bytes,

		Last60s:  last60,
		Last300s: last300,
		Last1h:   last1h,

		PerSec60s:  round2(float64(last60) / 60),
		PerSec300s: round2(float64(last300) / BudgetWindow.Seconds()),
		PerSec1h:   round2(float64(last1h) / 3600),

		PeakMinute:     peakMin,
		PeakMinuteRate: round2(float64(peakMin) / 60),
		Peak300s:       peak300,

		CurrentPercent: round2(100 * float64(last300) / BudgetRequests),
		PeakPercent:    round2(100 * float64(peak300) / BudgetRequests),
	}
	if t.asked > 0 {
		u.CollapsedPercent = round2(100 * float64(t.collapsed) / float64(t.asked))
		u.ServedPercent = round2(100 * float64(t.served) / float64(t.asked))
	}
	if peakMin > 0 {
		u.PeakMinuteAt = peakMinAt.Format(time.RFC3339)
	}
	if peak300 > 0 {
		u.Peak300sAt = peak300At.Format(time.RFC3339)
	}
	u.Verdict = verdictOf(peak300)
	u.CurrentVerdict = verdictOf(last300)
	return u
}

func groupsOf(ep *endpointCounts) []GroupUsage {
	out := make([]GroupUsage, 0, len(ep.order))
	for _, label := range ep.order {
		g := ep.groups[label]
		gu := GroupUsage{
			Label:     g.label,
			Asked:     g.totals.asked,
			Sent:      g.totals.sent,
			Served:    g.totals.served,
			Collapsed: g.totals.collapsed,
			Refused:   g.totals.refused,
			Failed:    g.totals.failed,
			Bytes:     g.totals.bytes,
		}
		if ep.totals.sent > 0 {
			gu.SentPercent = round2(100 * float64(g.totals.sent) / float64(ep.totals.sent))
		}
		if g.totals.asked > 0 {
			gu.ServedPercent = round2(100 * float64(g.totals.served) / float64(g.totals.asked))
		}
		for i := range TTLs {
			gu.WouldHit = append(gu.WouldHit, g.hits[i])
			pct := 0.0
			if g.totals.sent > 0 {
				pct = round2(100 * float64(g.hits[i]) / float64(g.totals.sent))
			}
			gu.WouldHitPercent = append(gu.WouldHitPercent, pct)
		}
		out = append(out, gu)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sent > out[j].Sent })
	return out
}

// verdictOf grades a count of requests in one budget window.
func verdictOf(inWindow int64) string {
	switch {
	case inWindow >= BudgetRequests:
		return VerdictOver
	case float64(inWindow) >= nearFraction*BudgetRequests:
		return VerdictNear
	default:
		return VerdictUnder
	}
}

// summarize writes the sentence /sanity prints.
//
// Every branch is a different honest answer, and the one that matters most is
// the empty one: a meter that has recorded nothing must say so rather than
// reporting a comfortable 0% of the budget, which is the reading that would let
// a broken meter pass for good news.
func summarize(s Snapshot) string {
	if s.Shared.Sent == 0 && s.All.Sent == 0 {
		return "no outbound indexer requests recorded yet on this instance"
	}
	if s.Shared.Sent == 0 {
		return fmt.Sprintf(
			"%d requests sent, none of them to an endpoint on the shared per-IP budget, so nothing here counts against the %d per %ds ceiling",
			s.All.Sent, BudgetRequests, int(BudgetWindow.Seconds()))
	}
	lead := fmt.Sprintf(
		"%s the budget: %d requests in the last %ds against a ceiling of %d (%.0f%%), peaking at %d in one %ds window",
		s.Shared.CurrentVerdict, s.Shared.Last300s, int(BudgetWindow.Seconds()),
		BudgetRequests, s.Shared.CurrentPercent, s.Shared.Peak300s, int(BudgetWindow.Seconds()))
	if s.Shared.PeakPercent >= 100 {
		lead += fmt.Sprintf(" (%.0f%% of it, which is a blackout)", s.Shared.PeakPercent)
	} else {
		lead += fmt.Sprintf(" (%.0f%%)", s.Shared.PeakPercent)
	}
	if s.Shared.Refused > 0 {
		lead += fmt.Sprintf(". %d refusals so far", s.Shared.Refused)
	}
	if s.Shared.Served > 0 {
		lead += fmt.Sprintf(". %.0f%% of demand was answered from the response cache", s.Shared.ServedPercent)
	}
	if s.Shared.Collapsed > 0 {
		lead += fmt.Sprintf(". %.0f%% of demand never reached the wire, collapsed into a request already in flight",
			s.Shared.CollapsedPercent)
	}
	if len(s.Caches) == 0 {
		lead += ". No response cache is registered, which is a wiring fault"
	}
	return lead
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
