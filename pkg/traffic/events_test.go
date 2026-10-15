package traffic

import (
	"strings"
	"testing"
	"time"
)

func ev(at time.Time, mut func(*Event)) Event {
	e := Event{
		At: at, Visitor: "v0", Kind: "leave", Path: "/realms",
		DwellMS: 20000, ScrollPct: 40, Client: "browser",
	}
	if mut != nil {
		mut(&e)
	}
	return e
}

func TestEngagementReadsVisits(t *testing.T) {
	s := testStore(t, 30)
	// Three visits with a deliberate spread, so a median and a p90 can be
	// told apart from a mean: a mean of these is 20.3s and the median is 5s.
	for _, d := range []int64{1000, 5000, 55000} {
		s.RecordEvent(ev(testNow, func(e *Event) { e.DwellMS = d }))
	}
	s.RecordEvent(ev(testNow, func(e *Event) {
		e.Kind, e.Action, e.Visitor = "action", "tab:usage", "v1"
	}))
	s.RecordEvent(ev(testNow, func(e *Event) {
		e.Kind, e.Value, e.Results = "search", "gnoswap", 4
	}))
	s.RecordEvent(ev(testNow, func(e *Event) {
		e.Kind, e.Value, e.Results = "search", "nothing here", 0
	}))
	s.RecordEvent(ev(testNow, func(e *Event) {
		e.Kind, e.Value = "error", "TypeError: x is not a function"
	}))
	s.Flush()

	got, err := s.Engagement(Query{Window: ParseWindow("24h"), Now: testNow})
	if err != nil {
		t.Fatalf("engagement: %v", err)
	}
	if got.Visits != 3 {
		t.Errorf("visits = %d, want 3 (only leave events are visits)", got.Visits)
	}
	if got.MedianDwellMS != 5000 {
		t.Errorf("median dwell = %d, want 5000", got.MedianDwellMS)
	}
	if got.P90DwellMS <= got.MedianDwellMS {
		t.Errorf("p90 %d not above median %d: the percentile query is returning one number twice",
			got.P90DwellMS, got.MedianDwellMS)
	}
	if len(got.Actions) != 1 || got.Actions[0].Label != "tab:usage" {
		t.Errorf("actions = %+v, want one row for tab:usage", got.Actions)
	}
	if len(got.Searches) != 2 {
		t.Errorf("searches = %+v, want both terms", got.Searches)
	}
	if len(got.ZeroSearches) != 1 || got.ZeroSearches[0].Label != "nothing here" {
		t.Errorf("zero searches = %+v, want only the one that found nothing", got.ZeroSearches)
	}
	if len(got.Errors) != 1 {
		t.Errorf("errors = %+v, want one", got.Errors)
	}
}

// A visit that bounced and a visit that was read must not land in the same
// bucket, because every panel on the page is a variation on telling them apart.
func TestEngagementRates(t *testing.T) {
	s := testStore(t, 30)
	// Two bounces: under ten seconds, nothing touched.
	s.RecordEvent(ev(testNow, func(e *Event) { e.DwellMS, e.ScrollPct, e.Actions = 2000, 5, 0 }))
	s.RecordEvent(ev(testNow, func(e *Event) { e.DwellMS, e.ScrollPct, e.Actions = 9000, 0, 0 }))
	// One read: long enough, scrolled, and something was clicked.
	s.RecordEvent(ev(testNow, func(e *Event) { e.DwellMS, e.ScrollPct, e.Actions = 90000, 80, 3 }))
	// One in between: short, but the reader did something. Neither bounce nor
	// engaged-by-time, and engaged by interaction.
	s.RecordEvent(ev(testNow, func(e *Event) { e.DwellMS, e.ScrollPct, e.Actions = 4000, 10, 1 }))
	s.Flush()

	got, err := s.Engagement(Query{Window: ParseWindow("24h"), Now: testNow})
	if err != nil {
		t.Fatalf("engagement: %v", err)
	}
	if got.BounceRate != 0.5 {
		t.Errorf("bounce rate = %v, want 0.5 (2 of 4)", got.BounceRate)
	}
	if got.EngagedRate != 0.5 {
		t.Errorf("engaged rate = %v, want 0.5 (2 of 4)", got.EngagedRate)
	}
	// The distribution keeps its own order, not the count order: the shape is
	// the answer and sorting by frequency destroys it.
	want := []string{"0-5s", "5-15s", "1-5m"}
	if len(got.DwellBuckets) != len(want) {
		t.Fatalf("dwell buckets = %+v, want %v", got.DwellBuckets, want)
	}
	for i, w := range want {
		if got.DwellBuckets[i].Label != w {
			t.Errorf("dwell bucket %d = %q, want %q (buckets must stay in time order)",
				i, got.DwellBuckets[i].Label, w)
		}
	}
}

