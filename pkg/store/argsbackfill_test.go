package store

import "testing"

// TestArgsBackfillHeightsIgnoresTheBlocksWindow is the regression test for the
// bug that shipped in #422.
//
// The first version of ArgsBackfill took its bounds from the `blocks` table,
// which -block-history-days keeps to a rolling window, while calls and
// package_submissions go back to genesis. On the live instance that stopped the
// walk after exactly one batch and left every older row empty forever.
//
// So: a call far below anything in `blocks` must still be offered.
func TestArgsBackfillHeightsIgnoresTheBlocksWindow(t *testing.T) {
	db := NewTestDB(t)

	// A blocks table that only knows the recent tip, the way a 90-day window
	// leaves it on a chain that is years old.
	for h := 9000; h <= 9002; h++ {
		if err := db.UpsertBlock("mainnet", h, "", 0, 0); err != nil {
			t.Fatalf("upsert block %d: %v", h, err)
		}
	}
	// And a call from long before the window starts.
	if err := db.InsertCall("mainnet", "TXOLD", 12, 0, "", "g1a", "gno.land/r/x/y", "F", "", "", true); err != nil {
		t.Fatalf("insert old call: %v", err)
	}
	if err := db.InsertCall("mainnet", "TXNEW", 9001, 0, "", "g1a", "gno.land/r/x/y", "G", "", "", true); err != nil {
		t.Fatalf("insert new call: %v", err)
	}

	agedRows(t, db)

	got, err := db.ArgsBackfillHeights("mainnet", 50)
	if err != nil {
		t.Fatalf("ArgsBackfillHeights: %v", err)
	}
	// Newest first, and the ancient one is present: that is the whole fix.
	want := []int{9001, 12}
	if len(got) != len(want) {
		t.Fatalf("heights = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("heights = %v, want %v (newest first, blocks window ignored)", got, want)
		}
	}
}

// TestArgsBackfillHeightsTerminates pins the other half: the cursor, not the
// emptiness of args, is what ends the walk.
//
// A call that genuinely took no arguments and sent nothing keeps an empty args
// forever, so a walk that stopped when nothing matched would hand back the same
// heights every pass and never finish.
func TestArgsBackfillHeightsTerminates(t *testing.T) {
	db := NewTestDB(t)

	for _, h := range []int{10, 20, 30} {
		if err := db.InsertCall("mainnet", "TX"+itoa(h), h, 0, "", "g1a", "gno.land/r/x/y", "F", "", "", true); err != nil {
			t.Fatalf("insert call at %d: %v", h, err)
		}
	}

	agedRows(t, db)

	// One pass at a time, the way the syncer walks it, writing the cursor after
	// each. Nothing ever fills args, so only the cursor can make this stop.
	seen := []int{}
	for range 10 {
		hs, err := db.ArgsBackfillHeights("mainnet", 1)
		if err != nil {
			t.Fatalf("ArgsBackfillHeights: %v", err)
		}
		if len(hs) == 0 {
			break
		}
		seen = append(seen, hs[0])
		if err := db.SetArgsBackfillCursor("mainnet", hs[0]); err != nil {
			t.Fatalf("SetArgsBackfillCursor: %v", err)
		}
	}
	want := []int{30, 20, 10}
	if len(seen) != len(want) {
		t.Fatalf("walk visited %v, want %v exactly once each", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("walk visited %v, want %v newest-first", seen, want)
		}
	}
}

// agedRows backdates every call to args_pass 0, which is what a row written
// before these columns existed holds.
//
// Needed because InsertCall stamps the *current* pass, so a freshly inserted
// row is by definition already done and the walk is right to skip it. Without
// this the fixture would be asserting on rows that need no work.
func agedRows(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.db.Exec(`UPDATE calls SET args_pass = 0`); err != nil {
		t.Fatalf("age rows: %v", err)
	}
}

// TestArgsBackfillHeightsOffersRowsFromAnOlderPass is what makes changing the
// truncation rules safe.
//
// A row filled by pass 1 is *not* empty, so the emptiness test the first
// version used would never offer it again, and every address v1 destroyed would
// stay destroyed. Only args_pass can tell a correct row from a stale one.
func TestArgsBackfillHeightsOffersRowsFromAnOlderPass(t *testing.T) {
	db := NewTestDB(t)

	// A row that already has arguments, written by an older pass.
	if err := db.InsertCall("mainnet", "TXSTALE", 500, 0, "", "g1a", "gno.land/r/x/y", "F",
		"g1vc883gshu5z7ytk5cdynhc8c2dh…", "", true); err != nil {
		t.Fatalf("insert stale call: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE calls SET args_pass = ?`, argsPass-1); err != nil {
		t.Fatalf("age row: %v", err)
	}

	got, err := db.ArgsBackfillHeights("mainnet", 50)
	if err != nil {
		t.Fatalf("ArgsBackfillHeights: %v", err)
	}
	if len(got) != 1 || got[0] != 500 {
		t.Fatalf("heights = %v, want [500]: a row from an older pass needs rewriting", got)
	}

	// And once it is rewritten at the current pass, it stops being offered.
	if err := db.UpdateCallArgsAndSend("mainnet", "TXSTALE", 0,
		"g1vc883gshu5z7ytk5cdynhc8c2dhqnfhsmnhpaz", ""); err != nil {
		t.Fatalf("UpdateCallArgsAndSend: %v", err)
	}
	if got, err = db.ArgsBackfillHeights("mainnet", 50); err != nil {
		t.Fatalf("ArgsBackfillHeights: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("heights = %v, want none: the row is current now", got)
	}
}
