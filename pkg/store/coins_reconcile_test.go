package store

import "testing"

// The reconciliation is only worth shipping if the three computable terms
// actually close on the fourth, so these pin the two ways the sum can be wrong:
// charging an address for a transaction it did not pay for, and charging it
// more than once for a transaction it did.

const (
	payer   = "g1payer00000000000000000000000000000000"
	other   = "g1other00000000000000000000000000000000"
	realmAd = "g1realm00000000000000000000000000000000"
)

// seedPaidTx writes one transaction with a gas fee and, optionally, a storage
// event, attributed to caller through a call row.
func seedPaidTx(t *testing.T, db *DB, hash, caller string, gasFee int, storageFee int) {
	t.Helper()
	const when = "2026-01-01T00:00:00Z"
	if err := db.InsertCall("alpha", hash, 100, 0, when, caller, "gno.land/r/x/y", "F", true); err != nil {
		t.Fatalf("InsertCall: %v", err)
	}
	if err := db.UpsertTransaction("alpha", hash, 100, when, 0, 0, gasFee, true); err != nil {
		t.Fatalf("UpsertTransaction: %v", err)
	}
	if storageFee != 0 {
		if err := db.InsertStorageEvent("alpha", hash, 0, "gno.land/r/x/y", 100, when,
			"deposit", 100, storageFee); err != nil {
			t.Fatalf("InsertStorageEvent: %v", err)
		}
	}
}

func TestUnemittedSpendFor(t *testing.T) {
	db := NewTestDB(t)

	seedPaidTx(t, db, "TX1", payer, 1000, 500)
	seedPaidTx(t, db, "TX2", payer, 2000, 0)
	// Somebody else's transaction. The payer must not be charged for it.
	seedPaidTx(t, db, "TX3", other, 9999, 9999)

	tests := []struct {
		name        string
		addr        string
		wantGas     int64
		wantDeposit int64
		wantTxs     int
	}{
		{
			name:        "the payer is charged for its own transactions only",
			addr:        payer,
			wantGas:     3000,
			wantDeposit: 500,
			wantTxs:     2,
		},
		{
			name:        "another signer's spend is its own",
			addr:        other,
			wantGas:     9999,
			wantDeposit: 9999,
			wantTxs:     1,
		},
		{
			// A realm never signs, so nothing names it as caller. This is why
			// the realm reconstruction was exact before any of this existed,
			// and it must stay zero rather than acquire a special case.
			name: "a realm's banker pays nothing",
			addr: realmAd,
		},
		{
			name: "the empty address is not a lookup",
			addr: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := db.UnemittedSpendFor("alpha", tt.addr)
			if err != nil {
				t.Fatalf("UnemittedSpendFor: %v", err)
			}
			if got.GasUgnot != tt.wantGas {
				t.Errorf("gas = %d, want %d", got.GasUgnot, tt.wantGas)
			}
			if got.StorageDepositUgnot != tt.wantDeposit {
				t.Errorf("storage deposit = %d, want %d", got.StorageDepositUgnot, tt.wantDeposit)
			}
			if got.Transactions != tt.wantTxs {
				t.Errorf("transactions = %d, want %d", got.Transactions, tt.wantTxs)
			}
		})
	}
}