func TestEngagementPathsAndExits(t *testing.T) {
	s := testStore(t, 30)
	// One reader walking three pages, in order.
	s.RecordEvent(ev(testNow.Add(-2*time.Minute), func(e *Event) { e.Path, e.From = "/", "" }))
	s.RecordEvent(ev(testNow.Add(-time.Minute), func(e *Event) { e.Path, e.From = "/realms", "/" }))
	s.RecordEvent(ev(testNow, func(e *Event) { e.Path, e.From = "/apps", "/realms" }))
	s.Flush()

	got, err := s.Engagement(Query{Window: ParseWindow("24h"), Now: testNow})
	if err != nil {
		t.Fatalf("engagement: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Label != "/" {
		t.Errorf("entries = %+v, want the one visit with no from_path", got.Entries)
	}
	if len(got.Edges) != 2 {
		t.Errorf("edges = %+v, want two moves", got.Edges)
	}
	for _, e := range got.Edges {
		if !strings.Contains(e.Label, "→") {
			t.Errorf("edge %q is not a from → to pair", e.Label)
		}
	}
	if len(got.Exits) != 1 || got.Exits[0].Label != "/apps" {
		t.Errorf("exits = %+v, want the last page of the walk", got.Exits)
	}
}

// Everything the client sends is clamped here, because this is the one
// endpoint anybody can write to. A browser sends none of these values; a curl
// sends all of them.
func TestEventNormalizeClamps(t *testing.T) {
	cases := []struct {
		name  string
		in    Event
		keep  bool
		check func(*testing.T, Event)
	}{
		{
			name: "unknown kind is dropped",
			in:   Event{Kind: "exfiltrate", Path: "/"},
		},
		{
			name: "a dwell of a year is clamped to four hours",
			in:   Event{Kind: "leave", Path: "/", DwellMS: 365 * 24 * 3600 * 1000},
			keep: true,
			check: func(t *testing.T, e Event) {
				if e.DwellMS != maxDwellMS {
					t.Errorf("dwell = %d, want %d", e.DwellMS, maxDwellMS)
				}
			},
		},
		{
			name: "a negative scroll is zero and 900% is 100",
			in:   Event{Kind: "leave", Path: "/", ScrollPct: 900},
			keep: true,
			check: func(t *testing.T, e Event) {
				if e.ScrollPct != 100 {
					t.Errorf("scroll = %d, want 100", e.ScrollPct)
				}
			},
		},
		{
			name: "a long label is clipped",
			in:   Event{Kind: "action", Path: "/", Action: strings.Repeat("x", 500)},
			keep: true,
			check: func(t *testing.T, e Event) {
				if len(e.Action) > maxLabelLen+4 {
					t.Errorf("action label is %d bytes, want it clipped to %d", len(e.Action), maxLabelLen)
				}
			},
		},
		{
			name: "an action with no label cannot be grouped and is dropped",
			in:   Event{Kind: "action", Path: "/"},
		},
		{
			name: "a search with no term is a keystroke",
			in:   Event{Kind: "search", Path: "/"},
		},
		{
			name: "a self-edge is not navigation",
			in:   Event{Kind: "leave", Path: "/realms", From: "/realms"},
			keep: true,
			check: func(t *testing.T, e Event) {
				if e.From != "" {
					t.Errorf("from = %q, want it cleared", e.From)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.in
			got := e.normalize()
			if got != tc.keep {
				t.Fatalf("normalize kept = %v, want %v", got, tc.keep)
			}
			if tc.check != nil {
				tc.check(t, e)
			}
		})
	}
}

// Retention is one promise over two tables. Kept here because the events half
// arrived second and is the half a future change would forget.
func TestPruneDeletesEventsToo(t *testing.T) {
	s := testStore(t, 7)
	old := testNow.AddDate(0, 0, -30)
	s.Record(rec(old, nil))
	s.RecordEvent(ev(old, nil))
	s.RecordEvent(ev(testNow, nil))
	s.Flush()

	n, err := s.Prune(testNow)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned %d rows, want 2 (one request and one event)", n)
	}
	got, err := s.Engagement(Query{Window: ParseWindow("90d"), Now: testNow})
	if err != nil {
		t.Fatalf("engagement: %v", err)
	}
	if got.Visits != 1 {
		t.Errorf("visits after prune = %d, want 1", got.Visits)
	}
}

// The beacon's own request row describes nothing a reader did. Counted in the
// request log, it would add one row per page left to every "all requests"
// figure on the dashboard.
func TestBeaconRowsStayOutOfRequestPanels(t *testing.T) {
	s := testStore(t, 30)
	s.Record(rec(testNow, func(r *Record) { r.Kind, r.Route = "beacon", EventPath }))
	s.Record(rec(testNow, nil))
	s.Flush()

	got, err := s.Report(Query{Window: ParseWindow("24h"), Now: testNow})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if got.Totals.Requests != 1 {
		t.Errorf("requests = %d, want 1: the beacon row is in the default view", got.Totals.Requests)
	}
	asked, err := s.Report(Query{Window: ParseWindow("24h"), Kind: "beacon", Now: testNow})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if asked.Totals.Requests != 1 {
		t.Errorf("requests with kind=beacon = %d, want 1: it must stay reachable", asked.Totals.Requests)
	}
}
