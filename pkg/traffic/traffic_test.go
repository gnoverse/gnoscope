package traffic

import (
	"database/sql"
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

	all, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if all.Totals.Requests != 51 {
		t.Errorf("who=all requests = %d, want 51", all.Totals.Requests)
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

// Measured on val1 on 2026-09-29: 117 recorded requests against 25 that
// actually crossed the network. Every panel was inflated by the server talking
// to itself, and unlike a crawler there is no view in which that belongs.
func TestReportNeverCountsTheWarmer(t *testing.T) {
	s := testStore(t, 30)
	s.Record(rec(testNow, func(r *Record) { r.Visitor, r.Client = "human", "browser" }))
	for i := 0; i < 92; i++ {
		s.Record(rec(testNow, func(r *Record) { r.Visitor, r.Client = "warmer", "internal" }))
	}
	s.Flush()

	for _, who := range []Who{WhoNonCrawlers, WhoAll} {
		got, err := s.Report(Query{Window: ParseWindow("24h"), Who: who, Now: testNow})
		if err != nil {
			t.Fatal(err)
		}
		if got.Totals.Requests != 1 {
			t.Errorf("who=%s: requests = %d, want 1; the warmer must never be counted, "+
				"in any who bucket", who, got.Totals.Requests)
		}
		for _, c := range got.Clients {
			if c.Label == "internal" {
				t.Errorf("who=%s: the warmer appears in the client breakdown: %+v", who, c)
			}
		}
	}
}

// The host is recorded so "only this site" is checkable rather than assumed.
// This server answers to its canonical name, its www form, and whatever
// redirects at it, and every panel used to treat them as one place.
func TestReportFiltersHost(t *testing.T) {
	s := testStore(t, 30)
	for i := 0; i < 5; i++ {
		s.Record(rec(testNow, func(r *Record) { r.Host = "gnoscope.com" }))
	}
	s.Record(rec(testNow, func(r *Record) { r.Host = "www.gnoscope.com" }))
	s.Record(rec(testNow, func(r *Record) { r.Host = "" })) // pre-migration row
	s.Flush()

	one, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Host: "gnoscope.com", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	// 5 for this host, plus the one row whose host was never recorded. Unknown
	// matches every host filter rather than none: the strict version made a
	// default-filtered page look like the history had been erased.
	if one.Totals.Requests != 6 {
		t.Errorf("host filter = %d rows, want 6 (5 matching plus 1 unrecorded)", one.Totals.Requests)
	}
	// The other host is still excluded, or the filter does nothing.
	other, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Host: "www.gnoscope.com", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if other.Totals.Requests != 2 {
		t.Errorf("www filter = %d rows, want 2 (1 matching plus 1 unrecorded)", other.Totals.Requests)
	}

	// The hosts panel ignores the host filter on purpose: a panel that only
	// showed the host already selected could not tell anyone a second exists.
	labels := map[string]int64{}
	for _, c := range one.Hosts {
		labels[c.Label] = c.Hits
	}
	if labels["www.gnoscope.com"] != 1 {
		t.Errorf("hosts panel lost the other host while filtered: %+v", one.Hosts)
	}
	if labels["(not recorded)"] != 1 {
		t.Errorf("pre-migration rows must be visible as (not recorded): %+v", one.Hosts)
	}
	if one.Unrecorded != 1 {
		t.Errorf("unrecorded_host = %d, want 1; a host filter hides these and the page has to say so", one.Unrecorded)
	}

	all, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if all.Totals.Requests != 7 {
		t.Errorf("no host filter = %d rows, want all 7", all.Totals.Requests)
	}
}

// who replaced a two-state include-crawlers toggle, which could not answer
// either of the questions people actually have: how much of this is machines,
// and which machines.
func TestReportWhoBuckets(t *testing.T) {
	s := testStore(t, 30)
	add := func(n int, client string) {
		for i := 0; i < n; i++ {
			s.Record(rec(testNow, func(r *Record) { r.Client = client; r.Visitor = client }))
		}
	}
	add(10, "browser")
	add(7, "bot")
	add(4, "agent")
	add(2, "unknown")
	add(99, "internal") // the warmer, never counted anywhere
	s.Flush()

	for _, tc := range []struct {
		who  Who
		want int64
	}{
		{WhoAll, 23},         // everything but the warmer
		{WhoNonCrawlers, 16}, // browser + agent + unknown
		{WhoCrawlers, 7},
		{WhoPeople, 10},
		{WhoAgents, 4},
		{WhoUnknown, 2},
	} {
		got, err := s.Report(Query{Window: ParseWindow("24h"), Who: tc.who, Now: testNow})
		if err != nil {
			t.Fatal(err)
		}
		if got.Totals.Requests != tc.want {
			t.Errorf("who=%s: %d requests, want %d", tc.who, got.Totals.Requests, tc.want)
		}
	}
}

func TestParseWhoFallsBackToNonCrawlers(t *testing.T) {
	for _, in := range []string{"", "humans", "bots", "ALL", "everyone"} {
		if got := ParseWho(in); got != WhoNonCrawlers {
			t.Errorf("ParseWho(%q) = %q, want %q", in, got, WhoNonCrawlers)
		}
	}
	// And the real ones resolve to themselves, or this guard has quietly
	// collapsed every bucket into the default.
	for _, in := range []Who{WhoAll, WhoCrawlers, WhoPeople, WhoAgents, WhoUnknown, WhoNonCrawlers} {
		if got := ParseWho(string(in)); got != in {
			t.Errorf("ParseWho(%q) = %q, want itself", in, got)
		}
	}
}

func TestReportErrorsOnly(t *testing.T) {
	s := testStore(t, 30)
	for i := 0; i < 8; i++ {
		s.Record(rec(testNow, nil)) // 200
	}
	s.Record(rec(testNow, func(r *Record) { r.Status = 404 }))
	s.Record(rec(testNow, func(r *Record) { r.Status = 500 }))
	s.Flush()

	got, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, ErrorsOnly: true, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if got.Totals.Requests != 2 {
		t.Errorf("errors only = %d, want 2", got.Totals.Requests)
	}
}

// A database written before the host column existed must open, not fail, and
// its rows must stay readable rather than being guessed at.
func TestOpenMigratesDatabaseWithoutHostColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	// The schema as it shipped, without host.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE requests (
		ts INTEGER NOT NULL, day TEXT NOT NULL, hour INTEGER NOT NULL,
		visitor TEXT NOT NULL DEFAULT '', method TEXT NOT NULL DEFAULT '',
		route TEXT NOT NULL DEFAULT '', target TEXT NOT NULL DEFAULT '',
		network TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '',
		tool TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 0,
		bytes INTEGER NOT NULL DEFAULT 0, dur_ms REAL NOT NULL DEFAULT 0,
		app_ms REAL NOT NULL DEFAULT 0, cache TEXT NOT NULL DEFAULT '',
		ref_host TEXT NOT NULL DEFAULT '', client TEXT NOT NULL DEFAULT '',
		robot INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO requests (ts, day, hour, client, status, route)
		VALUES (?, ?, ?, 'browser', 200, '/api/stats')`,
		testNow.Unix(), testNow.UTC().Format("2006-01-02"), testNow.UTC().Hour()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path, 30)
	if err != nil {
		t.Fatalf("opening a pre-host database must migrate, not fail: %v", err)
	}
	defer s.Close()

	// The old row survives and is reachable, with its host honestly empty.
	got, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if got.Totals.Requests != 1 {
		t.Fatalf("the pre-migration row was lost: %d requests", got.Totals.Requests)
	}
	if got.Unrecorded != 1 {
		t.Errorf("unrecorded_host = %d, want 1", got.Unrecorded)
	}
	// And a new row records its host, so the column is actually being written.
	s.Record(rec(testNow, func(r *Record) { r.Host = "gnoscope.com" }))
	s.Flush()
	after, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Host: "gnoscope.com", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	// 1 new row carrying the host, plus the pre-migration row whose host is
	// unknown and therefore matches every filter.
	if after.Totals.Requests != 2 {
		t.Errorf("host filter after migration = %d, want 2", after.Totals.Requests)
	}
}

// Adding page_kind is the moment kind='page' changes meaning, so it is the
// moment the rows written under the old meaning have to be corrected.
//
// Before the beacon existed every non-API URL was classified 'page' and every
// one of those rows is a document load. Left alone, the headline figure would
// be document loads before the deploy and page views after it, on one
// continuous chart, with nothing marking where the meaning changed.
func TestMigrationReclassifiesPreBeaconPageRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The schema as it stood after host arrived and before page_kind did.
	if _, err := db.Exec(`CREATE TABLE requests (
		ts INTEGER NOT NULL, day TEXT NOT NULL, hour INTEGER NOT NULL,
		visitor TEXT NOT NULL DEFAULT '', host TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT '', route TEXT NOT NULL DEFAULT '',
		target TEXT NOT NULL DEFAULT '', network TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT '', tool TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0,
		dur_ms REAL NOT NULL DEFAULT 0, app_ms REAL NOT NULL DEFAULT 0,
		cache TEXT NOT NULL DEFAULT '', ref_host TEXT NOT NULL DEFAULT '',
		client TEXT NOT NULL DEFAULT '', robot INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	ins := func(kind, target string) {
		if _, err := db.Exec(`INSERT INTO requests (ts, day, hour, kind, target, client, status)
			VALUES (?,?,?,?,?,'browser',200)`,
			testNow.Unix(), testNow.UTC().Format("2006-01-02"), testNow.UTC().Hour(), kind, target); err != nil {
			t.Fatal(err)
		}
	}
	ins("page", "/realms")
	ins("page", "/")
	ins("api", "")
	ins("mcp", "")
	db.Close()

	s, err := Open(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// The two old 'page' rows are documents now.
	docs, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Kind: "document", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if docs.Totals.Requests != 2 {
		t.Errorf("documents = %d, want 2; pre-beacon page rows were not reclassified", docs.Totals.Requests)
	}
	pages, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Kind: "page", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if pages.Totals.Requests != 0 {
		t.Errorf("page views = %d, want 0; history cannot contain page views that were never reported",
			pages.Totals.Requests)
	}
	// Nothing else was touched.
	all, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if all.Totals.Requests != 4 {
		t.Errorf("total = %d, want 4; the reclassification must move rows, not delete them", all.Totals.Requests)
	}

	// And it is one-shot: a beacon row written afterwards stays a page view.
	s.Record(rec(testNow, func(r *Record) { r.Kind = "page"; r.PageKind = "listing"; r.Target = "/apps" }))
	s.Flush()
	again, err := Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	after, err := again.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Kind: "page", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if after.Totals.Requests != 1 {
		t.Errorf("page views after reopen = %d, want 1; the reclassification must not run twice",
			after.Totals.Requests)
	}
}

// The correction must not depend on deploy order.
//
// The first version ran only when the page_kind column was newly added, which
// is true on a database that jumps straight to this build and false on one that
// already took the column-adding build. Deploy those in the wrong order and the
// fix skips silently, forever, on exactly the database that needs it.
func TestReclassifyRunsOnADatabaseThatAlreadyHasTheColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "already.db")

	// The schema as it stands *after* the column-adding build has run, holding
	// rows it classified under the old meaning.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE requests (
		ts INTEGER NOT NULL, day TEXT NOT NULL, hour INTEGER NOT NULL,
		visitor TEXT NOT NULL DEFAULT '', host TEXT NOT NULL DEFAULT '',
		entity_kind TEXT NOT NULL DEFAULT '', entity TEXT NOT NULL DEFAULT '',
		page_kind TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT '', route TEXT NOT NULL DEFAULT '',
		target TEXT NOT NULL DEFAULT '', network TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT '', tool TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0,
		dur_ms REAL NOT NULL DEFAULT 0, app_ms REAL NOT NULL DEFAULT 0,
		cache TEXT NOT NULL DEFAULT '', ref_host TEXT NOT NULL DEFAULT '',
		client TEXT NOT NULL DEFAULT '', robot INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	// Two pre-beacon rows: kind page, no page_kind. And one real beacon row.
	if _, err := db.Exec(`INSERT INTO requests (ts, day, hour, kind, target, page_kind, client, status) VALUES
		(?,?,?,'page','/realms','','browser',200),
		(?,?,?,'page','/','','browser',200),
		(?,?,?,'page','/apps','listing','browser',200)`,
		testNow.Unix(), testNow.UTC().Format("2006-01-02"), testNow.UTC().Hour(),
		testNow.Unix(), testNow.UTC().Format("2006-01-02"), testNow.UTC().Hour(),
		testNow.Unix(), testNow.UTC().Format("2006-01-02"), testNow.UTC().Hour()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pages, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Kind: "page", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	// Only the row the beacon actually wrote survives as a page view.
	if pages.Totals.Requests != 1 {
		t.Errorf("page views = %d, want 1; the correction skipped a database that already had the column",
			pages.Totals.Requests)
	}
	docs, err := s.Report(Query{Window: ParseWindow("24h"), Who: WhoAll, Kind: "document", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if docs.Totals.Requests != 2 {
		t.Errorf("documents = %d, want 2", docs.Totals.Requests)
	}
}
