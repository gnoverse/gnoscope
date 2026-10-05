package store

import (
	"database/sql"

	"github.com/gnoverse/gnoscope/pkg/tags"
)

// Every MsgAddPackage on one network, oldest first, with what is cheaply known
// about the packages they left behind.
//
// /api/code/timeline is a feed of publications, and the only history a
// package has is package_submissions: packages is a current-state projection
// with one row per path, so a feed read out of it would show each path once,
// at its latest stamp, and lose every first publication that was later
// redeployed. The submission rows are the feed; the current-state tables only
// decorate the rows that are still current.
//
// One read transaction, so the submissions and the decorations are one
// snapshot. Every query is filtered by one network, bound as a parameter, and
// no join keys on a path alone.

// TimelineSubmission is one MsgAddPackage, successful or not.
type TimelineSubmission struct {
	TxHash   string
	MsgIndex int
	Path     string
	Creator  string
	Height   int
	Time     string
	IsRealm  bool
	NumFiles int
	Success  bool
}

// TimelinePackage is what the current-state tables say about a path whose
// source is stored: which submission that source came from, and its size.
type TimelinePackage struct {
	TxHash  string
	Lines   int
	Imports int
	// Doc is the package's own doc comment, else the first prose line of its
	// README, else empty. Untrimmed: the caller cuts it to a line.
	Doc string
}

// CodeTimelineSource is everything the timeline is computed from.
type CodeTimelineSource struct {
	// Submissions are ordered by (height, tx hash, message index) ascending,
	// the order the classifier walks them in.
	Submissions []TimelineSubmission
	// Current is keyed by path.
	Current map[string]TimelinePackage
	// Users maps an address to its registered r/sys/users name, for the
	// addresses that have one.
	Users map[string]string
	// Debuts maps a creator to the height of their first successful
	// submission, from first_seen: the same table discover's "published for
	// the first time" event reads, so the two cannot disagree.
	Debuts map[string]int
	// Tags are the code-derived tags of the stored source, keyed by path.
	// They describe the current submission only, like Lines and Imports.
	Tags map[string][]tags.Tag
}

// CodeTimelineStamp is a cheap fingerprint of one network's submissions: any
// new MsgAddPackage moves the count, and a backfill moves it too. The tag
// rows are counted in, so the tag refresh pass catching up a database (the
// first start after the table exists) shows on the feed without waiting out
// the memo.
func (d *DB) CodeTimelineStamp(network string) (count, maxHeight int, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	err = d.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM package_submissions WHERE network = ?)
		  + (SELECT COUNT(*) FROM package_tags WHERE network = ?),
		(SELECT COALESCE(MAX(block_height), -1) FROM package_submissions WHERE network = ?)`,
		network, network, network).Scan(&count, &maxHeight)
	return count, maxHeight, err
}

// CodeTimeline reads one network's submissions and their decorations.
func (d *DB) CodeTimeline(network string) (CodeTimelineSource, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	out := CodeTimelineSource{
		Current: map[string]TimelinePackage{},
		Users:   map[string]string{},
		Debuts:  map[string]int{},
		Tags:    map[string][]tags.Tag{},
	}
	tx, err := d.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Ordered by tx hash inside a block because the block's own transaction
	// order is not stored. Arbitrary, but fixed, which is what a cursor needs.
	rows, err := tx.Query(`
		SELECT tx_hash, msg_index, path, creator, block_height, COALESCE(block_time, ''),
		       is_realm, num_files, success
		FROM package_submissions
		WHERE network = ?
		ORDER BY block_height, tx_hash, msg_index`, network)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var s TimelineSubmission
		if err := rows.Scan(&s.TxHash, &s.MsgIndex, &s.Path, &s.Creator, &s.Height, &s.Time,
			&s.IsRealm, &s.NumFiles, &s.Success); err != nil {
			rows.Close()
			return out, err
		}
		out.Submissions = append(out.Submissions, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	rows, err = tx.Query(`SELECT path, tx_hash FROM packages WHERE network = ?`, network)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var path string
		var p TimelinePackage
		if err := rows.Scan(&path, &p.TxHash); err != nil {
			rows.Close()
			return out, err
		}
		out.Current[path] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Lines with the expression CodeTree and PackageSource use, so a package's
	// size reads the same on every page.
	if err := scanInto(tx, `
		SELECT package_path,
		       SUM(LENGTH(body) - LENGTH(REPLACE(body, char(10), ''))
		           + (CASE WHEN body != '' AND SUBSTR(body, -1) != char(10) THEN 1 ELSE 0 END))
		FROM package_files WHERE network = ? GROUP BY package_path`, network,
		func(path string, n int) {
			if p, ok := out.Current[path]; ok {
				p.Lines = n
				out.Current[path] = p
			}
		}); err != nil {
		return out, err
	}
	if err := scanInto(tx, `
		SELECT package_path, COUNT(*) FROM dependencies WHERE network = ? GROUP BY package_path`, network,
		func(path string, n int) {
			if p, ok := out.Current[path]; ok {
				p.Imports = n
				out.Current[path] = p
			}
		}); err != nil {
		return out, err
	}

	// The package's own words, in the order the directory prefers them (see
	// PackageDocs and PackageReadmes): the doc comment, then a README lead.
	if err := scanStrings(tx, `
		SELECT package_path, package_doc FROM symbol_index
		WHERE network = ? AND package_doc <> ''`, network,
		func(path, doc string) {
			if p, ok := out.Current[path]; ok {
				p.Doc = doc
				out.Current[path] = p
			}
		}); err != nil {
		return out, err
	}
	if err := scanStrings(tx, `
		SELECT package_path, SUBSTR(body, 1, 2000) FROM package_files
		WHERE network = ? AND LOWER(file_name) = 'readme.md'`, network,
		func(path, body string) {
			if p, ok := out.Current[path]; ok && p.Doc == "" {
				p.Doc = readmeLead(body)
				out.Current[path] = p
			}
		}); err != nil {
		return out, err
	}

	// One name per address. An address can hold several; the newest
	// registration is the one it is most likely known by now, and the name
	// breaks a tie so the answer does not depend on scan order.
	if err := scanStrings(tx, `
		SELECT address, name FROM users
		WHERE network = ? AND deleted = 0 AND alias = 0
		ORDER BY block_height, name`, network,
		func(addr, name string) { out.Users[addr] = name }); err != nil {
		return out, err
	}
	if err := scanInto(tx, `
		SELECT subject, height FROM first_seen WHERE network = ? AND kind = '`+FirstSeenDeployer+`'`, network,
		func(addr string, h int) { out.Debuts[addr] = h }); err != nil {
		return out, err
	}
	trows, err := tx.Query(`SELECT path, tag, evidence FROM package_tags WHERE network = ?`, network)
	if err != nil {
		return out, err
	}
	defer trows.Close()
	for trows.Next() {
		var path string
		var t tags.Tag
		if err := trows.Scan(&path, &t.Tag, &t.Why); err != nil {
			return out, err
		}
		out.Tags[path] = append(out.Tags[path], t)
	}
	if err := trows.Err(); err != nil {
		return out, err
	}
	for _, ts := range out.Tags {
		tags.Sort(ts)
	}
	return out, nil
}

func scanInto(tx *sql.Tx, q, network string, fn func(string, int)) error {
	rows, err := tx.Query(q, network)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return err
		}
		fn(k, n)
	}
	return rows.Err()
}

func scanStrings(tx *sql.Tx, q, network string, fn func(string, string)) error {
	rows, err := tx.Query(q, network)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		fn(k, v)
	}
	return rows.Err()
}
