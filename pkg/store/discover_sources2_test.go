package store

import (
	"fmt"
	"testing"
	"time"
)

// The four sources added alongside the first three. Each test seeds the table
// the source reads and asserts on the event that comes out the far end of the
// whole pipeline, gate included, rather than on the query: a source that
// produces a row the gate then refuses is a source that produces nothing, and
// asserting on the SQL would not notice.

func mustValoper(t *testing.T, db *DB, network, hash string, height int, ts time.Time, fn, address, moniker string, success bool) {
	t.Helper()
	if err := db.InsertValoperRegistration(network, hash, height, rfc3339(ts),
		address, fn, address, moniker, success); err != nil {
		t.Fatalf("insert valoper registration: %v", err)
	}
}

func mustBankSend(t *testing.T, db *DB, network, hash string, height int, ts time.Time, from, to, amount string) {
	t.Helper()
	if err := db.InsertBankSend(network, hash, height, rfc3339(ts), from, to, amount, true); err != nil {
		t.Fatalf("insert bank send: %v", err)
	}
}

// A validator registering is an event; a validator updating its website is not.
//
// Both go through the same contract call, so the filter is on whether the call
// actually set a name. Without it the feed reports every contact-detail edit as
// a new validator arriving.
func TestValidatorRegistrationsAreEventsAndDescriptionEditsUsuallyAreNot(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	mustValoper(t, db, "alpha", "TXV1", 100, recent(3), "Register", "g1val1", "HazenNetwork", true)
	// Sets a name, so it is news.
	mustValoper(t, db, "alpha", "TXV2", 110, recent(2), "UpdateDescription", "g1val2", "Renamed", true)
	// Sets no name: a website or a contact edit, which is not.
	mustValoper(t, db, "alpha", "TXV3", 120, recent(2), "UpdateDescription", "g1val3", "", true)
	// Failed, so it registered nobody.
	mustValoper(t, db, "alpha", "TXV4", 130, recent(1), "Register", "g1val4", "NeverLanded", false)

	events := buildAndRead(t, db, "alpha")
	got := map[string]bool{}
	for _, e := range events {
		if e.Kind == "validator.registered" {
			got[e.Actor] = true
		}
	}
	for _, want := range []string{"g1val1", "g1val2"} {
		if !got[want] {
			t.Errorf("%s produced no validator.registered event", want)
		}
	}
	for _, unwanted := range []string{"g1val3", "g1val4"} {
		if got[unwanted] {
			t.Errorf("%s produced an event: a nameless edit and a failed call are not registrations", unwanted)
		}
	}
}

// The threshold is the whole editorial content of this kind, so it is asserted
// on both sides rather than only on the side that fires.
func TestOnlyTransfersOverTheThresholdAreEvents(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	const ugnotPerGNOT = 1_000_000
	over := fmt.Sprintf("%dugnot", 150_000*ugnotPerGNOT)
	under := fmt.Sprintf("%dugnot", 99_999*ugnotPerGNOT)

	mustBankSend(t, db, "alpha", "TXB1", 100, recent(3), "g1rich", "g1other", over)
	mustBankSend(t, db, "alpha", "TXB2", 110, recent(2), "g1small", "g1other", under)

	events := buildAndRead(t, db, "alpha")
	var large []DiscoverEvent
	for _, e := range events {
		if e.Kind == "transfer.large" {
			large = append(large, e)
		}
	}
	if len(large) != 1 {
		t.Fatalf("%d transfer.large events, want exactly the one over the threshold", len(large))
	}
	if large[0].Actor != "g1rich" {
		t.Errorf("actor = %q, want the sender", large[0].Actor)
	}
	// The sentence says GNOT, and the column stores ugnot. Getting that
	// conversion wrong renders a plausible number that is a million times off,
	// which is exactly the kind of wrong a reader cannot catch.
	if gnot := factsOf(t, large[0])["gnot"]; fmt.Sprint(gnot) != "150000" {
		t.Errorf("gnot fact = %v, want 150000: the column is ugnot and the sentence is GNOT", gnot)
	}
}

// A coin string the syncer could not parse must not become a headline. It
// stores NULL rather than 0, and a NULL has to fall out of the predicate rather
// than be treated as a small amount, because "unknown" and "tiny" are different
// and only one of them is safe to assume.
func TestAnUnparseableAmountIsNotAnEvent(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	mustBankSend(t, db, "alpha", "TXB1", 100, recent(3), "g1a", "g1b", "banana")

	for _, e := range buildAndRead(t, db, "alpha") {
		if e.Kind == "transfer.large" {
			t.Fatalf("an unparseable amount produced an event: %s", e.Facts)
		}
	}
}

