package store

import (
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/config"
)

func badgeStatsDB(t *testing.T) *DB {
	t.Helper()
	db := NewTestDB(t)
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: "alpha"}, {ID: "beta"}})
	return db
}

func day(n int) string {
	return time.Now().UTC().AddDate(0, 0, -n).Format(time.RFC3339Nano)
}

// The four numbers a badge prints, over one realm's whole history.
func TestRealmBadgeStatsCountsMessagesTxsAndCallers(t *testing.T) {
	db := badgeStatsDB(t)
	const path = "gno.land/r/alpha/board"
	if err := db.UpsertPackage("alpha", path, "board", "g1creator", "TXDEPLOY", 100, day(40), true, 1); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	// Two messages in one transaction, so Messages and Txs cannot both be
	// right by accident, plus a MsgRun naming the realm, which is a message
	// aimed at it with no MsgCall of its own.
	for _, c := range []struct {
		hash    string
		msgIdx  int
		caller  string
		when    string
		success bool
	}{
		{"TX1", 0, "g1alice", day(3), true},
		{"TX1", 1, "g1alice", day(3), true},
		{"TX2", 0, "g1bob", day(2), true},
		{"TX3", 0, "g1alice", day(60), true},
	} {
		if err := db.InsertCall("alpha", c.hash, 200+c.msgIdx, c.msgIdx, c.when, c.caller, path, "Post", "", "", c.success); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}
	if err := db.InsertMsgRun("alpha", "TX4", 300, day(1), "g1carol", "import \""+path+"\"", "", true); err != nil {
		t.Fatalf("InsertMsgRun: %v", err)
	}

	all, err := db.RealmBadgeStats("alpha", path, "")
	if err != nil {
		t.Fatalf("RealmBadgeStats: %v", err)
	}
	if all.Messages != 5 {
		t.Errorf("Messages = %d, want 5 (four calls and one run)", all.Messages)
	}
	if all.Txs != 4 {
		t.Errorf("Txs = %d, want 4: TX1 carried two of the five messages", all.Txs)
	}
	if all.UniqueCallers != 3 {
		t.Errorf("UniqueCallers = %d, want 3 (alice, bob, carol)", all.UniqueCallers)
	}

	// A window drops the 60-day-old call and nothing else.
	win, err := db.RealmBadgeStats("alpha", path, time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("RealmBadgeStats(since): %v", err)
	}
	if win.Messages != 4 {
		t.Errorf("Messages over 30 days = %d, want 4", win.Messages)
	}
	if win.Txs != 3 {
		t.Errorf("Txs over 30 days = %d, want 3", win.Txs)
	}
}

// Deploys is the version number, and the version number is the count of
// submissions that were accepted — not of the rows in packages, which keeps
// only the latest, and not of every attempt.
func TestRealmBadgeStatsCountsAcceptedSubmissionsOnly(t *testing.T) {
	db := badgeStatsDB(t)
	const path = "gno.land/r/alpha/board"
	if err := db.UpsertPackage("alpha", path, "board", "g1creator", "TXC", 300, day(1), true, 1); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	subs := []struct {
		hash    string
		height  int
		when    string
		success bool
	}{
		{"S1", 100, day(30), true},
		{"S2", 200, day(20), false}, // reverted: released nothing
		{"S3", 300, day(10), true},
	}
	for _, s := range subs {
		if err := db.InsertPackageSubmission("alpha", s.hash, 0, path, "board", "g1creator", s.height, s.when, true, 1, "", s.success); err != nil {
			t.Fatalf("InsertPackageSubmission(%s): %v", s.hash, err)
		}
	}

	got, err := db.RealmBadgeStats("alpha", path, "")
	if err != nil {
		t.Fatalf("RealmBadgeStats: %v", err)
	}
	if got.Deploys != 2 {
		t.Errorf("Deploys = %d, want 2: the failed submission is not a release", got.Deploys)
	}
	if got.LastDeployTime != subs[2].when {
		t.Errorf("LastDeployTime = %q, want the highest accepted submission's %q", got.LastDeployTime, subs[2].when)
	}
}

// Everything is network-scoped (AGENTS.md), and a badge is the worst place to
// break that: two chains' traffic summed into one number is a figure nobody
// can check against anything.
func TestRealmBadgeStatsDoesNotMixNetworks(t *testing.T) {
	db := badgeStatsDB(t)
	const path = "gno.land/r/alpha/board"
	for _, net := range []string{"alpha", "beta"} {
		if err := db.UpsertPackage(net, path, "board", "g1creator", "TX-"+net, 100, day(5), true, 1); err != nil {
			t.Fatalf("UpsertPackage(%s): %v", net, err)
		}
	}
	if err := db.InsertCall("alpha", "A1", 200, 0, day(1), "g1alice", path, "Post", "", "", true); err != nil {
		t.Fatalf("InsertCall: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := db.InsertCall("beta", "B1", 200+i, i, day(1), "g1bob", path, "Post", "", "", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}

	got, err := db.RealmBadgeStats("alpha", path, "")
	if err != nil {
		t.Fatalf("RealmBadgeStats: %v", err)
	}
	if got.Messages != 1 {
		t.Errorf("Messages = %d, want 1: beta's five calls are another chain's", got.Messages)
	}
	if got.Network != "alpha" {
		t.Errorf("Network = %q, want alpha", got.Network)
	}
}

// A path the index has never seen is an error, not a zero. "0 txs" under a
// realm's name is a number a reader believes.
func TestRealmBadgeStatsRejectsAnUnknownPath(t *testing.T) {
	db := badgeStatsDB(t)
	if _, err := db.RealmBadgeStats("alpha", "gno.land/r/alpha/nope", ""); err == nil {
		t.Fatal("an unknown path returned stats instead of an error")
	}
}
