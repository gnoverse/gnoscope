package store

// PackageDeploy is one MsgAddPackage against a path: a submission, successful
// or not.
//
// The deploy history is what a reader of a realm page actually means by "when
// did this change", and it is the only history a package has. Source bodies are
// not versioned here — package_files keeps the current one and nothing else, so
// a diff between two submissions needs the files re-fetched per transaction and
// is deliberately out of scope.
type PackageDeploy struct {
	Network     string `json:"network,omitempty"`
	TxHash      string `json:"tx_hash"`
	MsgIndex    int    `json:"msg_index"`
	Creator     string `json:"creator"`
	Name        string `json:"name"`
	BlockHeight int    `json:"block_height"`
	BlockTime   string `json:"block_time,omitempty"`
	Success     bool   `json:"success"`
	NumFiles    int    `json:"num_files"`
}

// PackageDeploys returns every submission at one path, newest first.
//
// package_submissions, never packages: packages is a current-state projection
// with one row per path, so reading a history out of it returns exactly one
// entry and calls it the whole story. Under the inert submission policy a
// parked package is invisible to every liveness probe, so a deployer who
// verifies by querying the path resubmits — routinely, once per retry — and
// those resubmissions are the history. See the schema comment on the table.
//
// Ordered by height then msg_index descending, not by created_at: created_at is
// when *we* stored the row, which is sync order, and a backfill reorders it.
func (d *DB) PackageDeploys(network, path string, limit int) ([]PackageDeploy, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 200
	}
	rows, err := d.db.Query(`
		SELECT network, tx_hash, msg_index, creator, name, block_height,
		       COALESCE(block_time, ''), success, num_files
		FROM package_submissions
		WHERE path = ? AND `+d.networkFilter("network", network)+`
		ORDER BY block_height DESC, msg_index DESC
		LIMIT ?`, path, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PackageDeploy{}
	for rows.Next() {
		var dp PackageDeploy
		if err := rows.Scan(&dp.Network, &dp.TxHash, &dp.MsgIndex, &dp.Creator, &dp.Name,
			&dp.BlockHeight, &dp.BlockTime, &dp.Success, &dp.NumFiles); err != nil {
			return nil, err
		}
		out = append(out, dp)
	}
	return out, rows.Err()
}

// CountPackageDeploys is the total behind a truncated PackageDeploys page.
func (d *DB) CountPackageDeploys(network, path string) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM package_submissions
		WHERE path = ? AND `+d.networkFilter("network", network), path).Scan(&n)
	return n, err
}