// The first call a package receives is a different event from its deploy, and
// `external` is what separates "somebody used it" from "its author tried it".
func TestPackageFirstCallSeparatesTheAuthorFromAStranger(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/moul/hello", "g1moul", 100, recent(5), true, 2)
	MustPackageSubmission(t, db, "alpha", "TX2", "gno.land/r/moul/solo", "g1moul", 101, recent(5), true, 2)
	// A stranger uses the first one.
	MustCall(t, db, "alpha", "TXC1", 120, recent(2), "g1stranger", "gno.land/r/moul/hello", "Fn")
	// The author tries the second one.
	MustCall(t, db, "alpha", "TXC2", 121, recent(2), "g1moul", "gno.land/r/moul/solo", "Fn")
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}

	byTarget := map[string]DiscoverEvent{}
	for _, e := range buildAndRead(t, db, "alpha") {
		if e.Kind == "package.first_call" {
			byTarget[e.Target] = e
		}
	}
	hello, ok := byTarget["gno.land/r/moul/hello"]
	if !ok {
		t.Fatal("a stranger's first call produced no event")
	}
	if ext := factsOf(t, hello)["external"]; ext != true {
		t.Errorf("external = %v for a call by somebody other than the creator, want true", ext)
	}
	solo, ok := byTarget["gno.land/r/moul/solo"]
	if !ok {
		t.Fatal("the author's own first call produced no event: it is still a first call")
	}
	if ext := factsOf(t, solo)["external"]; ext != false {
		t.Errorf("external = %v for the creator's own call, want false", ext)
	}
}

// The floor, which is the part of the spike rule that stops it being noise.
//
// A package going from one call to three is a tripling, and reporting it would
// bury every real surge under arithmetic about quiet packages. The floor says
// the day has to be substantial before a ratio is considered at all.
func TestAPackageSpikeNeedsVolumeAndNotJustARatio(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/busy/app", "g1busy", 100, recent(30), true, 1)
	MustPackageSubmission(t, db, "alpha", "TX2", "gno.land/r/quiet/app", "g1quiet", 101, recent(30), true, 1)

	var rows []CallerEdgeRow
	day := func(n int) string { return recent(n).Format("2006-01-02") }
	// A fortnight of ordinary days for both, then one loud day each. The busy
	// package clears the floor on its loud day; the quiet one triples and does
	// not come close.
	for i := 20; i >= 4; i-- {
		for c := 0; c < 4; c++ {
			rows = append(rows, CallerEdgeRow{
				Caller: fmt.Sprintf("g1c%d", c), PkgPath: "gno.land/r/busy/app",
				Day: day(i), Calls: 3, LastHeight: 1000 + i,
			})
		}
		rows = append(rows, CallerEdgeRow{
			Caller: "g1q", PkgPath: "gno.land/r/quiet/app",
			Day: day(i), Calls: 1, LastHeight: 1000 + i,
		})
	}
	for c := 0; c < 20; c++ {
		rows = append(rows, CallerEdgeRow{
			Caller: fmt.Sprintf("g1s%d", c), PkgPath: "gno.land/r/busy/app",
			Day: day(3), Calls: 15, LastHeight: 2000,
		})
	}
	rows = append(rows, CallerEdgeRow{
		Caller: "g1q", PkgPath: "gno.land/r/quiet/app",
		Day: day(3), Calls: 3, LastHeight: 2000,
	})
	if err := db.UpsertCallerEdges("alpha", rows); err != nil {
		t.Fatal(err)
	}

	spikes := map[string]DiscoverEvent{}
	for _, e := range buildAndRead(t, db, "alpha") {
		if e.Kind == "package.spike" {
			spikes[e.Target] = e
		}
	}
	if _, ok := spikes["gno.land/r/busy/app"]; !ok {
		t.Error("300 calls from 20 callers against a normal day of 12 did not fire")
	}
	if _, ok := spikes["gno.land/r/quiet/app"]; ok {
		t.Error("1 call to 3 fired: the floor exists to stop exactly this")
	}
}

// Reach is unique callers and never the call count. A thousand calls from one
// address is a bot; twelve calls from twelve people is news, and the ranking
// has to be able to tell them apart.
func TestAPackageSpikeRanksOnCallersNotCalls(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/bot/app", "g1bot", 100, recent(30), true, 1)

	var rows []CallerEdgeRow
	day := func(n int) string { return recent(n).Format("2006-01-02") }
	for i := 20; i >= 4; i-- {
		rows = append(rows, CallerEdgeRow{
			Caller: "g1one", PkgPath: "gno.land/r/bot/app",
			Day: day(i), Calls: 10, LastHeight: 1000 + i,
		})
	}
	rows = append(rows, CallerEdgeRow{
		Caller: "g1one", PkgPath: "gno.land/r/bot/app",
		Day: day(3), Calls: 400, LastHeight: 2000,
	})
	if err := db.UpsertCallerEdges("alpha", rows); err != nil {
		t.Fatal(err)
	}

	for _, e := range buildAndRead(t, db, "alpha") {
		if e.Kind != "package.spike" {
			continue
		}
		if e.Reach != 1 {
			t.Errorf("reach = %d for 400 calls from one address, want 1", e.Reach)
		}
		if f := factsOf(t, e); fmt.Sprint(f["callers"]) != "1" {
			t.Errorf("callers fact = %v, want 1", f["callers"])
		}
	}
}
