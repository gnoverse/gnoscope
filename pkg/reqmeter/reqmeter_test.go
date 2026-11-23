package reqmeter

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	mainnet = "https://indexer.gno.land/graphql/query"
	onyx    = "https://indexer.onyx.testnets.gno.land/graphql/query"
	onbloc  = "https://indexer.onbloc.xyz/graphql/query"
)

// fakeClock is a hand-driven clock, so the rolling window can be exercised
// across a boundary without a test that sleeps for five minutes.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func newTestMeter() (*Meter, *fakeClock) {
	c := &fakeClock{at: base}
	return newMeter(c.now), c
}

func send(m *Meter, endpoint, label string, body string, out Outcome) {
	m.Asked(endpoint, label)
	m.Sent(Request{
		URL:       endpoint,
		Label:     label,
		Body:      []byte(body),
		Bytes:     len(body) * 10,
		Outcome:   out,
		Cacheable: out == OutcomeOK,
	})
}

// Collapsed is the difference between what callers asked for and what reached
// the wire, which is the saving singleflight is already making. It is reported
// as a difference rather than counted separately so that the two figures cannot
// drift apart and claim a saving that did not happen.
func TestMeterCountsCollapsedRequests(t *testing.T) {
	m, _ := newTestMeter()
	// Five callers want the same answer; one round trip serves them.
	for i := 0; i < 5; i++ {
		m.Asked(onyx, "getTransactions")
	}
	m.Sent(Request{URL: onyx, Label: "getTransactions", Body: []byte("q"), Bytes: 100, Outcome: OutcomeOK, Cacheable: true})

	s := m.Snapshot()
	if s.Shared.Asked != 5 {
		t.Errorf("asked = %d, want 5", s.Shared.Asked)
	}
	if s.Shared.Sent != 1 {
		t.Errorf("sent = %d, want 1", s.Shared.Sent)
	}
	if s.Shared.Collapsed != 4 {
		t.Errorf("collapsed = %d, want 4", s.Shared.Collapsed)
	}
	if s.Shared.CollapsedPercent != 80 {
		t.Errorf("collapsed percent = %v, want 80", s.Shared.CollapsedPercent)
	}
}

// An endpoint off the shared Cloudflare edge still costs latency and still
// shows up, but it must not count against a ceiling it does not spend. Getting
// this backwards would report a budget problem on an instance reading mainnet
// through onbloc, which is the configuration that never 429s.
func TestMeterSeparatesTheSharedBucket(t *testing.T) {
	tests := []struct {
		url    string
		shared bool
	}{
		{mainnet, true},
		{onyx, true},
		{"https://indexer.latest.testnets.gno.land/graphql/query", true},
		{"https://gno.land/graphql/query", true},
		{onbloc, false},
		{"https://indexer.notgno.land/graphql/query", false},
		// A host that only ends in the characters is not in the domain.
		{"https://evilgno.land/query", false},
		// Unparseable counts as shared: over-reporting what we spend against a
		// ceiling is the safe direction to be wrong in.
		{"", true},
	}
	for _, tc := range tests {
		if got := OnSharedBucket(tc.url); got != tc.shared {
			t.Errorf("OnSharedBucket(%q) = %v, want %v", tc.url, got, tc.shared)
		}
	}

	m, _ := newTestMeter()
	send(m, onyx, "getTransactions", "a", OutcomeOK)
	send(m, onbloc, "getTransactions", "b", OutcomeOK)
	s := m.Snapshot()
	if s.All.Sent != 2 {
		t.Errorf("all sent = %d, want 2", s.All.Sent)
	}
	if s.Shared.Sent != 1 {
		t.Errorf("shared sent = %d, want 1", s.Shared.Sent)
	}
}

// The budget is per IP across every shared hostname at once, so the verdict has
// to be drawn from one aggregate window and not from the endpoints added up:
// the peak of a sum is not the sum of the peaks.
func TestMeterGradesAgainstTheBudget(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		verdict string
	}{
		{"quiet", 10, VerdictUnder},
		{"just under near", int(nearFraction*BudgetRequests) - 1, VerdictUnder},
		{"near", int(nearFraction*BudgetRequests) + 1, VerdictNear},
		{"exactly at the ceiling is over", BudgetRequests, VerdictOver},
		{"well over", BudgetRequests + 500, VerdictOver},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestMeter()
			for i := 0; i < tc.count; i++ {
				send(m, onyx, "getTransactions", fmt.Sprint(i), OutcomeOK)
			}
			s := m.Snapshot()
			if s.Shared.CurrentVerdict != tc.verdict {
				t.Errorf("%d requests in one window graded %q, want %q",
					tc.count, s.Shared.CurrentVerdict, tc.verdict)
			}
			if s.Shared.Last300s != int64(tc.count) {
				t.Errorf("last 300s = %d, want %d", s.Shared.Last300s, tc.count)
			}
		})
	}
}

