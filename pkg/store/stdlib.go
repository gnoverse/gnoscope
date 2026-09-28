package store

import (
	"fmt"
	"strings"
)

// The standard library, which is on the chain but not on the chain.
//
// Stdlib ships inside the node binary. It is never a MsgAddPackage, so the
// transaction stream cannot see it and no amount of syncing produces it: the
// indexer is right, the corpus is simply incomplete by construction. Measured
// 2026-09-28, the code index held 2,925 files and not one of them was
// `strings` or `avl`, which disqualifies it for the editor and agent clients
// the /developer API exists to serve.
//
// It gets its own table rather than a `source` column on package_files, and
// that is the whole safety argument. package_files is read by the storage
// series, the analytics totals, the symbol index and the realm detail; adding
// stdlib rows to it would require every one of those to remember a filter, and
// the one that forgot would silently report the node's own source as something
// somebody deployed. A separate table cannot be read by a query that does not
// name it.
//
// What the two do share is the search index, which is the point of having
// stdlib at all.

const stdlibSchema = `
CREATE TABLE IF NOT EXISTS stdlib_files (
	network      TEXT NOT NULL,
	package_path TEXT NOT NULL,
	file_name    TEXT NOT NULL,
	body         TEXT NOT NULL,
	PRIMARY KEY (network, package_path, file_name)
);
CREATE INDEX IF NOT EXISTS idx_stdlib_pkg ON stdlib_files(network, package_path);`

// StdlibFile is one file of one stdlib package.
type StdlibFile struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

// UpsertStdlibFile stores one stdlib file and keeps the search index in step,
// in one transaction, the same way UpsertPackageFile does for on-chain source.
func (d *DB) UpsertStdlibFile(network, pkgPath, fileName, body string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO stdlib_files (network, package_path, file_name, body)
		VALUES (?, ?, ?, ?)`, network, pkgPath, fileName, body); err != nil {
		return err
	}
	// FTS5 has no upsert, so delete then insert: a node upgrade that changes a
	// stdlib file would otherwise leave the old body matching forever.
	if _, err := tx.Exec(
		`DELETE FROM code_index WHERE network = ? AND package_path = ? AND file_name = ?`,
		network, pkgPath, fileName); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO code_index (network, package_path, file_name, body) VALUES (?, ?, ?, ?)`,
		network, pkgPath, fileName, body); err != nil {
		return err
	}
	return tx.Commit()
}

// StdlibPackages lists the stdlib package paths held for a network.
func (d *DB) StdlibPackages(network string) ([]string, error) {
	rows, err := d.db.Query(
		`SELECT DISTINCT package_path FROM stdlib_files WHERE network = ? ORDER BY package_path`,
		network)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// StdlibPackage returns one stdlib package's files, or nil if it is not held.
func (d *DB) StdlibPackage(network, pkgPath string) ([]StdlibFile, error) {
	rows, err := d.db.Query(
		`SELECT file_name, body FROM stdlib_files
		 WHERE network = ? AND package_path = ? ORDER BY file_name`,
		network, pkgPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StdlibFile
	for rows.Next() {
		var f StdlibFile
		if err := rows.Scan(&f.Name, &f.Body); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// StdlibFileCount reports how many stdlib files are held for a network.
func (d *DB) StdlibFileCount(network string) (int, error) {
	var n int
	err := d.db.QueryRow(
		`SELECT count(*) FROM stdlib_files WHERE network = ?`, network).Scan(&n)
	return n, err
}

// IsStdlibPath reports whether a package path is stdlib rather than on-chain.
//
// The path is the marker and no column is needed: every on-chain package lives
// under the chain's domain (`gno.land/...`), and no stdlib package does
// (`strings`, `chain/banker`, `crypto/sha256`). Verified 2026-09-28 against
// `vm/qpaths`, which returns both sets in one listing: 586 paths, of which 536
// carry the domain prefix and 50 do not.
func IsStdlibPath(path string) bool {
	return !strings.Contains(path, ".land/") && !strings.HasPrefix(path, "gno.land/")
}

// BackfillStdlibIndex puts held stdlib source into the search index if it is
// missing, the same completeness guard the on-chain backfill uses.
func (d *DB) BackfillStdlibIndex(network string) (int, error) {
	var held, indexed int
	if err := d.db.QueryRow(
		`SELECT count(*) FROM stdlib_files WHERE network = ?`, network).Scan(&held); err != nil {
		return 0, err
	}
	if err := d.db.QueryRow(
		`SELECT count(*) FROM code_index WHERE network = ? AND package_path NOT LIKE 'gno.land/%'`,
		network).Scan(&indexed); err != nil {
		return 0, err
	}
	if held == 0 || indexed >= held {
		return 0, nil
	}

	rows, err := d.db.Query(
		`SELECT package_path, file_name, body FROM stdlib_files WHERE network = ?`, network)
	if err != nil {
		return 0, err
	}
	type f struct{ path, name, body string }
	var all []f
	for rows.Next() {
		var x f
		if err := rows.Scan(&x.path, &x.name, &x.body); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, x := range all {
		if err := d.UpsertStdlibFile(network, x.path, x.name, x.body); err != nil {
			return n, fmt.Errorf("reindex %s/%s: %w", x.path, x.name, err)
		}
		n++
	}
	return n, nil
}
