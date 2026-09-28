package store

import (
	"fmt"
	"time"
)

// A realm's activity over time.
//
// RealmUsage answers "who called this realm, and how much" as one number per
// caller, summed over the whole history. That is the right shape for a table
// and the wrong one for a graph: it cannot say whether the realm's 1,200 calls
// arrived last week or eighteen months ago, which is the only question a
// sparkline is asked.
//
// Built on the same `act` CTE as RealmUsage (activitySource), so the two agree
// by construction about what counts as a message aimed at this realm: direct
// MsgCalls plus the MsgRun half matched by the LIKE heuristic. A second
// hand-written union here would drift from that one silently, and the symptom
// would be a graph whose bars do not add up to the total printed beside them.

// RealmActivityPoint is one bucket of one realm's message history.
type RealmActivityPoint struct {
	Time     string `json:"time"`
	Messages int    `json:"messages"`
	// Callers is the distinct addresses that sent one of those messages in
	// this bucket, and it does NOT sum across buckets: an address that comes
	// back on three days is counted in all three. The realm's actual unique
	// callers is RealmUsageSummary.UniqueCallers, computed over the whole
	// window at once.
	Callers int `json:"callers"`
}

// RealmActivitySeries buckets a realm's messages over the last `days` days.
//
// Buckets are dense: a day with no traffic comes back as a zero rather than as
// a missing key. A sparkline drawn from a sparse series closes its own gaps, so
// a realm that went quiet for a fortnight is drawn as one that declined gently
// over it, a wrong answer that looks like data.
//
// The package's own network is resolved first, exactly as RealmUsage does it,
// so `?network=all` cannot blend two chains' calls into one line.
func (d *DB) RealmActivitySeries(network, path, granularity string, days int) ([]RealmActivityPoint, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var resolved string
	lookup := `SELECT network FROM packages WHERE path = ? AND ` + d.networkFilter("network", network) + ` LIMIT 1`
	if err := d.db.QueryRow(lookup, path).Scan(&resolved); err != nil {
		return nil, err
	}

	sqlFmt, step, truncFn := timeseriesFormat(granularity)
	since := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)

	src, args := activitySource(resolved, path, RealmUsageFilter{Since: since})
	// MATERIALIZED for the reason RealmUsage gives: the MsgRun half is a LIKE
	// over every script's source that no index can serve, and this query names
	// `act` once but SQLite is free to re-plan it.
	//
	// The block_time guard is not redundant with the Since filter. activitySource
	// COALESCEs a missing timestamp to the empty string, strftime('') is NULL,
	// and a NULL bucket would group every undated row (genesis, and anything
	// synced before the syncer knew a block's time) into one phantom point that
	// fillBuckets then drops on the floor without saying so.
	q := "WITH act AS MATERIALIZED (\n" + src + "\n)\n" +
		fmt.Sprintf(`SELECT strftime('%s', block_time) AS bucket, COUNT(*), COUNT(DISTINCT caller)
			FROM act WHERE block_time <> '' GROUP BY bucket ORDER BY bucket ASC`, sqlFmt)

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := make(map[string]*RealmActivityPoint)
	for rows.Next() {
		var p RealmActivityPoint
		if err := rows.Scan(&p.Time, &p.Messages, &p.Callers); err != nil {
			return nil, err
		}
		pt := p
		buckets[p.Time] = &pt
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return fillBuckets(buckets, days, granularity, step, truncFn,
		func(k string) RealmActivityPoint { return RealmActivityPoint{Time: k} },
		func(*RealmActivityPoint) {}), nil
}