// The two endpoints burst at different times. Their individual peaks add to
// more than anything that ever happened at once, and the aggregate window is
// what keeps the verdict honest about that.
func TestMeterAggregatePeakIsNotTheSumOfPeaks(t *testing.T) {
	m, c := newTestMeter()
	for i := 0; i < 100; i++ {
		send(m, onyx, "getTransactions", fmt.Sprint(i), OutcomeOK)
	}
	// Far enough that the two bursts share no five-minute run.
	c.advance(30 * time.Minute)
	for i := 0; i < 100; i++ {
		send(m, mainnet, "getTransactions", fmt.Sprint(i), OutcomeOK)
	}
	c.advance(time.Minute)

	s := m.Snapshot()
	if s.Shared.Peak300s != 100 {
		t.Errorf("aggregate peak over a 300s window = %d, want 100", s.Shared.Peak300s)
	}
	var sumOfPeaks int64
	for _, ep := range s.Endpoints {
		sumOfPeaks += ep.Peak300s
	}
	if sumOfPeaks != 200 {
		t.Errorf("the per-endpoint peaks sum to %d, want 200; the fixture no longer proves anything", sumOfPeaks)
	}
}

// The rolling figures roll. A burst five minutes ago is out of the current
// window and still in the day's peak, which is the whole reason both are
// reported.
func TestMeterRatesRollForward(t *testing.T) {
	m, c := newTestMeter()
	for i := 0; i < 600; i++ {
		send(m, onyx, "getTransactions", fmt.Sprint(i), OutcomeOK)
	}
	if s := m.Snapshot(); s.Shared.Last300s != 600 {
		t.Fatalf("immediately after the burst, last 300s = %d, want 600", s.Shared.Last300s)
	}

	c.advance(6 * time.Minute)
	s := m.Snapshot()
	if s.Shared.Last300s != 0 {
		t.Errorf("six minutes later, last 300s = %d, want 0", s.Shared.Last300s)
	}
	if s.Shared.Last1h != 600 {
		t.Errorf("six minutes later, last 1h = %d, want 600", s.Shared.Last1h)
	}
	if s.Shared.PeakMinute != 600 {
		t.Errorf("peak minute = %d, want 600", s.Shared.PeakMinute)
	}
	if s.Shared.Sent != 600 {
		t.Errorf("the cumulative total moved to %d; it should never roll", s.Shared.Sent)
	}

	c.advance(2 * time.Hour)
	s = m.Snapshot()
	if s.Shared.Last1h != 0 {
		t.Errorf("two hours later, last 1h = %d, want 0", s.Shared.Last1h)
	}
	if s.Shared.PeakMinute != 600 {
		t.Errorf("the day's peak was forgotten after two hours: %d", s.Shared.PeakMinute)
	}

	c.advance(23 * time.Hour)
	if s := m.Snapshot(); s.Shared.PeakMinute != 0 {
		t.Errorf("a day later, peak minute = %d, want 0", s.Shared.PeakMinute)
	}
}

