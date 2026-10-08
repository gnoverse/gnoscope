package store

import (
	"database/sql"
	"errors"
	"time"
)

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// GenesisRow is one account of the gnoland-1 genesis.
type GenesisRow struct {
	Addr  [20]byte
	Ugnot int64
	// VestUgnot, VestStart and VestEnd describe the vesting clause, if any.
	// HasVesting says whether there was one: a clause for 0 ugnot is not "none".
	HasVesting bool
	VestUgnot  int64
	VestStart  int64
	VestEnd    int64
	// VestDelayed marks a schedule that releases nothing before VestEnd,
	// rather than continuously between VestStart and VestEnd.
	VestDelayed bool
}

// GenesisReset empties the table and its marker, so an import never builds on a
// partial one.
func (d *DB) GenesisReset() error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if _, err := d.db.Exec(`DELETE FROM genesis_import`); err != nil {
		return err
	}
	_, err := d.db.Exec(`DELETE FROM genesis_balances`)
	return err
}

// GenesisInsert writes one batch in one transaction.
func (d *DB) GenesisInsert(rows []GenesisRow) error {
	if len(rows) == 0 {
		return nil
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO genesis_balances (addr, ugnot, vest_ugnot, vest_start, vest_end, vest_delayed) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		var vu, vs, ve any
		if r.HasVesting {
			vu, vs, ve = r.VestUgnot, r.VestStart, r.VestEnd
		}
		if _, err := stmt.Exec(r.Addr[:], r.Ugnot, vu, vs, ve, r.VestDelayed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GenesisMarkImported writes the marker. Call it only after the last batch.
func (d *DB) GenesisMarkImported(source, sha string, rows int) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.Exec(`INSERT OR REPLACE INTO genesis_import (source, sha256, rows, imported_at) VALUES (?, ?, ?, ?)`,
		source, sha, rows, time.Now().UTC().Format(time.RFC3339))
	return err
}

// GenesisImported reports the marker, if a complete import exists.
func (d *DB) GenesisImported() (sha string, rows int, ok bool, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	e := d.db.QueryRow(`SELECT sha256, rows FROM genesis_import LIMIT 1`).Scan(&sha, &rows)
	if e != nil {
		if isNoRows(e) {
			return "", 0, false, nil
		}
		return "", 0, false, e
	}
	return sha, rows, true, nil
}

// GenesisLookup returns an address's genesis account. found is false for an
// address the genesis did not contain; the caller must check GenesisImported
// first, or "not found" reads as "not in genesis" on a database that never
// finished importing.
func (d *DB) GenesisLookup(addr []byte) (row GenesisRow, found bool, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var vu, vs, ve *int64
	e := d.db.QueryRow(`SELECT ugnot, vest_ugnot, vest_start, vest_end, vest_delayed FROM genesis_balances WHERE addr = ?`, addr).
		Scan(&row.Ugnot, &vu, &vs, &ve, &row.VestDelayed)
	if e != nil {
		if isNoRows(e) {
			return row, false, nil
		}
		return row, false, e
	}
	copy(row.Addr[:], addr)
	if vu != nil && vs != nil && ve != nil {
		row.HasVesting, row.VestUgnot, row.VestStart, row.VestEnd = true, *vu, *vs, *ve
	}
	return row, true, nil
}

// GenesisTotals counts the accounts and sums their allocations, for checking an
// import against the sheet's own published figures.
func (d *DB) GenesisTotals() (rows int, ugnot int64, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	err = d.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ugnot), 0) FROM genesis_balances`).Scan(&rows, &ugnot)
	return
}
