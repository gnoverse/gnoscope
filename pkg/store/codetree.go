package store

import "time"

// The whole deployed code of one network, in one read.
//
// /api/code/tree draws a file-tree explorer and a treemap of every package on
// a chain from a single payload, so this returns every current package with
// its file sizes and the few numbers that colour or rank a tile. It is four
// grouped scans rather than the per-row correlated subqueries ListPackages
// uses: one row per package across the whole chain is exactly the shape where
// those subqueries cost N lookups each, and here every number is wanted for
// every package anyway.
//
// Everything is filtered by one network and bound as a parameter. Joins and
// groupings never key on a path alone.

// CodeTreeFile is one file of a package: its name, line count and size in
// bytes. Same definitions as SourceFile.
type CodeTreeFile struct {
	Name  string
	Lines int
	Bytes int
}

// CodeTreePackage is one deployed package with its files and activity.
type CodeTreePackage struct {
	Path    string
	IsRealm bool
	Height  int
	Files   []CodeTreeFile
	// Calls and Callers are MsgCall messages to this path, and distinct
	// accounts sending them, since the window start.
	Calls   int
	Callers int
	// Dependents is how many packages on this network import this one.
	Dependents int
}

// CodeTree reads every deployed package on one network, ordered by path.
//
// A package is deployed when the network holds a current-state row for it
// and at least one of its submissions succeeded, or it has no submission rows
// at all (rows written before package_submissions existed). A path whose only
// submissions failed is left out: ProcessPackage no longer writes a row for
// one, but a database synced before that fix still carries them, and a tree
// of "every package on the chain" must not draw code the chain rejected.
//
// since bounds the activity counts (calls.block_time >= since).
//
// One read transaction, so the packages, files and counts are one snapshot.
func (d *DB) CodeTree(network string, since time.Time) ([]CodeTreePackage, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.Query(`
		SELECT p.path, p.is_realm, p.block_height
		FROM packages p
		WHERE p.network = ?
		  AND (EXISTS (SELECT 1 FROM package_submissions s
		               WHERE s.network = p.network AND s.path = p.path AND s.success)
		       OR NOT EXISTS (SELECT 1 FROM package_submissions s
		                      WHERE s.network = p.network AND s.path = p.path))
		ORDER BY p.path`, network)
	if err != nil {
		return nil, err
	}
	var out []CodeTreePackage
	idx := map[string]int{}
	for rows.Next() {
		var p CodeTreePackage
		if err := rows.Scan(&p.Path, &p.IsRealm, &p.Height); err != nil {
			rows.Close()
			return nil, err
		}
		p.Files = []CodeTreeFile{}
		idx[p.Path] = len(out)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Bytes and lines with the same expressions PackageSource uses, so a size
	// here and a size in /api/source agree.
	rows, err = tx.Query(`
		SELECT package_path, file_name,
		       LENGTH(body) - LENGTH(REPLACE(body, char(10), ''))
		         + (CASE WHEN body != '' AND SUBSTR(body, -1) != char(10) THEN 1 ELSE 0 END),
		       LENGTH(CAST(body AS BLOB))
		FROM package_files
		WHERE network = ?
		ORDER BY package_path, file_name`, network)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var path string
		var f CodeTreeFile
		if err := rows.Scan(&path, &f.Name, &f.Lines, &f.Bytes); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[path]; ok {
			out[i].Files = append(out[i].Files, f)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Every call counts, failed ones included, the same as the calls column
	// /api/packages serves: an attempt is somebody using the realm.
	rows, err = tx.Query(`
		SELECT pkg_path, COUNT(*), COUNT(DISTINCT caller)
		FROM calls
		WHERE network = ? AND block_time >= ?
		GROUP BY pkg_path`, network, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var path string
		var calls, callers int
		if err := rows.Scan(&path, &calls, &callers); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[path]; ok {
			out[i].Calls, out[i].Callers = calls, callers
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(`
		SELECT import_path, COUNT(DISTINCT package_path)
		FROM dependencies
		WHERE network = ?
		GROUP BY import_path`, network)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var path string
		var n int
		if err := rows.Scan(&path, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[path]; ok {
			out[i].Dependents = n
		}
	}
	rows.Close()
	return out, rows.Err()
}
