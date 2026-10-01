package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// One account's money, every asset at once, one row per transaction.
//
// The holdings tab answered "what moved" twice, in two tables that could not
// be read together: native legs in one, GRC20 legs in the other, both paged by
// leg. A swap is one native or token leg out and another in, so it appeared as
// two unrelated rows in two places, and the question a trader's page exists to
// answer ("what did they swap, for what") needed a reader to join them by eye
// on a transaction hash.
//
// This is that join, done once in SQL. Every asset is a leg in one view, legs
// are summed per transaction and asset, and the page is cut by transaction.
//
// The native half is two sources, not one, and the reason is measured: on
// mainnet the coin ledger (TransferEvent) is missing the early BankMsgSends,
// which predate the event. A bank send is counted only when the coin ledger has
// no leg for the same transaction, sender and recipient, so a send that did
// emit the event is not counted twice.

// DefiLeg is one asset's net movement inside one transaction, from the
// account's point of view: positive is received.
type DefiLeg struct {
	Token   string `json:"token"`
	PkgPath string `json:"pkg_path,omitempty"`
	Delta   int64  `json:"delta"`
	// Items is the signed count of legs. It is what a GRC721 moves, because
	// its Transfer carries no amount and Delta is always zero for one.
	Items int `json:"items"`
}

// DefiCall is one MsgCall the account made inside the transaction, which is
// what turns a row of deltas into an action a reader recognises.
type DefiCall struct {
	PkgPath string `json:"pkg_path"`
	Func    string `json:"func"`
}

// DefiTx is one row of the unified history.
type DefiTx struct {
	TxHash      string     `json:"tx_hash"`
	BlockHeight int        `json:"block_height"`
	BlockTime   string     `json:"block_time,omitempty"`
	Legs        []DefiLeg  `json:"legs"`
	Calls       []DefiCall `json:"calls,omitempty"`
	// FeeUgnot is the gas fee, set only when this account paid it. It is not a
	// leg: gas leaves through SendCoinsUnrestricted and emits nothing, and
	// folding it into the native delta would turn every swap into "sent GNOT".
	FeeUgnot int64 `json:"fee_ugnot,omitempty"`
}

// defiMovesSQL is the per-leg view every query below reads. It binds the
// address as ?1 and the network as ?2.
//
// A self-transfer nets to zero rather than to +value: both CASE arms would
// otherwise see `to_addr = ?1` first and count it as received.
const defiMovesSQL = `
	SELECT tx_hash, block_height, block_time, token, pkg_path,
	       CASE WHEN from_addr = to_addr THEN 0
	            WHEN to_addr = ?1 THEN value ELSE -value END AS delta,
	       CASE WHEN from_addr = to_addr THEN 0
	            WHEN to_addr = ?1 THEN 1 ELSE -1 END AS items
	  FROM token_transfers
	 WHERE network = ?2 AND (from_addr = ?1 OR to_addr = ?1)
	UNION ALL
	SELECT tx_hash, block_height, block_time, 'ugnot', '',
	       CASE WHEN from_addr = to_addr THEN 0
	            WHEN to_addr = ?1 THEN ugnot ELSE -ugnot END,
	       0
	  FROM coin_transfers
	 WHERE network = ?2 AND (from_addr = ?1 OR to_addr = ?1) AND ugnot <> 0
	UNION ALL
	SELECT b.tx_hash, b.block_height, COALESCE(b.block_time, ''), 'ugnot', '',
	       CASE WHEN b.from_address = b.to_address THEN 0
	            WHEN b.to_address = ?1 THEN b.ugnot_amount ELSE -b.ugnot_amount END,
	       0
	  FROM bank_sends b
	 WHERE b.network = ?2 AND (b.from_address = ?1 OR b.to_address = ?1)
	   AND b.success AND COALESCE(b.ugnot_amount, 0) <> 0
	   AND NOT EXISTS (SELECT 1 FROM coin_transfers c
	                    WHERE c.network = b.network AND c.tx_hash = b.tx_hash
	                      AND c.from_addr = b.from_address AND c.to_addr = b.to_address)`

// defiPayerSQL lists the transactions this account signed, which are the ones
// whose gas it paid. Same four sources UnemittedSpendFor reads, for the same
// reason: a transaction can carry several messages and is charged once.
const defiPayerSQL = `
	SELECT tx_hash FROM calls               WHERE network = ?2 AND caller = ?1
	UNION SELECT tx_hash FROM msg_runs      WHERE network = ?2 AND caller = ?1
	UNION SELECT tx_hash FROM bank_sends    WHERE network = ?2 AND from_address = ?1
	UNION SELECT tx_hash FROM package_submissions WHERE network = ?2 AND creator = ?1`

