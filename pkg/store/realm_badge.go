package store

import (
	"database/sql"
)

// RealmStats is the handful of numbers a one-line badge can carry.
//
// Deliberately not RealmUsage: that one answers the calls tab, which means a
// callers table capped at 500, a functions table, and a page of the message
// feed, all of which are thrown away by a caller that wants to print "1,234
// txs". A badge is fetched once per read of every document that embeds it, so
// what it costs per fetch is the whole design constraint.
type RealmStats struct {
	Path    string
	Network string
	// Messages is everything aimed at the realm, calls and MsgRuns alike; Txs
	// is the distinct transactions those arrived in. Both, because one
	// transaction can carry several messages and the two readings differ for
	// exactly the realms that are worth reading about.
	Messages      int
	Txs           int
	UniqueCallers int
	// Deploys is how many accepted submissions this path has had, which is the
	// closest thing to a version number a chain can answer: gno stores no
	// version field, so the Nth submission at a path is the Nth release of it.
	// Zero for a package the index holds with no submission of its own, a
	// genesis package being the ordinary case.
	Deploys int
	// LastDeployTime and LastActivity are RFC3339, or empty when the index has
	// no timestamp for them.
	LastDeployTime string
	LastActivity   string
}

// RealmBadgeStats counts one realm's whole history, or the part of it since
// an RFC3339 lower bound.
//
// An unknown path is an error rather than a zeroed struct: a badge that prints
// "0 txs" for a realm whose path has a typo in it is worse than one that says
// the path is wrong, because zero is a number a reader believes.
func (d *DB) RealmBadgeStats(network, path, since string) (*RealmStats, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Resolve the package's own network first, the same way RealmUsage does:
	// every query below binds it, so `?network=all` cannot sum two chains'
	// traffic into one badge (AGENTS.md: everything is network-scoped).
	var resolved string
	q := `SELECT network FROM packages WHERE path = ? AND ` + d.networkFilter("network", network) + ` LIMIT 1`
	if err := d.db.QueryRow(q, path).Scan(&resolved); err != nil {
		return nil, err
	}

	out := &RealmStats{Path: path, Network: resolved}

	src, args := activitySource(resolved, path, RealmUsageFilter{Since: since})
	sum := "WITH act AS MATERIALIZED (\n" + src + "\n)\nSELECT COUNT(*), COUNT(DISTINCT tx_hash), COUNT(DISTINCT caller), " +
		"(SELECT block_time FROM act ORDER BY block_height DESC LIMIT 1) FROM act"
	var last sql.NullString
	if err := d.db.QueryRow(sum, args...).Scan(&out.Messages, &out.Txs, &out.UniqueCallers, &last); err != nil {
		return nil, err
	}
	out.LastActivity = last.String

	// Submissions, not the packages row: packages is written INSERT OR REPLACE
	// and keeps only the latest deploy, so counting releases there would answer
	// 1 for every path however many times it has been redeployed. Failed
	// submissions are excluded — a deploy that reverted released nothing.
	var key sql.NullString
	rev := `SELECT COUNT(*), MAX(` + heightTimeKey + `) FROM package_submissions
		WHERE network = ? AND path = ? AND success = 1`
	if err := d.db.QueryRow(rev, resolved, path).Scan(&out.Deploys, &key); err != nil {
		return nil, err
	}
	if key.Valid {
		_, out.LastDeployTime = splitHeightTimeKey(key.String)
	}

	return out, nil
}
