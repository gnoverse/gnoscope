package store

import (
	"testing"

	"github.com/gnoverse/gnoscope/pkg/config"
)

// TestStatsCountsTransactionsNotMessages pins the headline to transactions.
//
// The home page tile said "transactions" and summed four per-message tables,
// so a transaction carrying a call and two sends counted three times. On
// mainnet that read 173,250 against the chain's 151,458 (2026-10-15), a 14%
// overstatement that onbloc's explorer did not share, which is how it was
// found: Gnoscan counts the transaction, gnoscope counted the message.
//
// The transactions table is keyed (network, tx_hash), so it is already the
// right answer; the sum stays available as TotalMessages, which is what the
// per-kind tiles beside it add up to.
func TestStatsCountsTransactionsNotMessages(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: "alpha"}})

	const (
		network = "alpha"
		hash    = "TXMULTI"
		height  = 42
	)

	// One transaction, three messages inside it.
	if err := db.UpsertTransaction(network, hash, height, "", 100, 200, 1, true); err != nil {
		t.Fatalf("upsert transaction: %v", err)
	}
	if err := db.InsertCall(network, hash, height, 0, "", "g1caller", "gno.land/r/demo/foo", "Bar", "", "", true); err != nil {
		t.Fatalf("insert call: %v", err)
	}
	if err := db.InsertBankSend(network, hash, height, "", "g1from", "g1to", "1ugnot", true); err != nil {
		t.Fatalf("insert bank send: %v", err)
	}
	if err := db.InsertBankSend(network, hash, height, "", "g1from", "g1other", "2ugnot", true); err != nil {
		t.Fatalf("insert second bank send: %v", err)
	}

	s, err := db.GetStats("")
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if s.TotalTxs != 1 {
		t.Errorf("total txs = %d, want 1: three messages travelled in one transaction", s.TotalTxs)
	}
	if s.TotalMessages != 3 {
		t.Errorf("total messages = %d, want 3: the per-kind tiles still count messages", s.TotalMessages)
	}
	if s.TotalCalls != 1 || s.TotalSends != 2 {
		t.Errorf("calls/sends = %d/%d, want 1/2", s.TotalCalls, s.TotalSends)
	}
}

// A failed transaction is still a transaction: the chain charged for it and
// both explorers count it. Nothing filters on success, and this is what says so.
func TestStatsCountsFailedTransactions(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: "alpha"}})

	if err := db.UpsertTransaction("alpha", "TXOK", 1, "", 100, 200, 1, true); err != nil {
		t.Fatalf("upsert ok tx: %v", err)
	}
	if err := db.UpsertTransaction("alpha", "TXBAD", 2, "", 100, 200, 1, false); err != nil {
		t.Fatalf("upsert failed tx: %v", err)
	}

	s, err := db.GetStats("")
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if s.TotalTxs != 2 {
		t.Errorf("total txs = %d, want 2: a reverted transaction still happened", s.TotalTxs)
	}
}
