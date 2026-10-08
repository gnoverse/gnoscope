package store

// Failures: the calls the chain reverted, scoped to a window and set against the
// window before it, so a rate reads as a story and not a bare percentage.
//
// A revert is not an error in the explorer's sense: most are ordinary user
// activity (an unfunded swap, a bid that lost a race), and a realm with a high
// failure share is often just a popular one. So the report keeps the count and
// the share apart, and ranks by count, because a realm with one call and one
// failure is 100% and tells nobody anything.
//
// The reason a call reverted is not stored: `calls` holds the call, not the
// response, and the response lives in the indexer. Nothing here claims a cause.

// FailureParams bounds one failures read. Since and PrevSince mean what they
// mean on PulseParams.
type FailureParams struct {
	Network   string
	Since     string
	PrevSince string
	Limit     int
}

// FailureTotals is one window's calls and how many reverted.
type FailureTotals struct {
	Calls  int `json:"calls"`
	Failed int `json:"failed"`
}

// FailedRealm is a realm ranked by reverted calls inside the window.
type FailedRealm struct {
	Network  string `json:"network,omitempty"`
	Path     string `json:"path"`
	Calls    int    `json:"calls"`
	Failed   int    `json:"failed"`
	Callers  int    `json:"failed_callers"`
	LastTime string `json:"last_failed_time"`
}

// FailedFunc is one function ranked the same way.
type FailedFunc struct {
	Network string `json:"network,omitempty"`
	Path    string `json:"path"`
	Func    string `json:"func"`
	Calls   int    `json:"calls"`
	Failed  int    `json:"failed"`
}

// FailedCall is one reverted call, newest first.
type FailedCall struct {
	Network     string `json:"network,omitempty"`
	TxHash      string `json:"tx_hash"`
	BlockHeight int    `json:"block_height"`
	BlockTime   string `json:"block_time"`
	Caller      string `json:"caller"`
	Path        string `json:"path"`
	Func        string `json:"func"`
}

// Failures is the whole report.
type Failures struct {
	Current FailureTotals `json:"current"`
	Prev    FailureTotals `json:"prev"`
	HasPrev bool          `json:"has_prev"`
	Realms  []FailedRealm `json:"realms"`
	Funcs   []FailedFunc  `json:"funcs"`
	Recent  []FailedCall  `json:"recent"`
}

// GetFailures reads the report from local SQLite over the same
// (network, block_time) range the pulse uses.
func (d *DB) GetFailures(p FailureParams) (*Failures, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	limit := p.Limit
	if limit <= 0 {
		limit = 10
	}
	nf := d.networkFilter("network", p.Network)
	out := &Failures{Realms: []FailedRealm{}, Funcs: []FailedFunc{}, Recent: []FailedCall{}}

	totals := func(since, until string) (FailureTotals, error) {
		var t FailureTotals
		clause, args := pulseRange("block_time", since, until)
		err := d.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN success THEN 0 ELSE 1 END), 0)
			FROM calls WHERE `+nf+clause, args...).Scan(&t.Calls, &t.Failed)
		return t, err
	}
	var err error
	if out.Current, err = totals(p.Since, ""); err != nil {
		return nil, err
	}
	if p.PrevSince != "" {
		if out.Prev, err = totals(p.PrevSince, p.Since); err != nil {
			return nil, err
		}
		out.HasPrev = true
	}

	rows, err := d.db.Query(`
		SELECT network, pkg_path, COUNT(*), SUM(CASE WHEN success THEN 0 ELSE 1 END) AS f,
		       COUNT(DISTINCT CASE WHEN success THEN NULL ELSE caller END), COALESCE(MAX(CASE WHEN success THEN NULL ELSE block_time END), '')
		  FROM calls WHERE `+nf+` AND block_time >= ?
		 GROUP BY network, pkg_path HAVING f > 0
		 ORDER BY f DESC, pkg_path ASC LIMIT ?`, p.Since, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r FailedRealm
		if err := rows.Scan(&r.Network, &r.Path, &r.Calls, &r.Failed, &r.Callers, &r.LastTime); err != nil {
			rows.Close()
			return nil, err
		}
		out.Realms = append(out.Realms, r)
	}
	rows.Close()

	rows, err = d.db.Query(`
		SELECT network, pkg_path, func_name, COUNT(*), SUM(CASE WHEN success THEN 0 ELSE 1 END) AS f
		  FROM calls WHERE `+nf+` AND block_time >= ?
		 GROUP BY network, pkg_path, func_name HAVING f > 0
		 ORDER BY f DESC, pkg_path ASC, func_name ASC LIMIT ?`, p.Since, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f FailedFunc
		if err := rows.Scan(&f.Network, &f.Path, &f.Func, &f.Calls, &f.Failed); err != nil {
			rows.Close()
			return nil, err
		}
		out.Funcs = append(out.Funcs, f)
	}
	rows.Close()

	rows, err = d.db.Query(`
		SELECT network, tx_hash, block_height, COALESCE(block_time, ''), caller, pkg_path, func_name
		  FROM calls WHERE `+nf+` AND block_time >= ? AND success = 0
		 ORDER BY block_height DESC, msg_index DESC LIMIT ?`, p.Since, limit*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c FailedCall
		if err := rows.Scan(&c.Network, &c.TxHash, &c.BlockHeight, &c.BlockTime, &c.Caller, &c.Path, &c.Func); err != nil {
			return nil, err
		}
		out.Recent = append(out.Recent, c)
	}
	return out, rows.Err()
}
