package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/reqmeter"
)

func getUsage(t *testing.T, a *API) reqmeter.Snapshot {
	t.Helper()
	rec := httptest.NewRecorder()
	a.HandleIndexerUsage(rec, httptest.NewRequest("GET", "/api/indexer/usage", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var snap reqmeter.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return snap
}

// The endpoint serves what the meter holds, with the ceiling beside it. The
// number alone is not actionable: a reader has to be able to tell from one
// response whether we are the cause of an incident, which means the budget and
// the verdict travel with the count.
func TestIndexerUsageEndpoint(t *testing.T) {
	m := reqmeter.New()
	for i := 0; i < 40; i++ {
		m.Asked("https://indexer.onyx.testnets.gno.land/graphql/query", "getTransactions")
		m.Sent(reqmeter.Request{
			URL:       "https://indexer.onyx.testnets.gno.land/graphql/query",
			Label:     "getTransactions",
			Body:      []byte(fmt.Sprint(i)),
			Bytes:     1000,
			Outcome:   reqmeter.OutcomeOK,
			Cacheable: true,
		})
	}
	a := &API{}
	a.SetIndexerMeter(m)

	snap := getUsage(t, a)
	if snap.Shared.Sent != 40 {
		t.Errorf("sent = %d, want 40", snap.Shared.Sent)
	}
	if snap.Budget.Requests != reqmeter.BudgetRequests {
		t.Errorf("the response does not carry the ceiling: %+v", snap.Budget)
	}
	if snap.Shared.CurrentVerdict != reqmeter.VerdictUnder {
		t.Errorf("40 requests graded %q, want under", snap.Shared.CurrentVerdict)
	}
	if len(snap.TTLSeconds) != len(reqmeter.TTLs) {
		t.Errorf("ttl_seconds = %v, want one entry per simulated lifetime", snap.TTLSeconds)
	}
	if len(snap.Endpoints) != 1 || len(snap.Endpoints[0].Groups) != 1 {
		t.Fatalf("endpoints = %+v, want one with one group", snap.Endpoints)
	}
	if snap.Endpoints[0].Groups[0].Label != "getTransactions" {
		t.Errorf("group = %q, want getTransactions", snap.Endpoints[0].Groups[0].Label)
	}
	if snap.Summary == "" {
		t.Error("no summary, so /sanity has no sentence to print")
	}
}

// An instance that has made no outbound request must say so rather than
// reporting a comfortable 0% of the budget. #528 shipped a feature that
// recorded nothing until a flag was added and read as healthy while it did;
// this endpoint needs no flag, and the empty reading still has to be legible
// as empty.
func TestIndexerUsageEndpointWithNoTraffic(t *testing.T) {
	a := &API{}
	a.SetIndexerMeter(reqmeter.New())
	snap := getUsage(t, a)
	if snap.All.Sent != 0 {
		t.Errorf("sent = %d on a meter nothing used", snap.All.Sent)
	}
	if !strings.Contains(snap.Summary, "no outbound indexer requests recorded yet") {
		t.Errorf("summary = %q", snap.Summary)
	}
	if snap.Bounds.MaxEndpoints == 0 {
		t.Error("the response does not report the meter's own bounds")
	}
}

// With no meter set, the handler falls back to the process-wide one, which is
// the one the indexer clients were built with. An API that answered from a
// fresh empty meter would report zero forever on the real instance, which is
// the failure this whole dispatch exists to stop happening twice.
func TestIndexerUsageFallsBackToTheProcessMeter(t *testing.T) {
	a := &API{}
	if a.indexerUsage() != reqmeter.Default() {
		t.Fatal("an API with no meter set does not read the process-wide meter")
	}
}

// The real cache's hit rate has to come out of this endpoint in the same shape
// as the simulation it replaced, or the before-and-after #525 asked for cannot
// be read off a running instance at all.
func TestIndexerUsageReportsTheResponseCache(t *testing.T) {
	const ep = "https://indexer.onyx.testnets.gno.land/graphql/query"
	m := reqmeter.New()
	m.Asked(ep, "getBlocks")
	m.Sent(reqmeter.Request{URL: ep, Label: "getBlocks", Body: []byte("q"), Bytes: 300, Outcome: reqmeter.OutcomeOK, Cacheable: true})
	for i := 0; i < 9; i++ {
		m.Asked(ep, "getBlocks")
		m.Served(ep, "getBlocks")
	}
	m.RegisterCache(func() reqmeter.CacheStats {
		return reqmeter.CacheStats{Name: "indexer.onyx.testnets.gno.land", Entries: 1, MaxEntries: 20000, MaxBytes: 32 << 20, Hits: 9, Misses: 1, HitPercent: 90}
	})
	a := &API{}
	a.SetIndexerMeter(m)

	snap := getUsage(t, a)
	if snap.Shared.Served != 9 || snap.Shared.ServedPercent != 90 {
		t.Errorf("served = %d (%v%%), want 9 (90%%)", snap.Shared.Served, snap.Shared.ServedPercent)
	}
	if len(snap.Caches) != 1 || snap.Caches[0].HitPercent != 90 || snap.Caches[0].MaxBytes == 0 {
		t.Fatalf("caches = %+v, want the registered cache with its bounds", snap.Caches)
	}
	g := snap.Endpoints[0].Groups[0]
	if g.Served != 9 || len(g.WouldHit) != len(reqmeter.TTLs) {
		t.Errorf("group = %+v, want the measured hit beside the simulated one", g)
	}
	if strings.Contains(snap.CacheNote, "wiring fault") {
		t.Errorf("cache_note reports a wiring fault with a cache registered: %q", snap.CacheNote)
	}
}

// ⚠️ And the other direction, which is the one #528 got wrong: an instance with
// no cache registered must read as misconfigured, not as a cache that happens
// to be cold.
func TestIndexerUsageSaysWhenNoCacheIsWired(t *testing.T) {
	const ep = "https://indexer.onyx.testnets.gno.land/graphql/query"
	m := reqmeter.New()
	m.Asked(ep, "getBlocks")
	m.Sent(reqmeter.Request{URL: ep, Label: "getBlocks", Body: []byte("q"), Bytes: 300, Outcome: reqmeter.OutcomeOK, Cacheable: true})
	a := &API{}
	a.SetIndexerMeter(m)

	snap := getUsage(t, a)
	if len(snap.Caches) != 0 {
		t.Fatalf("caches = %+v, want none", snap.Caches)
	}
	if !strings.Contains(snap.CacheNote, "wiring fault") || !strings.Contains(snap.Summary, "wiring fault") {
		t.Errorf("an unwired cache reads as a cold one: note %q, summary %q", snap.CacheNote, snap.Summary)
	}
}
