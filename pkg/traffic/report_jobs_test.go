package traffic

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// runJobs is what keeps a report's twenty-odd queries off each other's
// critical path, so the two things it promises are worth holding: every job
// runs, and no more than limit of them are in flight at once. Dropping the
// bound would move the queue into the connection pool; dropping a job would
// return a report with an empty panel and no error.
func TestRunJobsRunsEveryJobWithinTheBound(t *testing.T) {
	for _, tc := range []struct{ jobs, limit int }{
		{1, 8}, {8, 8}, {25, 8}, {25, 1},
	} {
		var mu sync.Mutex
		var inFlight, peak, ran int
		fns := make([]func() error, tc.jobs)
		for i := range fns {
			fns[i] = func() error {
				mu.Lock()
				inFlight++
				ran++
				if inFlight > peak {
					peak = inFlight
				}
				mu.Unlock()
				time.Sleep(2 * time.Millisecond)
				mu.Lock()
				inFlight--
				mu.Unlock()
				return nil
			}
		}
		if err := runJobs(fns, tc.limit); err != nil {
			t.Fatalf("%d jobs at %d: %v", tc.jobs, tc.limit, err)
		}
		if ran != tc.jobs {
			t.Errorf("%d jobs at %d: %d ran", tc.jobs, tc.limit, ran)
		}
		if peak > tc.limit {
			t.Errorf("%d jobs at %d: %d were in flight at once", tc.jobs, tc.limit, peak)
		}
	}
}

// A failing job is reported, and the jobs beside it still run: they write into
// separate fields of one report, and stopping half way would leave a report
// whose panels disagree about which window they describe.
func TestRunJobsReportsAnErrorAndStillRunsTheRest(t *testing.T) {
	boom := errors.New("boom")
	var mu sync.Mutex
	ran := 0
	count := func() error {
		mu.Lock()
		ran++
		mu.Unlock()
		return nil
	}
	fns := []func() error{count, func() error { return boom }, count, count}

	if err := runJobs(fns, 2); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	if ran != 3 {
		t.Errorf("%d of the 3 good jobs ran", ran)
	}
}

// Both percentiles rank the same set, and that set excludes streams: an SSE
// connection's DurMS is how long a reader stayed, not how long they waited.
// The count behind the ranking is now taken once and shared, so a count that
// quietly started including streams would push both percentiles down the list
// rather than failing.
func TestPercentilesIgnoreStreams(t *testing.T) {
	s := testStore(t, 0)
	now := time.Now().UTC()
	for i := 1; i <= 100; i++ {
		s.Record(Record{At: now, Kind: "api", Route: "/api/thing", DurMS: float64(i), Client: "browser"})
	}
	// Ten streams, each far slower than anything above. Counted in the totals,
	// never in the ranking.
	for i := 0; i < 10; i++ {
		s.Record(Record{At: now, Kind: "stream", Route: "/api/live", DurMS: 900000, Client: "browser"})
	}
	s.Flush()

	rep, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Requests != 110 {
		t.Errorf("totals = %d, want 110 — streams are counted", rep.Totals.Requests)
	}
	// 100 ranked rows of 1..100ms, read at OFFSET n*pct/100 and so one past
	// the nominal rank: 51 and 96. The exact convention is not what this test
	// is about, the set size is. Counting the ten streams would make it 110,
	// and since they sort last the same offsets would read 56 and 105.
	if rep.Totals.P50MS != 51 {
		t.Errorf("p50 = %v, want 51 — the ranking counted the streams", rep.Totals.P50MS)
	}
	if rep.Totals.P95MS != 96 {
		t.Errorf("p95 = %v, want 96 — the ranking counted the streams", rep.Totals.P95MS)
	}
}
