package traffic

import (
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T, retentionDays int) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "traffic.db"), retentionDays)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var testNow = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func rec(at time.Time, mut func(*Record)) Record {
	r := Record{
		At: at, Visitor: "v0", Method: "GET", Route: "/api/stats",
		Kind: "api", Status: 200, Bytes: 100, DurMS: 10, Client: "browser",
	}
	if mut != nil {
		mut(&r)
	}
	return r
}

func TestRecordAndReport(t *testing.T) {
	s := testStore(t, 30)
	for i := 0; i < 3; i++ {
		s.Record(rec(testNow.Add(-time.Duration(i)*time.Hour), nil))
	}
	s.Record(rec(testNow, func(r *Record) {
		r.Visitor, r.Route, r.Target, r.Kind = "v1", "/", "/realms", "page"
	}))
	s.Flush()

	got, err := s.Report(Query{Window: ParseWindow("24h"), Now: testNow})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if got.Empty {
		t.Fatal("report says empty with four rows written")
	}
	if got.Totals.Requests != 4 {
		t.Errorf("requests = %d, want 4", got.Totals.Requests)
	}
	if got.Totals.Visitors != 2 {
		t.Errorf("visitors = %d, want 2", got.Totals.Visitors)
	}
	if got.Totals.API != 3 || got.Totals.Pages != 1 {
		t.Errorf("api/pages = %d/%d, want 3/1", got.Totals.API, got.Totals.Pages)
	}
	if len(got.TopPages) != 1 || got.TopPages[0].Label != "/realms" {
		t.Errorf("top pages = %+v, want one row for /realms", got.TopPages)
	}
}

// The default view is "what are people doing", and a crawler is not people.
// It is still stored, because it is real load, and ?bots=1 brings it back.
func TestReportExcludesBotsByDefault(t *testing.T) {
	s := testStore(t, 30)
	s.Record(rec(testNow, func(r *Record) { r.Visitor = "human" }))
	for i := 0; i < 50; i++ {
		s.Record(rec(testNow, func(r *Record) {
			r.Visitor, r.Client, r.Robot = "crawler", "bot", true
		}))
	}
	s.Flush()

	def, err := s.Report(Query{Window: ParseWindow("24h"), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if def.Totals.Requests != 1 {
		t.Errorf("default requests = %d, want 1 (50 bot rows should be excluded)", def.Totals.Requests)
	}

	all, err := s.Report(Query{Window: ParseWindow("24h"), WithBots: true, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if all.Totals.Requests != 51 {
		t.Errorf("bots=1 requests = %d, want 51", all.Totals.Requests)
	}
}

func TestReportFiltersNetwork(t *testing.T) {
	s := testStore(t, 30)
	s.Record(rec(testNow, func(r *Record) { r.Network = "mainnet" }))
	s.Record(rec(testNow, func(r *Record) { r.Network = "pearl" }))
	s.Record(rec(testNow, func(r *Record) { r.Network = "pearl" }))
	s.Flush()

	got, err := s.Report(Query{Window: ParseWindow("24h"), Network: "pearl", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if got.Totals.Requests != 2 {
		t.Errorf("pearl requests = %d, want 2 (the whole point of #448: networks must not collapse)", got.Totals.Requests)
	}
}

func TestReportWindowExcludesOlderRows(t *testing.T) {
	s := testStore(t, 0)
	s.Record(rec(testNow.Add(-2*time.Hour), nil))
	s.Record(rec(testNow.Add(-48*time.Hour), nil))
	s.Flush()

	day, err := s.Report(Query{Window: ParseWindow("24h"), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if day.Totals.Requests != 1 {
		t.Errorf("24h window = %d rows, want 1", day.Totals.Requests)
	}
	week, err := s.Report(Query{Window: ParseWindow("7d"), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if week.Totals.Requests != 2 {
		t.Errorf("7d window = %d rows, want 2", week.Totals.Requests)
	}
}

func TestReportEmptyWindow(t *testing.T) {
	s := testStore(t, 30)
	got, err := s.Report(Query{Window: ParseWindow("24h"), Now: testNow})
	if err != nil {
		t.Fatalf("an empty window must be an empty report, not an error: %v", err)
	}
	if !got.Empty {
		t.Error("Empty is false with no rows; the page would draw a dozen convincing zeroes")
	}
}

// Retention is a promise, and a promise nothing enforces is a lie.
func TestPruneDeletesPastRetention(t *testing.T) {
	s := testStore(t, 7)
	s.Record(rec(testNow.Add(-2*24*time.Hour), nil))
	s.Record(rec(testNow.Add(-30*24*time.Hour), nil))
	s.Flush()

	n, err := s.Prune(testNow)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1", n)
	}
	got, err := s.Report(Query{Window: ParseWindow("90d"), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if got.Totals.Requests != 1 {
		t.Errorf("%d rows survived, want 1", got.Totals.Requests)
	}
}

func TestPruneKeepsEverythingAtZeroRetention(t *testing.T) {
	s := testStore(t, 0)
	s.Record(rec(testNow.Add(-365*24*time.Hour), nil))
	s.Flush()
	if n, err := s.Prune(testNow); err != nil || n != 0 {
		t.Errorf("prune at retention 0 deleted %d rows (err %v), want 0", n, err)
	}
}

// A burst must cost bounded memory, and the drop must be visible rather than
// silent: a dashboard understating traffic with nothing saying so is worse
// than one that says it lost rows.
func TestRecordDropsPastBufferLimit(t *testing.T) {
	s := testStore(t, 30)
	for i := 0; i < bufferLimit+25; i++ {
		s.Record(rec(testNow, nil))
	}
	st := s.Stats()
	if st.Buffered != bufferLimit {
		t.Errorf("buffered = %d, want the cap %d", st.Buffered, bufferLimit)
	}
	if st.Dropped != 25 {
		t.Errorf("dropped = %d, want 25", st.Dropped)
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	s.Record(rec(testNow, nil))
	s.Flush()
	if _, err := s.Prune(testNow); err != nil {
		t.Errorf("prune on nil store: %v", err)
	}
	got, err := s.Report(Query{Window: ParseWindow("24h"), Now: testNow})
	if err != nil || !got.Empty {
		t.Errorf("nil store report = %+v, %v; want an empty report and no error", got, err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("close on nil store: %v", err)
	}
}

func TestParseWindowRejectsUnknown(t *testing.T) {
	for _, in := range []string{"", "1h", "all", "365d", "7D"} {
		if got := ParseWindow(in).Key; got != "7d" {
			t.Errorf("ParseWindow(%q) = %q, want the 7d fallback", in, got)
		}
	}
	if ParseWindow("24h").Key != "24h" || !ParseWindow("24h").ByHour {
		t.Error("24h must resolve to itself and bucket by hour")
	}
	if ParseWindow("30d").ByHour {
		t.Error("30d must not bucket by hour: 720 columns is not a chart")
	}
}