// Which queries dominate is the decisive follow-up question, so the groups are
// ordered by what they actually cost and carry their own would-have-hit figures:
// a cache covering the top group may be the whole fix.
func TestMeterGroupsAndRanksCallers(t *testing.T) {
	m, _ := newTestMeter()
	for i := 0; i < 20; i++ {
		send(m, onyx, "getTransactions", fmt.Sprint(i), OutcomeOK)
	}
	for i := 0; i < 5; i++ {
		send(m, onyx, "getBlocks", fmt.Sprint(i), OutcomeOK)
	}
	// The same tip query over and over, which is what a cache would catch.
	for i := 0; i < 10; i++ {
		send(m, onyx, "latestBlockHeight", "tip", OutcomeOK)
	}

	s := m.Snapshot()
	if len(s.Endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(s.Endpoints))
	}
	groups := s.Endpoints[0].Groups
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	if groups[0].Label != "getTransactions" || groups[1].Label != "latestBlockHeight" {
		t.Errorf("groups ranked %q then %q, want getTransactions then latestBlockHeight",
			groups[0].Label, groups[1].Label)
	}
	if groups[0].SentPercent < 57 || groups[0].SentPercent > 58 {
		t.Errorf("top group is %v%% of the endpoint's requests, want about 57", groups[0].SentPercent)
	}

	tip := groups[1]
	// Nine of the ten repeats land inside every lifetime.
	for i := range TTLs {
		if tip.WouldHit[i] != 9 {
			t.Errorf("latestBlockHeight would-have-hit at %s = %d, want 9", TTLs[i], tip.WouldHit[i])
		}
		if tip.WouldHitPercent[i] != 90 {
			t.Errorf("latestBlockHeight hit rate at %s = %v, want 90", TTLs[i], tip.WouldHitPercent[i])
		}
	}
	// Every getTransactions request asked a different question, so a cache
	// would have saved none of them.
	for i := range TTLs {
		if groups[0].WouldHit[i] != 0 {
			t.Errorf("distinct queries would-have-hit at %s = %d, want 0", TTLs[i], groups[0].WouldHit[i])
		}
	}
}

// Refusals and failures are counted apart. A 429 is the one the budget is
// about; a timeout says nothing about it.
func TestMeterSplitsRefusalsFromFailures(t *testing.T) {
	m, _ := newTestMeter()
	send(m, onyx, "getTransactions", "a", OutcomeOK)
	send(m, onyx, "getTransactions", "b", OutcomeRefused)
	send(m, onyx, "getTransactions", "c", OutcomeFailed)
	send(m, onyx, "getTransactions", "d", OutcomeFailed)

	s := m.Snapshot()
	if s.Shared.Refused != 1 {
		t.Errorf("refused = %d, want 1", s.Shared.Refused)
	}
	if s.Shared.Failed != 2 {
		t.Errorf("failed = %d, want 2", s.Shared.Failed)
	}
	if s.Shared.Sent != 4 {
		t.Errorf("sent = %d, want 4; a refusal is still a request that was made", s.Shared.Sent)
	}
	if !strings.Contains(s.Summary, "1 refusals") {
		t.Errorf("the summary does not mention the refusal: %q", s.Summary)
	}
}

// Nothing recorded must read as nothing recorded. A meter that reports a
// comfortable 0% of the budget when it is not wired up is a broken meter
// passing for good news, which is the failure mode #528's operator note was
// about.
func TestMeterSaysWhenItHasNothing(t *testing.T) {
	m, _ := newTestMeter()
	s := m.Snapshot()
	if s.Shared.Sent != 0 || s.All.Sent != 0 {
		t.Fatalf("a fresh meter reports traffic")
	}
	if !strings.Contains(s.Summary, "no outbound indexer requests recorded yet") {
		t.Errorf("summary = %q", s.Summary)
	}
	if len(s.Endpoints) != 0 {
		t.Errorf("endpoints = %d, want none", len(s.Endpoints))
	}
	if s.Budget.Requests != BudgetRequests || s.Budget.Seconds != int(BudgetWindow.Seconds()) {
		t.Errorf("the ceiling is missing from an empty snapshot: %+v", s.Budget)
	}
}

// An instance reading only off-edge origins spends none of the shared budget,
// and must say that rather than reporting "under" as though it had been
// measured against the ceiling.
func TestMeterSaysWhenNothingTouchesTheBudget(t *testing.T) {
	m, _ := newTestMeter()
	send(m, onbloc, "getTransactions", "a", OutcomeOK)
	s := m.Snapshot()
	if !strings.Contains(s.Summary, "none of them to an endpoint on the shared per-IP budget") {
		t.Errorf("summary = %q", s.Summary)
	}
}

