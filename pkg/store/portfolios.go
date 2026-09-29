package store

// Who holds what, across every asset at once.
//
// The per-asset question ("who holds GNS") was already answerable by TopHolders.
// This is the other axis, and it is the one a reader actually arrives with:
// "who are the holders on this chain, and what do they hold". Answering it
// needed the GRC20 replay and the native sweep in the same result set, which is
// also where it gets delicate, because those two are not the same kind of
// number (see native.go).
//
// The join is deliberately left to the caller. This file returns positions, the
// API layer prices them, and the frontend ranks them: putting a dollar value in
// SQL would bake in a price this package has no business knowing and could not
// caveat.

// Position is one address's holding of one asset.
type Position struct {
	Address string `json:"address"`
	Token   string `json:"token"`
	Symbol  string `json:"symbol"`
	PkgPath string `json:"pkg_path,omitempty"`
	Balance int64  `json:"balance"`
	// Fungible says whether Balance is an amount or a count of items. A GRC721
	// rides the same Transfer event without one, so its legs sum to zero, and
	// printing that as a balance would read as "holds none".
	Fungible bool `json:"fungible"`
}

// AllPositions returns every positive GRC20 position on a network.
//
// Everything, not a page, because the ranking that matters is by **value**, and
// value needs a price the caller applies afterwards: cutting to a top-N here
// would rank by raw base units, which compares 1e18-scaled balances against
// 1e6-scaled ones and puts whichever token has the most decimals on top.
//
// Bounded by `limit` on the number of ROWS only as a runaway guard. On mainnet
// this is about a thousand rows (28 assets, a few hundred holders each), which
// is small; a chain where it is not will hit the guard and the caller says so
// rather than silently ranking a prefix.
func (d *DB) AllPositions(network string, limit int) ([]Position, bool, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Two measures per position, because the two kinds of asset are counted
	// differently and one query has to serve both:
	//
	//   bal   sum of value in minus value out. What a GRC20 holding is.
	//   items count of legs in minus legs out. What a GRC721 holding is.
	//
	// A GRC721 rides the GRC20 Transfer event and carries **no amount**, so
	// every leg parses to zero and `bal` is always zero for one. Keeping only
	// `bal > 0`, which is what this did first, silently dropped every NFT
	// position from a view that claims to span every asset, with no error and
	// no empty state: the rows simply were not there.
	rows, err := d.db.Query(`
		SELECT addr, token, MAX(pkg) AS pkg_path,
		       SUM(delta) AS bal, SUM(leg) AS items, MAX(amt) AS max_amt FROM (
			SELECT to_addr   AS addr, token, pkg_path AS pkg,  value AS delta,  1 AS leg, value AS amt
			  FROM token_transfers WHERE network = ?1 AND to_addr   <> ''
			UNION ALL
			SELECT from_addr AS addr, token, pkg_path AS pkg, -value AS delta, -1 AS leg, value AS amt
			  FROM token_transfers WHERE network = ?1 AND from_addr <> ''
		)
		GROUP BY addr, token
		HAVING (max_amt > 0 AND bal > 0) OR (max_amt = 0 AND items > 0)
		ORDER BY addr ASC
		LIMIT ?2`, network, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := []Position{}
	for rows.Next() {
		var p Position
		var items, maxAmt int64
		if err := rows.Scan(&p.Address, &p.Token, &p.PkgPath, &p.Balance, &items, &maxAmt); err != nil {
			return nil, false, err
		}
		// Fungibility from the data, the same test TokenSummaries makes: a
		// token whose every transfer carries a zero amount is not fungible.
		p.Fungible = maxAmt > 0
		if !p.Fungible {
			// Balance is a count of items for an NFT, and Fungible is what tells
			// a caller to render it that way. Nothing may price it: see
			// HandleHolders, which refuses to.
			p.Balance = items
		}
		_, p.Symbol = TokenKeyParts(p.Token)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, nil
}

// NativePositions returns swept native balances as positions, so one ranking
// can span both ledgers.
//
// The caller must keep the distinction: these come from the `balances` sweep,
// an incomplete sample of an unenumerable population, while AllPositions is an
// exact replay over a bounded window. Merging them without saying so produces a
// leaderboard that looks authoritative and is not.
func (d *DB) NativePositions(network string, limit int) ([]Position, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT address, ugnot FROM balances
		 WHERE network = ? AND ugnot > 0
		 ORDER BY ugnot DESC LIMIT ?`, network, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Position{}
	for rows.Next() {
		p := Position{Token: NativeKey, Symbol: "GNOT", Fungible: true}
		if err := rows.Scan(&p.Address, &p.Balance); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