// One transaction, several messages, one fee. Summing per message overstates
// gas by the message count, and on mainnet that is a 60% error on the busiest
// account here (796 message rows across 491 transactions), not a rounding one.
func TestUnemittedSpendCountsATransactionOnce(t *testing.T) {
	db := NewTestDB(t)
	const when = "2026-01-01T00:00:00Z"

	if err := db.UpsertTransaction("alpha", "MULTI", 100, when, 0, 0, 7000, true); err != nil {
		t.Fatalf("UpsertTransaction: %v", err)
	}
	// Four messages of three kinds, all in the one transaction, all naming the
	// same payer: every branch of the union has to collapse onto one tx_hash.
	if err := db.InsertCall("alpha", "MULTI", 100, 0, when, payer, "gno.land/r/x/y", "A", true); err != nil {
		t.Fatalf("InsertCall: %v", err)
	}
	if err := db.InsertCall("alpha", "MULTI", 100, 1, when, payer, "gno.land/r/x/z", "B", true); err != nil {
		t.Fatalf("InsertCall: %v", err)
	}
	if err := db.InsertBankSend("alpha", "MULTI", 100, when, payer, other, "5ugnot", true); err != nil {
		t.Fatalf("InsertBankSend: %v", err)
	}
	if err := db.InsertPackageSubmission("alpha", "MULTI", 0, "gno.land/r/x/new", "new",
		payer, 100, when, true, 1, true); err != nil {
		t.Fatalf("InsertPackageSubmission: %v", err)
	}

	got, err := db.UnemittedSpendFor("alpha", payer)
	if err != nil {
		t.Fatalf("UnemittedSpendFor: %v", err)
	}
	if got.GasUgnot != 7000 {
		t.Errorf("gas = %d, want 7000 (the fee counted once, not once per message)", got.GasUgnot)
	}
	if got.Transactions != 1 {
		t.Errorf("transactions = %d, want 1", got.Transactions)
	}
}

// Being paid is not signing. bank_sends is the one table matched in a single
// direction, and matching both would charge every recipient for the sender's
// gas.
func TestUnemittedSpendDoesNotChargeTheRecipient(t *testing.T) {
	db := NewTestDB(t)
	const when = "2026-01-01T00:00:00Z"

	if err := db.UpsertTransaction("alpha", "PAY", 100, when, 0, 0, 4242, true); err != nil {
		t.Fatalf("UpsertTransaction: %v", err)
	}
	if err := db.InsertBankSend("alpha", "PAY", 100, when, payer, other, "100ugnot", true); err != nil {
		t.Fatalf("InsertBankSend: %v", err)
	}

	recipient, err := db.UnemittedSpendFor("alpha", other)
	if err != nil {
		t.Fatalf("UnemittedSpendFor: %v", err)
	}
	if recipient.GasUgnot != 0 {
		t.Errorf("the recipient was charged %d in gas for a transfer it received", recipient.GasUgnot)
	}
	sender, err := db.UnemittedSpendFor("alpha", payer)
	if err != nil {
		t.Fatalf("UnemittedSpendFor: %v", err)
	}
	if sender.GasUgnot != 4242 {
		t.Errorf("sender gas = %d, want 4242", sender.GasUgnot)
	}
}

// A storage unlock refunds, and the syncer stores that as a negative fee. The
// sum has to stay signed: treating a refund as a cost would make the
// reconciliation overshoot by twice the refund.
func TestUnemittedSpendKeepsRefundsNegative(t *testing.T) {
	db := NewTestDB(t)
	const when = "2026-01-01T00:00:00Z"

	if err := db.UpsertTransaction("alpha", "FREE", 100, when, 0, 0, 10, true); err != nil {
		t.Fatalf("UpsertTransaction: %v", err)
	}
	if err := db.InsertCall("alpha", "FREE", 100, 0, when, payer, "gno.land/r/x/y", "Del", true); err != nil {
		t.Fatalf("InsertCall: %v", err)
	}
	if err := db.InsertStorageEvent("alpha", "FREE", 0, "gno.land/r/x/y", 100, when,
		"unlock", -100, -600); err != nil {
		t.Fatalf("InsertStorageEvent: %v", err)
	}

	got, err := db.UnemittedSpendFor("alpha", payer)
	if err != nil {
		t.Fatalf("UnemittedSpendFor: %v", err)
	}
	if got.StorageDepositUgnot != -600 {
		t.Errorf("storage deposit = %d, want -600 (a refund is a credit)", got.StorageDepositUgnot)
	}
}
