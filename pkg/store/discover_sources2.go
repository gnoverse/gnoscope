package store

import (
	"fmt"

	"github.com/gnoverse/gnoscope/pkg/discover"
)

// Four more event sources, and they are grouped here because they share a
// property the first three do not: every one of them reads a table this repo
// already maintains, with no indexer round trip, no RPC call and no schema
// change.
//
// That is the whole selection rule for this batch. The five kinds still unwired
// each need something else first (the inert lifecycle needs the indexer and an
// RPC read, namespace.registered needs a name the chain does not store in any
// table, and the two proposal kinds read a live overview), so they are their own
// work rather than a longer version of this.

// sourceValidatorRegistered: an address registering as a candidate validator.
//
// Two functions, not one. Register is the debut and is obviously news;
// UpdateDescription is news only when it sets a name, because the valopers
// contract uses the same call to change a website or a contact and most of
// those carry no moniker at all. The design's own count on mainnet was 22
// Register rows and 5 UpdateDescription rows with a non-empty moniker.
//
// success = 1 because a failed registration registered nobody, and the feed
// reports what happened rather than what was attempted.
func (d *DB) sourceValidatorRegistered(network, since string) ([]discoverCandidate, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT tx_hash, block_height, block_time, address, moniker
		  FROM valoper_registrations
		 WHERE network = ? AND success = 1
		   AND block_height > 0
		   AND block_time >= ?
		   AND (func_name = 'Register' OR (func_name = 'UpdateDescription' AND moniker <> ''))
		 ORDER BY block_time DESC`, network, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []discoverCandidate
	for rows.Next() {
		var txHash, blockTime, address, moniker string
		var height int64
		if err := rows.Scan(&txHash, &height, &blockTime, &address, &moniker); err != nil {
			return nil, err
		}
		out = append(out, discoverCandidate{
			Kind: "validator.registered",
			// The address, not the transaction: a valoper that registers and
			// then renames itself is one subject with two events, and keying on
			// the tx would make them two unrelated rows to a consumer deduping
			// on id. The height is the ordinal, so both survive and they sort.
			Subject:  address,
			Ordinal:  int(height),
			At:       blockTime,
			Height:   height,
			Actor:    address,
			Evidence: txHash,
			// One validator is one validator. Reach is unique actors, and
			// inventing a larger number here would buy this kind a rank its
			// editorial base already decides.
			Reach:     1,
			Magnitude: 1,
			Input: discover.ValidatorRegistered{
				Moniker: moniker,
				Address: address,
				Height:  height,
			},
		})
	}
	return out, rows.Err()
}

// sourceTransferLarge: a bank transfer above the threshold.
//
// The design specified an ALTER TABLE here, adding a parsed amount column
// because `bank_sends.amount` is a decorated coin *list* ("100ugnot,5foo") that
// no SQL predicate can threshold. That is stale: `ugnot_amount` already exists
// on the table and the syncer already fills it, so this is a plain predicate
// and the migration the design worried about is not needed. Checked against
// schema.go rather than assumed, because the design is a month older than the
// column.
//
// ugnot_amount is nullable, and a NULL is a row whose amount the syncer could
// not parse rather than a row worth zero. `>= threshold` excludes NULL on its
// own, which is the behaviour we want and is worth saying out loud: a parse
// failure must not be able to become a headline.
func (d *DB) sourceTransferLarge(network, since string) ([]discoverCandidate, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	const ugnotPerGNOT = 1_000_000
	threshold := int64(discover.TransferLargeThresholdGNOT) * ugnotPerGNOT

	rows, err := d.db.Query(`
		SELECT tx_hash, block_height, block_time, from_address, to_address, ugnot_amount
		  FROM bank_sends
		 WHERE network = ? AND block_height > 0
		   AND block_time >= ?
		   AND ugnot_amount >= ?
		 ORDER BY block_time DESC`, network, since, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []discoverCandidate
	for rows.Next() {
		var txHash, blockTime, from, to string
		var height, ugnot int64
		if err := rows.Scan(&txHash, &height, &blockTime, &from, &to, &ugnot); err != nil {
			return nil, err
		}
		gnot := ugnot / ugnotPerGNOT
		out = append(out, discoverCandidate{
			Kind:     "transfer.large",
			Subject:  txHash,
			At:       blockTime,
			Height:   height,
			Actor:    from,
			Evidence: txHash,
			// A send has exactly two parties, and that is the honest reach.
			// Scoring it by the amount instead would let one transfer outrank a
			// spike involving hundreds of people, which is the one-dimensional
			// failure this feature is built to avoid.
			Reach:     2,
			Magnitude: float64(gnot),
			Input: discover.TransferLarge{
				GNOT:   gnot,
				From:   from,
				To:     to,
				Height: height,
			},
		})
	}
	return out, rows.Err()
}

// sourcePackageFirstCall: the first successful call a package ever received.
//
// A different event from the deploy and usually much later, which is the reason
// it is its own kind: a package nobody has ever used and a package somebody
// just used for the first time are different news, and only the second one says
// anything about whether the thing works.
//
// first_seen carries the fact and the date; `external` needs the creator, and
// that join is the one AGENTS.md warns about, so it is on both halves of the
// primary key. `first_seen.subject` for this kind is the package path.
func (d *DB) sourcePackageFirstCall(network, since string) ([]discoverCandidate, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// The caller is not in first_seen, so it is resolved from the winning call:
	// the earliest successful call on that path, by height. idx_calls_net_pkg_height
	// covers the lookup, and ordering by height rather than block_time is
	// deliberate for the same reason the design gives, a block_time sort is in
	// no index and scans.
	rows, err := d.db.Query(`
		SELECT f.subject, f.at, f.height,
		       COALESCE(p.creator, ''),
		       COALESCE((SELECT c.caller FROM calls c
		                  WHERE c.network = f.network AND c.pkg_path = f.subject
		                    AND c.success = 1
		                  ORDER BY c.block_height ASC LIMIT 1), '')
		  FROM first_seen f
		  LEFT JOIN packages p ON p.network = f.network AND p.path = f.subject
		 WHERE f.network = ? AND f.kind = ?
		   AND f.height > 0
		   AND f.at >= ?
		 ORDER BY f.at DESC`, network, FirstSeenPackageCalled, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []discoverCandidate
	for rows.Next() {
		var path, at, creator, caller string
		var height int64
		if err := rows.Scan(&path, &at, &height, &creator, &caller); err != nil {
			return nil, err
		}
		// No caller means the call that set first_seen is not in `calls`, which
		// should not happen and is not something to paper over with a blank in
		// a sentence: skip it rather than emit an event whose actor is empty.
		if caller == "" {
			continue
		}
		out = append(out, discoverCandidate{
			Kind:      "package.first_call",
			Subject:   path,
			At:        at,
			Height:    height,
			Actor:     caller,
			Target:    path,
			Namespace: func() string { ns, _ := splitGnoPath(path); return ns }(),
			// first_seen does not carry the transaction, and inventing a link
			// that resolves to the wrong tx is worse than showing none.
			FirstEver: true,
			Reach:     1,
			Magnitude: 1,
			Input: discover.PackageFirstCall{
				Path:     path,
				Ref:      shortRef(path),
				Caller:   caller,
				Height:   height,
				External: creator != "" && caller != creator,
			},
		})
	}
	return out, rows.Err()
}

// sourcePackageSpike: a package taking far more calls in a day than it usually
// does.
//
// One series per package, because a spike is a statement about that package's
// own normal: r/gnoland/wugnot's quiet day is busier than most realms' best,
// and a chain-wide baseline would report the busy realms every day and the
// quiet ones never.
//
// The design notes this emits nothing at all on a young mainnet, and the floor
// is why: 25 calls in a day before a ratio is even considered. That is the
// detector refusing to call three calls against a median of one a tenfold
// surge, which it arithmetically is and which is not news.
func (d *DB) sourcePackageSpike(network, since string) ([]discoverCandidate, error) {
	d.mu.RLock()
	rows, err := d.db.Query(`
		SELECT pkg_path, day, SUM(calls), COUNT(DISTINCT caller), MAX(last_height)
		  FROM caller_edges
		 WHERE network = ?
		 GROUP BY pkg_path, day
		 ORDER BY pkg_path, day`, network)
	if err != nil {
		d.mu.RUnlock()
		return nil, err
	}
	type dayRow struct {
		day     string
		calls   int
		callers int
		height  int64
	}
	byPath := map[string][]dayRow{}
	var order []string
	for rows.Next() {
		var path string
		var r dayRow
		if err := rows.Scan(&path, &r.day, &r.calls, &r.callers, &r.height); err != nil {
			rows.Close()
			d.mu.RUnlock()
			return nil, err
		}
		if _, seen := byPath[path]; !seen {
			order = append(order, path)
		}
		byPath[path] = append(byPath[path], r)
	}
	err = rows.Err()
	rows.Close()
	d.mu.RUnlock()
	if err != nil {
		return nil, err
	}

	sinceDay := since
	if len(sinceDay) > 10 {
		sinceDay = sinceDay[:10]
	}

	var out []discoverCandidate
	for _, path := range order {
		series := byPath[path]
		if len(series) == 0 {
			continue
		}
		points := make([]discover.Point, len(series))
		for i, r := range series {
			points[i] = discover.Point{Day: r.day, X: float64(r.calls)}
		}
		// This package's own first active day, not the chain's. The detector
		// uses the start to refuse a spike it has no baseline for, and a
		// package deployed yesterday has no history whatever the chain's age.
		verdicts := discover.Detect(points, discover.SpikeFloors["package.spike"], series[0].day)

		byDay := map[string]dayRow{}
		for _, r := range series {
			byDay[r.day] = r
		}
		for _, v := range verdicts {
			if !v.Fired || v.Day < sinceDay {
				continue
			}
			r := byDay[v.Day]
			out = append(out, discoverCandidate{
				Kind: "package.spike",
				// The path and the day together: one package can spike twice in
				// a window and they are different events.
				Subject:   fmt.Sprintf("%s/%s", path, v.Day),
				At:        v.Day + "T00:00:00Z",
				Height:    r.height,
				Target:    path,
				Namespace: func() string { ns, _ := splitGnoPath(path); return ns }(),
				// Unique callers, which caller_edges gives for free and which is
				// the number that should rank this: a thousand calls from one
				// address is a bot, and twelve calls from twelve people is news.
				Reach:     int64(r.callers),
				Magnitude: float64(r.calls),
				Input: discover.PackageSpike{
					Ref:            shortRef(path),
					Day:            v.Day,
					Calls:          r.calls,
					Callers:        r.callers,
					BaselineMedian: int(v.Median + 0.5),
					Ratio:          v.Ratio,
				},
			})
		}
	}
	return out, nil
}

// shortRef is the form a reader recognises: r/gnoswap/router, not
// gno.land/r/gnoswap/router. The prefix is on every path on the chain, so it
// distinguishes nothing and costs nine characters of a budgeted line.
func shortRef(path string) string {
	if len(path) > 9 && path[:9] == "gno.land/" {
		return path[9:]
	}
	return path
}
