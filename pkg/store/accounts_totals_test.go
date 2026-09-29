package store

import "testing"

// TestAddressTransactionsCountsMessagesAndTxsApart pins the two numbers apart.
//
// Every branch of AddressTransactions' union reads a per-message table, so a
// multicall arrives as N rows sharing one hash. The list is right to hold all
// N; the header above it is not, and for years said "transactions: N". On
// g1manfred47kzduec920z88wfr64ylksmdcedlf5 the gap was 902 against 372,
// measured 2026-09-29. Both numbers now come back, and this is what keeps them
// from collapsing into one again.
func TestAddressTransactionsCountsMessagesAndTxsApart(t *testing.T) {
	db := NewTestDB(t)

	const addr = "g1manfred"

	// One transaction carrying three messages of two kinds — a deploy batch
	// with a call riding along, which is the ordinary shape of a multicall.
	if err := db.InsertPackageSubmission("gnoland1", "TXMULTI", 0, "gno.land/r/moul/a", "a", addr, 100, "2026-01-01T00:00:00Z", true, 1, "", true); err != nil {
		t.Fatalf("insert submission 0: %v", err)
	}
	if err := db.InsertPackageSubmission("gnoland1", "TXMULTI", 1, "gno.land/r/moul/b", "b", addr, 100, "2026-01-01T00:00:00Z", true, 1, "", true); err != nil {
		t.Fatalf("insert submission 1: %v", err)
	}
	if err := db.InsertCall("gnoland1", "TXMULTI", 100, 2, "2026-01-01T00:00:00Z", addr, "gno.land/r/moul/a", "Init", "", "", true); err != nil {
		t.Fatalf("insert call: %v", err)
	}
	// And one ordinary single-message transaction, so the two counts differ by
	// the multicall alone rather than by everything on the page.
	if err := db.InsertCall("gnoland1", "TXSOLO", 101, 0, "2026-01-02T00:00:00Z", addr, "gno.land/r/moul/a", "Ping", "", "", true); err != nil {
		t.Fatalf("insert solo call: %v", err)
	}

	rows, totals, err := db.AddressTransactions("gnoland1", addr, 50, 0)
	if err != nil {
		t.Fatalf("AddressTransactions: %v", err)
	}
	if len(rows) != 4 {
		t.Errorf("rows = %d, want 4: the list is per message and must stay so", len(rows))
	}
	if totals.Messages != 4 {
		t.Errorf("totals.Messages = %d, want 4", totals.Messages)
	}
	if totals.Txs != 2 {
		t.Errorf("totals.Txs = %d, want 2: TXMULTI is one transaction, not three", totals.Txs)
	}
}
