package store

import (
	"database/sql"
	"errors"
	"strings"
)

// The source of one package, addressed by the submission that put it there.
//
// package_files holds one version of a package's source: whatever the newest
// successful submission at that path carried. That version is identified by
// the packages row's (block_height, tx_hash), which is what this file calls
// the stamp. The stamp is what lets a reader cache source forever: the bytes
// served under a stamp can never change, because a redeploy moves the stamp.
// Bodies of earlier submissions are not stored, so a stamp that is no longer
// current cannot be answered from here at all.

// SourceStamp identifies the submission whose source package_files holds.
type SourceStamp struct {
	Height int    `json:"height"`
	TxHash string `json:"tx_hash"`
	Time   string `json:"time,omitempty"`
}

// SourceFile is one file of a package. Body is a pointer so a manifest can
// leave it out while a pinned read still distinguishes an empty file from an
// absent body.
type SourceFile struct {
	Name  string  `json:"name"`
	Size  int     `json:"size"`
	Lines int     `json:"lines"`
	Body  *string `json:"body,omitempty"`
}

// PackageSource is the stamp and file list of one package on one network.
type PackageSource struct {
	Network string       `json:"network"`
	Path    string       `json:"path"`
	Name    string       `json:"name"`
	IsRealm bool         `json:"-"`
	Stamp   SourceStamp  `json:"stamp"`
	Files   []SourceFile `json:"files"`
}

// ErrNoSource is returned when the network has no package at the path.
var ErrNoSource = errors.New("package not found")

// PackageSource reads one package's stamp and files, with bodies when asked.
//
// One read transaction, so the stamp and the files come from the same
// snapshot. ReplacePackage writes both in one transaction, and a reader that
// could interleave with it would pin the wrong bytes under a stamp.
//
// network is required: a stamp is a height, and a height means nothing
// without its chain.
func (d *DB) PackageSource(network, path string, withBodies bool) (*PackageSource, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	src := PackageSource{Network: network, Path: path, Files: []SourceFile{}}
	var blockTime sql.NullString
	err = tx.QueryRow(`
		SELECT name, is_realm, block_height, tx_hash, block_time
		FROM packages WHERE network = ? AND path = ?`, network, path).
		Scan(&src.Name, &src.IsRealm, &src.Stamp.Height, &src.Stamp.TxHash, &blockTime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSource
	}
	if err != nil {
		return nil, err
	}
	src.Stamp.Time = blockTime.String

	// Size in bytes, not characters: LENGTH on TEXT counts characters, and a
	// size a client compares against the body it received has to be bytes.
	// Lines are newline count, plus one for a final line with no newline.
	bodyCol := `NULL`
	if withBodies {
		bodyCol = `body`
	}
	rows, err := tx.Query(`
		SELECT file_name,
		       LENGTH(CAST(body AS BLOB)),
		       LENGTH(body) - LENGTH(REPLACE(body, char(10), ''))
		         + (CASE WHEN body != '' AND SUBSTR(body, -1) != char(10) THEN 1 ELSE 0 END),
		       `+bodyCol+`
		FROM package_files
		WHERE network = ? AND package_path = ?
		ORDER BY file_name`, network, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f SourceFile
		var body sql.NullString
		if err := rows.Scan(&f.Name, &f.Size, &f.Lines, &body); err != nil {
			return nil, err
		}
		if withBodies {
			b := body.String
			f.Body = &b
		}
		src.Files = append(src.Files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &src, nil
}

// SourceSibling is another package under the same namespace, with its stamp.
type SourceSibling struct {
	Path  string      `json:"path"`
	Stamp SourceStamp `json:"stamp"`
}

// PackagesUnderNamespace lists every package on one network whose path starts
// with prefix, with its stamp, ordered by path.
//
// The candidate set for version families: a family never crosses a namespace
// (see discover.Generation), so the caller narrows by that and filters the
// rest by family key. prefix ends in "/". A range on path rather than LIKE,
// because a path may contain `_`, which LIKE reads as a wildcard; the upper
// bound swaps that final "/" for "0", the next byte up.
func (d *DB) PackagesUnderNamespace(network, prefix string) ([]SourceSibling, error) {
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	upper := strings.TrimSuffix(prefix, "/") + "0"
	rows, err := d.db.Query(`
		SELECT path, block_height, tx_hash, COALESCE(block_time, '')
		FROM packages
		WHERE network = ? AND path > ? AND path < ?
		ORDER BY path`, network, prefix, upper)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceSibling
	for rows.Next() {
		var s SourceSibling
		if err := rows.Scan(&s.Path, &s.Stamp.Height, &s.Stamp.TxHash, &s.Stamp.Time); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SubmissionCounts is how many MsgAddPackage messages one path has seen on one
// network, and how many of them succeeded.
func (d *DB) SubmissionCounts(network, path string) (total, successful int, err error) {
	err = d.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0)
		FROM package_submissions WHERE network = ? AND path = ?`, network, path).
		Scan(&total, &successful)
	return total, successful, err
}
