package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func ev(network, kind, subject string, ordinal int, at string, height int64) DiscoverEvent {
	return DiscoverEvent{
		Network:   network,
		ID:        EventID(network, kind, subject, ordinal),
		Kind:      kind,
		At:        at,
		Height:    height,
		Actor:     "g1" + subject,
		Target:    "gno.land/r/ns/" + subject,
		Namespace: "ns",
		Facts:     json.RawMessage(`{"package_name":"` + subject + `"}`),
		Layers:    json.RawMessage(`{"means":{"text":"An app was published."}}`),
		BuiltAt:   "2026-09-27T00:00:00Z",
	}
}

func seed(t *testing.T, db *DB, events ...DiscoverEvent) {
	t.Helper()
	if _, err := db.UpsertDiscoverEvents(events); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func ids(events []DiscoverEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

// The id is the only thing a consumer can dedupe on, so the property that
// matters is not "inserting twice does not error" but "inserting twice changes
// nothing". A rebuild that produced new ids would re-notify every subscriber.
func TestARebuildInsertsNothingAndChangesNothing(t *testing.T) {
	db := NewTestDB(t)
	batch := []DiscoverEvent{
		ev("alpha", "package.deployed", "hello", 0, "2026-09-20T10:00:00Z", 100),
		ev("alpha", "package.enabled", "hello", 0, "2026-09-20T11:00:00Z", 110),
	}

	n, err := db.UpsertDiscoverEvents(batch)
	if err != nil || n != 2 {
		t.Fatalf("first insert: n=%d err=%v", n, err)
	}
	before, _, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha"})
	if err != nil {
		t.Fatal(err)
	}

	// The same events again, as a rebuild would produce them. Nothing changed,
	// so nothing is written: an unchanged row must not count, or every tick
	// reports hundreds of writes and the number stops meaning anything.
	n, err = db.UpsertDiscoverEvents(batch)
	if err != nil || n != 0 {
		t.Fatalf("rebuild wrote %d rows, want 0: %v", n, err)
	}
	after, _, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("row count changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].At != after[i].At {
			t.Errorf("row %d changed: %+v -> %+v", i, before[i], after[i])
		}
	}
}

// A path deployed on two chains is two events. Joining on anything but the full
// key is the way things go wrong in this schema, per AGENTS.md.
func TestEventsAreScopedToOneChain(t *testing.T) {
	db := NewTestDB(t)
	seed(t, db,
		ev("alpha", "package.deployed", "hello", 0, "2026-09-20T10:00:00Z", 100),
		ev("beta", "package.deployed", "hello", 0, "2026-09-20T10:00:00Z", 100),
	)

	for _, net := range []string{"alpha", "beta"} {
		got, _, err := db.DiscoverEvents(DiscoverQuery{Network: net})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d events, want 1", net, len(got))
		}
		if got[0].Network != net {
			t.Errorf("%s: got an event from %s", net, got[0].Network)
		}
	}

	if _, _, err := db.DiscoverEvents(DiscoverQuery{}); err == nil {
		t.Error("a query with no network was answered; capacity and counts are per chain")
	}
}