// The bounds are the promise this package makes about its own cost, so they are
// reported rather than asserted in a comment. Both caps have to actually hold.
func TestMeterStaysBounded(t *testing.T) {
	m, _ := newTestMeter()
	// onyx first, so it holds a slot of its own and the fakes are what gets
	// folded. An endpoint the operator configured is never the one to lose.
	for i := 0; i < maxGroups*4; i++ {
		send(m, onyx, fmt.Sprintf("field%d", i), "a", OutcomeOK)
	}
	for i := 0; i < maxEndpoints*4; i++ {
		send(m, fmt.Sprintf("https://indexer-%d.example/query", i), "getTransactions", "a", OutcomeOK)
	}

	s := m.Snapshot()
	if len(s.Endpoints) > maxEndpoints {
		t.Errorf("endpoints = %d, cap %d", len(s.Endpoints), maxEndpoints)
	}
	var onyxEP *EndpointUsage
	for i := range s.Endpoints {
		if s.Endpoints[i].URL == onyx {
			onyxEP = &s.Endpoints[i]
		}
	}
	if onyxEP == nil {
		t.Fatal("the onyx endpoint is missing from the snapshot")
	}
	if len(onyxEP.Groups) > maxGroups {
		t.Errorf("groups = %d, cap %d", len(onyxEP.Groups), maxGroups)
	}
	// Nothing is lost when a cap bites, it is folded.
	var sent int64
	for _, g := range onyxEP.Groups {
		sent += g.Sent
	}
	if sent != onyxEP.Sent {
		t.Errorf("the groups account for %d of %d requests; the overflow bucket is dropping traffic", sent, onyxEP.Sent)
	}
	if s.Bounds.MaxEndpoints != maxEndpoints || s.Bounds.SimCapacity != maxSimKeys {
		t.Errorf("the snapshot does not report its own bounds: %+v", s.Bounds)
	}
}

// A label this could not read is its own group and says so. Folding it into
// whichever group happened to parse would hide a parser that had stopped
// working.
func TestMeterKeepsUnknownAsItsOwnGroup(t *testing.T) {
	m, _ := newTestMeter()
	send(m, onyx, "", "a", OutcomeOK)
	s := m.Snapshot()
	if s.Endpoints[0].Groups[0].Label != UnknownLabel {
		t.Errorf("an unlabelled request landed in %q", s.Endpoints[0].Groups[0].Label)
	}
}

// Every path into the meter takes the same lock, and the one that does not
// (the cache simulation) has its own. Run under -race this is the test that
// says so.
func TestMeterIsConcurrencySafe(t *testing.T) {
	m, _ := newTestMeter()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				send(m, onyx, "getTransactions", fmt.Sprint(n, j), OutcomeOK)
				m.Snapshot()
			}
		}(i)
	}
	wg.Wait()
	if got := m.Snapshot().Shared.Sent; got != 1600 {
		t.Fatalf("sent = %d, want 1600", got)
	}
}

// A nil meter is a meter nobody wired up, and every call on one has to be a
// no-op: the client constructors hand one out by default, and a test or tool
// that builds a Client literally gets nil.
func TestNilMeterIsSafe(t *testing.T) {
	var m *Meter
	m.Asked(onyx, "getTransactions")
	m.Sent(Request{URL: onyx, Label: "getTransactions"})
	if s := m.Snapshot(); s.Summary != "" {
		t.Errorf("a nil meter produced %q", s.Summary)
	}
}

// The response is read by a browser and by whoever comes to answer #525 with
// it, so it has to survive the round trip through JSON with its figures intact.
func TestSnapshotMarshals(t *testing.T) {
	m, _ := newTestMeter()
	send(m, onyx, "getTransactions", "a", OutcomeOK)
	b, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"budget", "shared", "all", "endpoints", "ttl_seconds", "bounds", "summary"} {
		if _, ok := back[key]; !ok {
			t.Errorf("the response has no %q", key)
		}
	}
	// The endpoint's own counters sit flat beside its URL rather than nested.
	eps := back["endpoints"].([]any)
	ep := eps[0].(map[string]any)
	for _, key := range []string{"url", "shared_bucket", "sent", "groups", "peak_300s"} {
		if _, ok := ep[key]; !ok {
			t.Errorf("the endpoint has no %q", key)
		}
	}
}

