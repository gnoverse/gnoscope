package store

import (
	"fmt"
	"testing"
	"time"
)

func seedSeriesRealm(t *testing.T, db *DB, network, path string) {
	t.Helper()
	when := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.UpsertPackage(network, path, "pkg", "g1creator", "TXD", 100, when, true, 1); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
}

// The invariant a sparkline depends on. A sparse series lets the renderer join
// the last busy day straight to the next one, so a realm that went quiet for a
// fortnight is drawn as one that declined gently over it: a wrong answer with
// no visible symptom.
func TestRealmActivitySeriesIsDense(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks(nil)
	const path = "gno.land/r/alpha/board"
	seedSeriesRealm(t, db, "alpha", path)

	// Two calls today, none for a week, one eight days ago.
	for i, ago := range []int{0, 0, 8} {
		ts := time.Now().UTC().AddDate(0, 0, -ago).Format(time.RFC3339Nano)
		if err := db.InsertCall("alpha", fmt.Sprintf("TX%d", i), 100+i, 0, ts,
			fmt.Sprintf("g1c%d", i), path, "Post", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}

	pts, err := db.RealmActivitySeries("alpha", path, "daily", 14)
	if err != nil {
		t.Fatalf("RealmActivitySeries: %v", err)
	}
	if len(pts) != 15 {
		t.Fatalf("got %d buckets for a 14-day window, want 15 (both ends inclusive)", len(pts))
	}

	var zeros, total int
	for _, p := range pts {
		total += p.Messages
		if p.Messages == 0 {
			zeros++
		}
	}
	if total != 3 {
		t.Errorf("total messages = %d, want 3", total)
	}
	if zeros != 13 {
		t.Errorf("%d zero buckets, want 13: the quiet days are missing from the series", zeros)
	}
	if got := pts[len(pts)-1].Messages; got != 2 {
		t.Errorf("today has %d messages, want 2", got)
	}
}

// Every table here is network-scoped, and a badge that blended two chains into
// one line would be indistinguishable from a busy realm.
func TestRealmActivitySeriesIsNetworkScoped(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks(nil)
	const path = "gno.land/r/shared/board"
	seedSeriesRealm(t, db, "alpha", path)
	seedSeriesRealm(t, db, "beta", path)

	when := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < 5; i++ {
		if err := db.InsertCall("alpha", fmt.Sprintf("A%d", i), 100+i, 0, when, "g1a", path, "Post", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := db.InsertCall("beta", fmt.Sprintf("B%d", i), 200+i, 0, when, "g1b", path, "Post", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}

	// "" is what ?network=all resolves to. The package lookup picks the one
	// chain that holds this path and the series is scoped to it; without that
	// step the filter would be empty and the two chains' calls would land on
	// one line. beta is seeded second, so an implementation that resolved
	// nothing would answer 0 rather than 5 or 7, and all three are distinguishable.
	for _, tt := range []struct {
		network string
		want    int
	}{{"alpha", 5}, {"beta", 2}, {"", 5}} {
		pts, err := db.RealmActivitySeries(tt.network, path, "daily", 7)
		if err != nil {
			t.Fatalf("%s: %v", tt.network, err)
		}
		var total int
		for _, p := range pts {
			total += p.Messages
		}
		if total != tt.want {
			t.Errorf("%s: %d messages, want %d", tt.network, total, tt.want)
		}
	}
}

// Callers is distinct-per-bucket and does not sum across buckets. Stated as a
// test because the tempting reading (add them up for "unique callers") is
// wrong by exactly the number of people who came back.
func TestRealmActivitySeriesCountsCallersPerBucket(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks(nil)
	const path = "gno.land/r/alpha/regulars"
	seedSeriesRealm(t, db, "alpha", path)

	// One address calling on three consecutive days.
	for i := 0; i < 3; i++ {
		ts := time.Now().UTC().AddDate(0, 0, -i).Format(time.RFC3339Nano)
		if err := db.InsertCall("alpha", fmt.Sprintf("R%d", i), 100+i, 0, ts, "g1regular", path, "Post", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}

	pts, err := db.RealmActivitySeries("alpha", path, "daily", 7)
	if err != nil {
		t.Fatalf("RealmActivitySeries: %v", err)
	}
	var summed, peak int
	for _, p := range pts {
		summed += p.Callers
		if p.Callers > peak {
			peak = p.Callers
		}
	}
	if peak != 1 {
		t.Errorf("peak callers = %d, want 1: one address called every day", peak)
	}
	if summed != 3 {
		t.Errorf("summed callers = %d, want 3, and that 3 is why summing is not the unique-caller count (1)", summed)
	}
}

// MsgRuns count too: RealmUsage's totals include them, and a graph that did not
// would not add up to the number printed beside it.
func TestRealmActivitySeriesIncludesMsgRuns(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks(nil)
	const path = "gno.land/r/alpha/lib"
	seedSeriesRealm(t, db, "alpha", path)

	when := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.InsertCall("alpha", "C1", 100, 0, when, "g1a", path, "Do", true); err != nil {
		t.Fatalf("InsertCall: %v", err)
	}
	if err := db.InsertMsgRun("alpha", "R1", 101, when, "g1b",
		`package main; import "`+path+`"; func main() {}`, true); err != nil {
		t.Fatalf("InsertMsgRun: %v", err)
	}

	pts, err := db.RealmActivitySeries("alpha", path, "daily", 3)
	if err != nil {
		t.Fatalf("RealmActivitySeries: %v", err)
	}
	var total int
	for _, p := range pts {
		total += p.Messages
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (one call and one MsgRun)", total)
	}
}

// An unknown path is an error, not an empty graph: a badge drawing a flat zero
// for a typo says the realm is dead rather than absent.
func TestRealmActivitySeriesRejectsAnUnknownPackage(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks(nil)
	if _, err := db.RealmActivitySeries("alpha", "gno.land/r/alpha/nope", "daily", 7); err == nil {
		t.Fatal("no error for a package that does not exist")
	}
}
