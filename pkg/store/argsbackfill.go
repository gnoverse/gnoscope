package store

import "strconv"

func argsBackfillCursorKey(network string) string {
	return "args_backfill_cursor:" + network
}

// ArgsBackfillRange returns the next block range whose messages still need
// their args and send columns filled, walking **backwards** from the sync tip.
//
// Backwards, unlike TokenBackfillRange, and the direction is the whole design.
// A ledger has to be complete before any of it is true, so the token walk goes
// forwards until it meets the forward fill and only the finished state is worth
// anything. This one fills a preview column: every batch it completes makes a
// page better on its own, and the pages people open are the recent ones. Going
// forwards would spend an hour on 2023 before the current tip gained a single
// argument.
//
// The cursor is the lowest height already done, so a restart resumes where it
// stopped and a finished walk stays finished. `more` is false once the walk
// has reached the oldest block stored for the network.
//
// Returns a half-open range [from, to).
func (d *DB) ArgsBackfillRange(network string, batch int) (from, to int, more bool, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var lowest, tip *int
	if err = d.db.QueryRow(
		`SELECT MIN(height), MAX(height) FROM blocks WHERE `+
			d.networkFilter("network", network)).Scan(&lowest, &tip); err != nil {
		return 0, 0, false, err
	}
	if lowest == nil || tip == nil {
		return 0, 0, false, nil // nothing stored yet, nothing to fill
	}

	// With no cursor the walk starts just above the tip, so the newest block is
	// in the first batch rather than one short of it.
	to = *tip + 1
	if cur, cerr := d.getSyncStateLocked(argsBackfillCursorKey(network)); cerr == nil && cur != "" {
		if n, perr := strconv.Atoi(cur); perr == nil && n < to {
			to = n
		}
	}
	if to <= *lowest {
		return 0, 0, false, nil // the walk has reached the oldest block stored
	}
	from = to - batch
	if from < *lowest {
		from = *lowest
	}
	return from, to, true, nil
}

// SetArgsBackfillCursor records the lowest height the walk has completed.
func (d *DB) SetArgsBackfillCursor(network string, height int) error {
	return d.SetSyncState(argsBackfillCursorKey(network), strconv.Itoa(height))
}
