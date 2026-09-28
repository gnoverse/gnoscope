package store

import (
	"fmt"
)

// Closing the gap between a reconstructed balance and the chain's own figure.
//
// ADR 0034 established that summing TransferEvent legs reproduces a realm's
// balance exactly and a signer's only approximately, and left the shortfall as
// a disclaimer: "gas and storage deposits leave no event, so a spending account
// reads short". That is true and it is not the whole story, because two of the
// three missing terms are sitting in this database already.
//
//	live = derived + genesis - gas - storage_deposit
//
//	derived           SUM over coin_transfers. Every bank transfer emits one.
//	gas               transactions.gas_fee, for every transaction this address
//	                  paid for. SendCoinsUnrestricted, so no event.
//	storage_deposit   storage_events.fee, signed, over the same transactions.
//	                  Also SendCoinsUnrestricted, also no event.
//	genesis           an allocation written into the bank at InitChain. No
//	                  transaction, no event, and not derivable from anything
//	                  here: the chain exposes only the *vesting* portion of it
//	                  and the genesis file is unreachable through the public RPC.
//
// So three of the four terms are exact, and the fourth is the residual. For an
// account created after genesis the residual is zero, which makes it a
// self-check rather than a caveat; for a genesis account it is that account's
// allocation, and naming it as such is more use than calling it a discrepancy.
//
// ⚠️ **The fee payer is the master, always.** tm2's ante handler deducts
// tx.Fee.GasFee from signerAccs[0] (tm2/pkg/sdk/auth/ante.go:207), and a
// session-signed transaction records the master as the caller of its messages.
// So the four branches below, which match on caller/creator/from_address, are
// exactly the set of transactions this address paid for. Adding the session
// branches that AddressTransactions uses would charge a delegated key for fees
// it has never paid, and credit the master with none.
//
// ⚠️ **bank_sends matches from_address only.** Being paid is activity, which is
// why the address page's own query matches both directions, and it is not
// signing. Matching to_address here would charge the recipient of every
// transfer for the sender's gas.

// UnemittedSpend is what left an account without emitting a transfer event.
type UnemittedSpend struct {
	// GasUgnot is the total declared fee, which is what the chain deducts:
	// DeductFees takes tx.Fee.GasFee verbatim, not gas_used times a price.
	GasUgnot int64 `json:"gas_ugnot"`
	// StorageDepositUgnot is signed and therefore net: a deposit is positive,
	// an unlock's refund negative, as the syncer stores them. A realm that
	// freed more bytes than it wrote leaves this negative, which is a credit
	// and has to stay one.
	StorageDepositUgnot int64 `json:"storage_deposit_ugnot"`
	// Transactions is how many distinct transactions the two figures cover,
	// so a reader can tell "nothing spent" from "nothing indexed".
	Transactions int `json:"transactions"`
}

// UnemittedSpendFor totals the gas and storage deposits this address paid.
//
// Zero for a realm's banker, correctly and without a special case: a realm
// never signs, so no row in any of the four tables names it as caller, and the
// storage deposit for a realm's bytes is paid by whoever called it. That is why
// the realm reconstruction was exact to begin with.
func (d *DB) UnemittedSpendFor(network, addr string) (UnemittedSpend, error) {
	var out UnemittedSpend
	if addr == "" {
		return out, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	nf := func(col string) string { return d.networkFilter(col, network) }

	// DISTINCT across the union, because one transaction can carry several
	// messages and would otherwise be charged its fee once per message. On
	// mainnet that is not a rounding error: the busiest account here has 796
	// message rows across 491 transactions, so summing per row overstates its
	// gas by 60%.
	payer := fmt.Sprintf(`
		SELECT tx_hash FROM calls              WHERE caller = ?       AND %s
		UNION SELECT tx_hash FROM package_submissions WHERE creator = ? AND %s
		UNION SELECT tx_hash FROM msg_runs     WHERE caller = ?       AND %s
		UNION SELECT tx_hash FROM bank_sends   WHERE from_address = ? AND %s`,
		nf("network"), nf("network"), nf("network"), nf("network"))

	q := fmt.Sprintf(`
		WITH paid AS (%s)
		SELECT
		  (SELECT COALESCE(SUM(t.gas_fee), 0) FROM transactions t
		     JOIN paid p ON p.tx_hash = t.tx_hash WHERE %s),
		  (SELECT COALESCE(SUM(s.fee), 0) FROM storage_events s
		     JOIN paid p ON p.tx_hash = s.tx_hash WHERE %s),
		  (SELECT COUNT(*) FROM paid)`,
		payer, nf("t.network"), nf("s.network"))

	args := []any{addr, addr, addr, addr}
	err := d.db.QueryRow(q, args...).Scan(&out.GasUgnot, &out.StorageDepositUgnot, &out.Transactions)
	if err != nil {
		return UnemittedSpend{}, err
	}
	return out, nil
}
