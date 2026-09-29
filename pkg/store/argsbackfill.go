package store

import "strconv"

// The cursor key carries the pass, so bumping argsPass restarts the walk from
// the tip for free rather than needing a reset anybody has to remember to run.
func argsBackfillCursorKey(network string) string {
	return "args_backfill_cursor:v" + strconv.Itoa(argsPass) + ":" + network
}

// ArgsBackfillHeights returns the next batch of block heights whose messages
// still need their args and send columns filled, newest first.
//
// Heights, not a range, and that is the fix for two separate mistakes in the
// first version of this.
//
// The first was fatal. That version took its bounds from the `blocks` table,
// copying TokenBackfillRange, and `blocks` is *windowed*: `-block-history-days`
// keeps 90 days of it, while calls and package_submissions go back to genesis.
// So `MIN(height) FROM blocks` was around the sync tip, the walk decided it had
// reached the oldest block stored after exactly one 100-block batch, and it
// stopped. Observed on the live instance 2026-09-29: one pass at 430865..430964,
// then silence, with every row below that still empty.
//
// The second was waste. Walking every height in a range spends a round trip on
// each one, and mainnet's 32,774 calls are spread across 430,000 blocks, so the
// overwhelming majority of those fetches would find nothing to update. Asking
// the source tables which heights actually hold candidate rows skips the rest
// without a request.
//
// The candidate test is a *skip filter*, never the stop condition. The cursor
// is what guarantees termination: it only moves down, and every height it
// passes is done whether or not anything was written there. That matters most
// for the two tables still keyed on an empty `send`, which is the steady state
// of the overwhelming majority of messages: without the cursor the walk would
// offer those heights forever.
func (d *DB) ArgsBackfillHeights(network string, batch int) ([]int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// No cursor yet means start above every stored height, so the newest block
	// is in the first batch rather than one short of it.
	below := int(^uint(0) >> 1)
	if cur, err := d.getSyncStateLocked(argsBackfillCursorKey(network)); err == nil && cur != "" {
		if n, perr := strconv.Atoi(cur); perr == nil {
			below = n
		}
	}

	nf := d.networkFilter("network", network)
	rows, err := d.db.Query(`
		SELECT height FROM (
			-- args_pass, not "args is empty": an empty args is also the
			-- steady state of a call that took no arguments, and after a pass
			-- bump the rows that need rewriting are precisely the ones that are
			-- NOT empty. Only this column can tell the two apart.
			SELECT DISTINCT block_height height FROM calls
			 WHERE block_height < ? AND args_pass < ? AND `+nf+`
			UNION
			SELECT DISTINCT block_height FROM package_submissions
			 WHERE block_height < ? AND send = '' AND `+nf+`
			UNION
			SELECT DISTINCT block_height FROM msg_runs
			 WHERE block_height < ? AND send = '' AND `+nf+`
		) ORDER BY height DESC LIMIT ?`, below, argsPass, below, below, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []int{}
	for rows.Next() {
		var h int
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetArgsBackfillCursor records the lowest height the walk has completed.
func (d *DB) SetArgsBackfillCursor(network string, height int) error {
	return d.SetSyncState(argsBackfillCursorKey(network), strconv.Itoa(height))
}