// Three savings, counted apart. Collapsed is "N callers wanted this at the same
// instant"; Served is "we already knew the answer". Folding the second into the
// first would credit singleflight with the cache's work and leave an operator
// unable to tell which machinery is carrying a page.
func TestMeterSeparatesCacheHitsFromCollapses(t *testing.T) {
	m, _ := newTestMeter()

	// Ten callers want the same block. One round trip, three collapse onto it,
	// six are answered from the cache afterwards.
	for i := 0; i < 4; i++ {
		m.Asked(onyx, "getBlocks")
	}
	m.Sent(Request{URL: onyx, Label: "getBlocks", Body: []byte("q"), Bytes: 300, Outcome: OutcomeOK, Cacheable: true})
	for i := 0; i < 6; i++ {
		m.Asked(onyx, "getBlocks")
		m.Served(onyx, "getBlocks")
	}

	s := m.Snapshot()
	if s.Shared.Asked != 10 || s.Shared.Sent != 1 || s.Shared.Served != 6 || s.Shared.Collapsed != 3 {
		t.Fatalf("asked/sent/served/collapsed = %d/%d/%d/%d, want 10/1/6/3",
			s.Shared.Asked, s.Shared.Sent, s.Shared.Served, s.Shared.Collapsed)
	}
	// The arithmetic has to close from the response alone.
	if s.Shared.Sent+s.Shared.Served+s.Shared.Collapsed != s.Shared.Asked {
		t.Error("sent + served + collapsed does not add up to asked")
	}
	if s.Shared.ServedPercent != 60 || s.Shared.CollapsedPercent != 30 {
		t.Errorf("served/collapsed percent = %v/%v, want 60/30", s.Shared.ServedPercent, s.Shared.CollapsedPercent)
	}
	g := s.Endpoints[0].Groups[0]
	if g.Served != 6 || g.ServedPercent != 60 {
		t.Errorf("group served = %d (%v%%), want 6 (60%%)", g.Served, g.ServedPercent)
	}
	// The real hit rate sits beside the simulation it was predicting.
	if len(g.WouldHit) != len(TTLs) {
		t.Errorf("would_hit = %v, want one entry per TTL", g.WouldHit)
	}
	if !strings.Contains(s.Summary, "answered from the response cache") {
		t.Errorf("summary says nothing about the cache: %q", s.Summary)
	}
}

// ⚠️ #528 shipped a feature that recorded nothing until a flag reached the unit
// file, and a deploy that shipped the binary alone read as broken only to
// someone who knew. The opposite failure is covered here: a meter with no
// registered cache must say "nothing is wired" rather than report a hit rate
// of zero, which reads identically to a cache that is working and cold.
func TestUsageSaysWhenNoCacheIsRegistered(t *testing.T) {
	m, _ := newTestMeter()
	send(m, onyx, "getBlocks", "a", OutcomeOK)

	s := m.Snapshot()
	if len(s.Caches) != 0 {
		t.Fatalf("caches = %+v, want none", s.Caches)
	}
	if !strings.Contains(s.CacheNote, "wiring fault") {
		t.Errorf("cache_note = %q, want it to name the wiring fault", s.CacheNote)
	}
	if !strings.Contains(s.Summary, "wiring fault") {
		t.Errorf("summary = %q, want it to name the wiring fault", s.Summary)
	}

	// With one registered, the note turns into the real explanation and the
	// cache's own bounds are readable off the response.
	m.RegisterCache(func() CacheStats {
		return CacheStats{Name: "indexer.onyx.testnets.gno.land", Entries: 7, MaxEntries: 20000, MaxBytes: 32 << 20, Hits: 3, Misses: 7}
	})
	s = m.Snapshot()
	if len(s.Caches) != 1 || s.Caches[0].Entries != 7 || s.Caches[0].MaxBytes != 32<<20 {
		t.Fatalf("caches = %+v, want the registered one with its bounds", s.Caches)
	}
	if strings.Contains(s.CacheNote, "wiring fault") {
		t.Errorf("cache_note still reports a wiring fault: %q", s.CacheNote)
	}
	if strings.Contains(s.Summary, "wiring fault") {
		t.Errorf("summary still reports a wiring fault: %q", s.Summary)
	}
}

// The registry is bounded like everything else here: one cache per endpoint
// pool, and the pools come from the operator's config.
func TestCacheRegistryIsBounded(t *testing.T) {
	m, _ := newTestMeter()
	for i := 0; i < maxCacheSources*3; i++ {
		m.RegisterCache(func() CacheStats { return CacheStats{Name: "x"} })
	}
	m.RegisterCache(nil)
	if got := len(m.Snapshot().Caches); got != maxCacheSources {
		t.Errorf("registered caches = %d, want %d", got, maxCacheSources)
	}
}