// The reason paging is keyset and not OFFSET: the feed grows at the head. With
// OFFSET, an insert between two requests shifts every later row down by one and
// the reader sees a row twice and misses another. Nothing else in this file
// would catch that.
func TestPagingIsStableWhileTheFeedGrows(t *testing.T) {
	db := NewTestDB(t)
	var batch []DiscoverEvent
	for i := 0; i < 10; i++ {
		batch = append(batch, ev("alpha", "package.deployed", fmt.Sprintf("p%02d", i), 0,
			fmt.Sprintf("2026-09-20T10:%02d:00Z", i), int64(100+i)))
	}
	seed(t, db, batch...)

	first, cursor, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha", Limit: 4})
	if err != nil || cursor == "" {
		t.Fatalf("first page: cursor=%q err=%v", cursor, err)
	}

	// Four newer events arrive between the two requests, which is the whole
	// point: they belong at the head and must not disturb the page below.
	var newer []DiscoverEvent
	for i := 10; i < 14; i++ {
		newer = append(newer, ev("alpha", "package.deployed", fmt.Sprintf("p%02d", i), 0,
			fmt.Sprintf("2026-09-20T11:%02d:00Z", i), int64(200+i)))
	}
	seed(t, db, newer...)

	seen := map[string]bool{}
	for _, id := range ids(first) {
		seen[id] = true
	}
	for cursor != "" {
		var page []DiscoverEvent
		page, cursor, err = db.DiscoverEvents(DiscoverQuery{Network: "alpha", Limit: 4, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids(page) {
			if seen[id] {
				t.Fatalf("%s came back on two pages", id)
			}
			seen[id] = true
		}
	}
	// Ten original events, each exactly once. The four that arrived mid-read
	// are legitimately absent: they are newer than where the reader started.
	if len(seen) != 10 {
		t.Fatalf("saw %d distinct events, want the 10 that existed when paging began", len(seen))
	}
	for i := 0; i < 10; i++ {
		id := EventID("alpha", "package.deployed", fmt.Sprintf("p%02d", i), 0)
		if !seen[id] {
			t.Errorf("%s was skipped", id)
		}
	}
}

// Two events in one block share an `at`. If the tie-break were not part of both
// the ordering and the cursor, one of them would be skipped at a page boundary.
func TestEventsInTheSameBlockDoNotBreakPaging(t *testing.T) {
	db := NewTestDB(t)
	const sameAt = "2026-09-20T10:00:00Z"
	var batch []DiscoverEvent
	for i := 0; i < 6; i++ {
		batch = append(batch, ev("alpha", "package.deployed", fmt.Sprintf("p%d", i), i, sameAt, 100))
	}
	seed(t, db, batch...)

	seen := map[string]bool{}
	cursor := ""
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("paging did not terminate")
		}
		got, next, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids(got) {
			if seen[id] {
				t.Fatalf("%s came back twice", id)
			}
			seen[id] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 6 {
		t.Fatalf("saw %d of 6 events that share a timestamp", len(seen))
	}
}

func TestFilters(t *testing.T) {
	db := NewTestDB(t)
	a := ev("alpha", "package.deployed", "hello", 0, "2026-09-20T10:00:00Z", 100)
	b := ev("alpha", "chain.spike", "2026-09-21", 0, "2026-09-21T10:00:00Z", 200)
	b.Namespace = ""
	b.Actor = ""
	c := ev("alpha", "package.deployed", "world", 0, "2026-09-22T10:00:00Z", 300)
	c.Namespace = "other"
	seed(t, db, a, b, c)

	tests := []struct {
		name string
		q    DiscoverQuery
		want []string
	}{
		{"by kind", DiscoverQuery{Network: "alpha", Kinds: []string{"chain.spike"}}, []string{b.ID}},
		{"by two kinds", DiscoverQuery{Network: "alpha", Kinds: []string{"chain.spike", "package.deployed"}},
			[]string{c.ID, b.ID, a.ID}},
		{"by namespace", DiscoverQuery{Network: "alpha", Namespace: "ns"}, []string{a.ID}},
		{"by actor", DiscoverQuery{Network: "alpha", Actor: c.Actor}, []string{c.ID}},
		{"since is inclusive", DiscoverQuery{Network: "alpha", Since: "2026-09-21T10:00:00Z"},
			[]string{c.ID, b.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := db.DiscoverEvents(tt.q)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(ids(got)) != fmt.Sprint(tt.want) {
				t.Errorf("got %v, want %v", ids(got), tt.want)
			}
		})
	}
}

// The cursor arrives in a URL, so it is attacker-controlled. The worst a
// malformed one should do is start the reader at the top; returning an error
// would turn a mangled link into a broken page.
func TestAMalformedCursorStartsAtTheTop(t *testing.T) {
	db := NewTestDB(t)
	seed(t, db, ev("alpha", "package.deployed", "hello", 0, "2026-09-20T10:00:00Z", 100))

	for _, bad := range []string{"nonsense", "|", "|id", "2026-09-20T10:00:00Z|", "' OR 1=1 --"} {
		got, _, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha", Cursor: bad})
		if err != nil {
			t.Errorf("cursor %q errored: %v", bad, err)
		}
		if len(got) != 1 {
			t.Errorf("cursor %q returned %d events, want the whole feed", bad, len(got))
		}
	}
}

// A chain whose block-1 fingerprint changed has a new set of events. Rows left
// behind would keep a feed republishing events for blocks that no longer exist,
// with ids no rebuild will ever produce again.
func TestAChainResetWipesTheFeed(t *testing.T) {
	db := NewTestDB(t)
	seed(t, db,
		ev("alpha", "package.deployed", "hello", 0, "2026-09-20T10:00:00Z", 100),
		ev("beta", "package.deployed", "hello", 0, "2026-09-20T10:00:00Z", 100),
	)

	if _, err := db.DeleteNetworkData("alpha"); err != nil {
		t.Fatal(err)
	}

	got, _, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("alpha kept %d events through a reset", len(got))
	}
	// And the other chain is untouched, which is the half a blanket DELETE
	// would get wrong.
	other, _, err := db.DiscoverEvents(DiscoverQuery{Network: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 {
		t.Errorf("beta lost events to alpha's reset: %d remain", len(other))
	}
}

func TestCountsAndGeneration(t *testing.T) {
	db := NewTestDB(t)
	a := ev("alpha", "package.deployed", "hello", 0, "2026-09-20T10:00:00Z", 100)
	b := ev("alpha", "package.deployed", "world", 0, "2026-09-21T10:00:00Z", 200)
	c := ev("alpha", "chain.spike", "2026-09-21", 0, "2026-09-21T11:00:00Z", 210)
	c.BuiltAt = "2026-09-27T01:00:00Z" // a later generation
	seed(t, db, a, b, c)

	counts, err := db.DiscoverCounts("alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	if counts["package.deployed"] != 2 || counts["chain.spike"] != 1 {
		t.Errorf("counts = %v", counts)
	}

	windowed, err := db.DiscoverCounts("alpha", "2026-09-21T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if windowed["package.deployed"] != 1 {
		t.Errorf("windowed counts = %v, want one deploy inside the window", windowed)
	}

	built, err := db.DiscoverBuiltAt("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if built != "2026-09-27T01:00:00Z" {
		t.Errorf("built_at = %q, want the newest generation", built)
	}
	// An empty chain has no generation rather than an error: the page still
	// renders, and says the tick has not run.
	if got, err := db.DiscoverBuiltAt("gamma"); err != nil || got != "" {
		t.Errorf("empty chain: got %q err=%v", got, err)
	}
}

// The cap is a cap, not a default a caller may raise: a page of these rows
// carries two blocks of generated prose each.
func TestTheLimitIsCapped(t *testing.T) {
	db := NewTestDB(t)
	var batch []DiscoverEvent
	for i := 0; i < 5; i++ {
		batch = append(batch, ev("alpha", "package.deployed", fmt.Sprintf("p%d", i), 0,
			fmt.Sprintf("2026-09-20T10:%02d:00Z", i), int64(100+i)))
	}
	seed(t, db, batch...)

	for _, limit := range []int{0, -1, DiscoverLimitMax + 1, 10_000} {
		got, _, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha", Limit: limit})
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if len(got) != 5 {
			t.Errorf("limit %d returned %d events", limit, len(got))
		}
	}
}

func TestUpsertRejectsAnEventWithNoIdentity(t *testing.T) {
	db := NewTestDB(t)
	for _, bad := range []DiscoverEvent{
		{Network: "alpha", At: "2026-09-20T10:00:00Z"},
		{ID: "x", At: "2026-09-20T10:00:00Z"},
		{Network: "alpha", ID: "x"},
	} {
		if _, err := db.UpsertDiscoverEvents([]DiscoverEvent{bad}); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// The derived half of a row refreshes; its identity does not.
//
// This is the failure that produced the rule. A layer 1 template fix left
// already-stored rows carrying the old wording, and adding a score column left
// every existing row at zero, which would have sorted them all off the bottom
// of the page with nothing to say why. Neither is history a subscriber read.
func TestARebuildRefreshesTheDerivedHalfAndNotTheIdentity(t *testing.T) {
	db := NewTestDB(t)
	first := ev("alpha", "package.deployed", "hello", 0, "2026-09-20T10:00:00Z", 100)
	first.ScoreBase = 0
	if _, err := db.UpsertDiscoverEvents([]DiscoverEvent{first}); err != nil {
		t.Fatal(err)
	}

	// The same event, rebuilt by software that now scores it and words it
	// differently. Same id, because the id is a function of the event.
	second := first
	second.ScoreBase = 41.8
	second.Layers = []byte(`{"means":{"text":"An app was published, reworded."}}`)
	n, err := db.UpsertDiscoverEvents([]DiscoverEvent{second})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the refresh wrote %d rows, want 1", n)
	}

	got, _, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d rows, want 1: a refresh must not duplicate the event", len(got))
	}
	if got[0].ScoreBase != 41.8 {
		t.Errorf("score = %v, want the refreshed 41.8", got[0].ScoreBase)
	}
	if !strings.Contains(string(got[0].Layers), "reworded") {
		t.Errorf("layers were not refreshed: %s", got[0].Layers)
	}
	// Identity untouched, which is the whole contract: consumers dedupe on it.
	if got[0].ID != first.ID || got[0].At != first.At || got[0].Height != first.Height {
		t.Errorf("identity changed: %+v", got[0])
	}
}

// A ranked page cannot be built from a time-ordered fetch.
//
// The candidate pool is bounded, so its *ordering* decides what can be ranked
// at all. Taking the newest N and sorting those by score answers "the best of
// the most recent N", which is a different question. Measured on mainnet
// 2026-09-28, a 200-row time-ordered pool held 11 of the 21 deployer.first
// events, so ten of the highest-scoring events on the chain could not reach the
// page by any route.
func TestAScoreOrderedPoolSeesWhatATimeOrderedOneCannot(t *testing.T) {
	db := NewTestDB(t)

	// One high-scoring event, older than a run of low-scoring ones.
	best := ev("alpha", "deployer.first", "best", 0, "2026-09-01T10:00:00Z", 100)
	best.ScoreBase = 600
	seed(t, db, best)
	for i := 0; i < 5; i++ {
		e := ev("alpha", "package.deployed", fmt.Sprintf("noise%d", i), 0,
			fmt.Sprintf("2026-09-2%dT10:00:00Z", i), int64(200+i))
		e.ScoreBase = 25
		seed(t, db, e)
	}

	// Time order with a small pool: the best event is not in it.
	byTime, _, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range byTime {
		if e.ID == best.ID {
			t.Fatal("the fixture does not reproduce the bug: widen the gap")
		}
	}

	// Score order with the same pool: it is.
	byScore, _, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha", Limit: 3, ByScore: true})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range byScore {
		if e.ID == best.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("the highest-scoring event is missing from a score-ordered pool: %v", ids(byScore))
	}
	if byScore[0].ScoreBase != 600 {
		t.Errorf("the pool is not ordered by score: first is %v", byScore[0].ScoreBase)
	}
}

func TestDiscoverTotalCountsTheFilterNotThePage(t *testing.T) {
	db := NewTestDB(t)
	for i := 0; i < 7; i++ {
		e := ev("alpha", "package.deployed", fmt.Sprintf("p%d", i), 0,
			fmt.Sprintf("2026-09-2%dT10:00:00Z", i), int64(100+i))
		if i < 3 {
			e.Namespace = "one"
		}
		seed(t, db, e)
	}
	seed(t, db, ev("beta", "package.deployed", "other", 0, "2026-09-20T10:00:00Z", 100))

	n, err := db.DiscoverTotal(DiscoverQuery{Network: "alpha", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("total = %d, want 7: the page limit must not reach the count", n)
	}
	if n, _ := db.DiscoverTotal(DiscoverQuery{Network: "alpha", Namespace: "one"}); n != 3 {
		t.Errorf("filtered total = %d, want 3", n)
	}
	if _, err := db.DiscoverTotal(DiscoverQuery{}); err == nil {
		t.Error("counted across every chain")
	}
}

// Asking for more than the cap gives the cap, not the default.
//
// These were one branch, so a request for 1000 silently became 50. The API's
// candidate pool asked for 1000 and ranked an eighth of the window, reporting
// nothing unusual while doing it.
func TestAnOversizedLimitClampsToTheCapNotTheDefault(t *testing.T) {
	db := NewTestDB(t)
	for i := 0; i < DiscoverLimitMax+20; i++ {
		seed(t, db, ev("alpha", "package.deployed", fmt.Sprintf("p%03d", i), 0,
			fmt.Sprintf("2026-09-20T10:%02d:%02dZ", i/60, i%60), int64(100+i)))
	}

	got, _, err := db.DiscoverEvents(DiscoverQuery{Network: "alpha", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != DiscoverLimitMax {
		t.Errorf("asked for 1000 and got %d, want the cap of %d", len(got), DiscoverLimitMax)
	}

	// And an unset limit still gets the default, which is the other half.
	got, _, err = db.DiscoverEvents(DiscoverQuery{Network: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Errorf("unset limit returned %d, want the default 50", len(got))
	}
}