// DefiTxCount is how many transactions moved any asset in or out of addr.
// Counted in SQL over the whole set, so a pager pages against the real total.
func (d *DB) DefiTxCount(network, addr string) (int, error) {
	if addr == "" {
		return 0, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int
	err := d.db.QueryRow(`SELECT COUNT(DISTINCT tx_hash) FROM (`+defiMovesSQL+`)`,
		addr, network).Scan(&n)
	return n, err
}

// DefiTxs returns one page of the unified history, newest first.
//
// Two passes: the page of transaction hashes, then everything about those
// hashes. Cutting the page by leg instead would split a swap across two pages
// whenever its two legs straddled the boundary.
func (d *DB) DefiTxs(network, addr string, limit, offset int) ([]DefiTx, error) {
	out := []DefiTx{}
	if addr == "" || limit <= 0 {
		return out, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT tx_hash, MAX(block_height) AS h, MAX(block_time)
		  FROM (`+defiMovesSQL+`)
		 GROUP BY tx_hash
		 ORDER BY h DESC, tx_hash DESC
		 LIMIT ?3 OFFSET ?4`, addr, network, limit, offset)
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for rows.Next() {
		var tx DefiTx
		var bt sql.NullString
		if err := rows.Scan(&tx.TxHash, &tx.BlockHeight, &bt); err != nil {
			rows.Close()
			return nil, err
		}
		tx.BlockTime = bt.String
		tx.Legs = []DefiLeg{}
		idx[tx.TxHash] = len(out)
		out = append(out, tx)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	hashes := make([]any, 0, len(out))
	for _, tx := range out {
		hashes = append(hashes, tx.TxHash)
	}
	// ?1 and ?2 stay the address and network; the page's hashes follow.
	in := placeholders(3, len(hashes))
	args := append([]any{addr, network}, hashes...)

	legRows, err := d.db.Query(fmt.Sprintf(`
		SELECT tx_hash, token, MAX(pkg_path), SUM(delta), SUM(items)
		  FROM (%s)
		 WHERE tx_hash IN (%s)
		 GROUP BY tx_hash, token`, defiMovesSQL, in), args...)
	if err != nil {
		return nil, err
	}
	for legRows.Next() {
		var h string
		var l DefiLeg
		if err := legRows.Scan(&h, &l.Token, &l.PkgPath, &l.Delta, &l.Items); err != nil {
			legRows.Close()
			return nil, err
		}
		// A transaction whose legs cancel (sent and received back the same
		// amount) still moved, and stays listed; only the zero leg is dropped.
		if l.Delta == 0 && l.Items == 0 {
			continue
		}
		if i, ok := idx[h]; ok {
			out[i].Legs = append(out[i].Legs, l)
		}
	}
	legRows.Close()
	if err := legRows.Err(); err != nil {
		return nil, err
	}

	callRows, err := d.db.Query(fmt.Sprintf(`
		SELECT tx_hash, pkg_path, func_name FROM calls
		 WHERE network = ?2 AND caller = ?1 AND tx_hash IN (%s)
		 ORDER BY tx_hash, msg_index`, in), args...)
	if err != nil {
		return nil, err
	}
	for callRows.Next() {
		var h string
		var c DefiCall
		if err := callRows.Scan(&h, &c.PkgPath, &c.Func); err != nil {
			callRows.Close()
			return nil, err
		}
		if i, ok := idx[h]; ok {
			out[i].Calls = append(out[i].Calls, c)
		}
	}
	callRows.Close()
	if err := callRows.Err(); err != nil {
		return nil, err
	}

	feeRows, err := d.db.Query(fmt.Sprintf(`
		SELECT t.tx_hash, t.gas_fee FROM transactions t
		 WHERE t.network = ?2 AND t.tx_hash IN (%s)
		   AND t.tx_hash IN (%s)`, in, defiPayerSQL), args...)
	if err != nil {
		return nil, err
	}
	for feeRows.Next() {
		var h string
		var fee int64
		if err := feeRows.Scan(&h, &fee); err != nil {
			feeRows.Close()
			return nil, err
		}
		if i, ok := idx[h]; ok {
			out[i].FeeUgnot = fee
		}
	}
	feeRows.Close()
	if err := feeRows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		legs := out[i].Legs
		// Out before in, so a swap reads left to right as "gave, got", then by
		// token so two renders of one row never swap places.
		sort.SliceStable(legs, func(a, b int) bool {
			if sa, sb := legSign(legs[a]), legSign(legs[b]); sa != sb {
				return sa < sb
			}
			return legs[a].Token < legs[b].Token
		})
	}
	return out, nil
}

func legSign(l DefiLeg) int {
	switch {
	case l.Delta < 0 || (l.Delta == 0 && l.Items < 0):
		return -1
	case l.Delta > 0 || l.Items > 0:
		return 1
	}
	return 0
}

func placeholders(from, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "?%d", from+i)
	}
	return b.String()
}

// DefiBucket is one asset's net movement inside one time bucket.
type DefiBucket struct {
	Bucket string `json:"bucket"`
	Token  string `json:"token"`
	Delta  int64  `json:"delta"`
	// In and Out are the gross halves of Delta. A bucket that received 900
	// and sent 400 is not one that received 500, and a chart drawing bars
	// from the net would say it was.
	In    int64 `json:"in"`
	Out   int64 `json:"out"`
	Items int   `json:"items"`
}

// DefiSpan is the first and last block time this account moved anything.
func (d *DB) DefiSpan(network, addr string) (first, last string, err error) {
	if addr == "" {
		return "", "", nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	var f, l sql.NullString
	err = d.db.QueryRow(`SELECT MIN(block_time), MAX(block_time) FROM (`+defiMovesSQL+`)
		WHERE block_time <> ''`, addr, network).Scan(&f, &l)
	return f.String, l.String, err
}

// DefiBuckets sums every leg into time buckets, over the whole history.
//
// prefix is how many characters of an RFC3339 block time name a bucket: 10
// for a day, 13 for an hour, 16 for a minute. The result is bounded by buckets times assets,
// never by legs, which is the point: the page draws a curve over 100,000 legs
// from a few hundred rows, and never needs the legs themselves.
//
// The gas this account paid is returned as a ugnot row of its own, under the
// pseudo-token "fee", so the caller can walk the native balance back through
// it without the history rows calling every swap a GNOT spend.
func (d *DB) DefiBuckets(network, addr string, prefix int) ([]DefiBucket, error) {
	return d.defiBuckets(network, addr, "", prefix)
}

// DefiTokenBuckets is DefiBuckets for one asset, with no fee row: what a
// per-asset curve draws when the account's shared axis is too coarse for it.
func (d *DB) DefiTokenBuckets(network, addr, token string, prefix int) ([]DefiBucket, error) {
	if token == "" {
		return []DefiBucket{}, nil
	}
	return d.defiBuckets(network, addr, token, prefix)
}

// DefiTokenSpan is DefiSpan for one asset.
func (d *DB) DefiTokenSpan(network, addr, token string) (first, last string, err error) {
	if addr == "" || token == "" {
		return "", "", nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	var f, l sql.NullString
	err = d.db.QueryRow(`SELECT MIN(block_time), MAX(block_time) FROM (`+defiMovesSQL+`)
		WHERE block_time <> '' AND token = ?3`, addr, network, token).Scan(&f, &l)
	return f.String, l.String, err
}

func (d *DB) defiBuckets(network, addr, token string, prefix int) ([]DefiBucket, error) {
	out := []DefiBucket{}
	if addr == "" {
		return out, nil
	}
	if prefix != 10 && prefix != 13 && prefix != 16 {
		return nil, fmt.Errorf("bucket prefix must be 10, 13 or 16, not %d", prefix)
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	// One asset is the same query narrowed, and without the fee row: gas is
	// native money and a token's curve has no business carrying it.
	tokenFilter, feeFilter := "", ""
	args := []any{addr, network}
	if token != "" {
		tokenFilter, feeFilter = "AND token = ?3", "AND 0"
		args = append(args, token)
	}
	rows, err := d.db.Query(fmt.Sprintf(`
		SELECT substr(block_time, 1, %[1]d) AS b, token, SUM(delta),
		       SUM(CASE WHEN delta > 0 THEN delta ELSE 0 END),
		       SUM(CASE WHEN delta < 0 THEN -delta ELSE 0 END),
		       SUM(items)
		  FROM (%[2]s)
		 WHERE block_time <> '' %[4]s
		 GROUP BY b, token
		UNION ALL
		SELECT substr(t.block_time, 1, %[1]d) AS b, 'fee', -SUM(t.gas_fee), 0, SUM(t.gas_fee), 0
		  FROM transactions t
		 WHERE t.network = ?2 AND t.block_time <> '' AND t.tx_hash IN (%[3]s) %[5]s
		 GROUP BY b
		 ORDER BY 1`, prefix, defiMovesSQL, defiPayerSQL, tokenFilter, feeFilter), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b DefiBucket
		if err := rows.Scan(&b.Bucket, &b.Token, &b.Delta, &b.In, &b.Out, &b.Items); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UnledgeredBankSends is the net ugnot this address moved by BankMsgSend
// without a matching leg in the coin ledger, and how many sends that was.
//
// It is the term the holdings reconciliation was missing. Early mainnet sends
// predate TransferEvent, so they never reached coin_transfers, and the page
// folded them into the unexplained residual it then called a probable genesis
// allocation. On g1qyfled… (2026-10-01) that residual was 758k GNOT against a
// 728k genesis vesting allocation: about 30k of it was sends sitting in
// bank_sends with a sender and a block height, not genesis.
func (d *DB) UnledgeredBankSends(network, addr string) (net int64, sends int, err error) {
	if addr == "" {
		return 0, 0, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	err = d.db.QueryRow(`
		SELECT COALESCE(SUM(CASE WHEN b.from_address = b.to_address THEN 0
		                         WHEN b.to_address = ?1 THEN b.ugnot_amount
		                         ELSE -b.ugnot_amount END), 0),
		       COUNT(*)
		  FROM bank_sends b
		 WHERE b.network = ?2 AND (b.from_address = ?1 OR b.to_address = ?1)
		   AND b.success AND COALESCE(b.ugnot_amount, 0) <> 0
		   AND NOT EXISTS (SELECT 1 FROM coin_transfers c
		                    WHERE c.network = b.network AND c.tx_hash = b.tx_hash
		                      AND c.from_addr = b.from_address AND c.to_addr = b.to_address)`,
		addr, network).Scan(&net, &sends)
	return net, sends, err
}
