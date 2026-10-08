package store

// LargeSend is one successful native transfer of at least a threshold.
type LargeSend struct {
	Network     string `json:"network,omitempty"`
	TxHash      string `json:"tx_hash"`
	BlockHeight int    `json:"block_height"`
	BlockTime   string `json:"block_time"`
	From        string `json:"from"`
	To          string `json:"to"`
	Ugnot       int64  `json:"ugnot"`
}

// LargeSends lists successful ugnot sends of at least minUgnot since the given
// time, biggest first. A failed send moved nothing, so it is never listed.
func (d *DB) LargeSends(network, since string, minUgnot int64, limit int) ([]LargeSend, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	nf := d.networkFilter("network", network)
	rows, err := d.db.Query(`
		SELECT network, tx_hash, block_height, COALESCE(block_time, ''), from_address, to_address, COALESCE(ugnot_amount, 0)
		  FROM bank_sends
		 WHERE `+nf+` AND block_time >= ? AND success = 1 AND COALESCE(ugnot_amount, 0) >= ?
		 ORDER BY ugnot_amount DESC, block_height DESC LIMIT ?`, since, minUgnot, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []LargeSend{}
	for rows.Next() {
		var s LargeSend
		if err := rows.Scan(&s.Network, &s.TxHash, &s.BlockHeight, &s.BlockTime, &s.From, &s.To, &s.Ugnot); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
