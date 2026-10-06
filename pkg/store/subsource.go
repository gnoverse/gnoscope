package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/gnoverse/gnoscope/pkg/indexer"
)

// The source of every submission, beside the current one in package_files.
// See blobs and submission_files in schema.go for why both exist.

// BlobHash is the content address of one file body: lowercase hex sha256.
func BlobHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// insertSubmissionFilesTx writes one submission's files and their bodies.
// INSERT OR IGNORE on both: a blob already stored is the same bytes by
// construction, and a file already recorded for this message is the same file
// (a message's files cannot change once it is in a block).
func insertSubmissionFilesTx(tx *sql.Tx, network, txHash string, msgIndex int, files []indexer.MemFile) error {
	for _, f := range files {
		h := BlobHash(f.Body)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO blobs (hash, body) VALUES (?, ?)`, h, f.Body); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO submission_files (network, tx_hash, msg_index, file_name, hash)
			VALUES (?, ?, ?, ?, ?)`, network, txHash, msgIndex, f.Name, h); err != nil {
			return err
		}
	}
	return nil
}

// RecordSubmission records one MsgAddPackage, its row in package_submissions
// and its files, in one transaction.
//
// One transaction for the same reason ReplacePackage is one: the backfill
// finds the submissions it still has to fetch by looking for a row without
// files, and a row written without its files by a crash in between would be
// fetched again, which is harmless; the reverse, files with no row, would
// never be read by anything. Written for every submission, failed or not.
func (d *DB) RecordSubmission(network, txHash string, msgIndex int, path, name, creator string, blockHeight int, blockTime string, isRealm bool, send string, success bool, files []indexer.MemFile) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO package_submissions
			(network, tx_hash, msg_index, path, name, creator, block_height, block_time, is_realm, num_files, send, success)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, network, txHash, msgIndex, path, name, creator, blockHeight, blockTime, isRealm, len(files), send, success); err != nil {
		return err
	}
	if err := insertSubmissionFilesTx(tx, network, txHash, msgIndex, files); err != nil {
		return err
	}
	return tx.Commit()
}

// AddSubmissionFiles stores the files of a submission already recorded,
// which is the backfill's write. It reports false, and writes nothing, when
// no submission (network, txHash, msgIndex) at path exists: the backfill
// fetches whole height ranges, and a message this database never recorded is
// not its to invent.
func (d *DB) AddSubmissionFiles(network, txHash string, msgIndex int, path string, files []indexer.MemFile) (bool, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck

	var one int
	err = tx.QueryRow(`SELECT 1 FROM package_submissions WHERE network = ? AND tx_hash = ? AND msg_index = ? AND path = ?`,
		network, txHash, msgIndex, path).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := insertSubmissionFilesTx(tx, network, txHash, msgIndex, files); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// SubmissionRef names one MsgAddPackage.
type SubmissionRef struct {
	TxHash   string
	MsgIndex int
	Path     string
	Height   int
}

// SubmissionsMissingFiles lists, oldest first, up to limit submissions above
// afterHeight that have no submission_files rows. -1 starts at genesis.
func (d *DB) SubmissionsMissingFiles(network string, afterHeight, limit int) ([]SubmissionRef, error) {
	rows, err := d.db.Query(`
		SELECT s.tx_hash, s.msg_index, s.path, s.block_height
		FROM package_submissions s
		WHERE s.network = ? AND s.block_height > ?
		  AND NOT EXISTS (SELECT 1 FROM submission_files f
		                  WHERE f.network = s.network AND f.tx_hash = s.tx_hash AND f.msg_index = s.msg_index)
		ORDER BY s.block_height, s.tx_hash, s.msg_index
		LIMIT ?`, network, afterHeight, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubmissionRef
	for rows.Next() {
		var r SubmissionRef
		if err := rows.Scan(&r.TxHash, &r.MsgIndex, &r.Path, &r.Height); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HasSubmissionFiles reports whether one submission's files are stored.
func (d *DB) HasSubmissionFiles(network, txHash string, msgIndex int) (bool, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM submission_files WHERE network = ? AND tx_hash = ? AND msg_index = ?`,
		network, txHash, msgIndex).Scan(&n)
	return n > 0, err
}

// SubmissionFiles returns one submission's files with their bodies, ordered
// by name.
func (d *DB) SubmissionFiles(network, txHash string, msgIndex int) ([]indexer.MemFile, error) {
	rows, err := d.db.Query(`
		SELECT f.file_name, b.body
		FROM submission_files f JOIN blobs b ON b.hash = f.hash
		WHERE f.network = ? AND f.tx_hash = ? AND f.msg_index = ?
		ORDER BY f.file_name`, network, txHash, msgIndex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []indexer.MemFile{}
	for rows.Next() {
		var f indexer.MemFile
		if err := rows.Scan(&f.Name, &f.Body); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SubmissionStorage is what the per-submission source costs on one network.
type SubmissionStorage struct {
	Submissions     int   // package_submissions rows
	WithFiles       int   // of those, with their files stored
	FileRows        int   // submission_files rows
	LogicalBytes    int64 // the sum of every submission's file sizes, as if stored per submission
	BlobBytes       int64 // the bytes actually stored in blobs referenced from this network
	Blobs           int   // distinct bodies referenced from this network
	CurrentBytes    int64 // package_files bytes, for comparison
	CurrentFileRows int
}

// SubmissionStorageStats measures per-submission source on one network.
func (d *DB) SubmissionStorageStats(network string) (SubmissionStorage, error) {
	var s SubmissionStorage
	if err := d.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(EXISTS (SELECT 1 FROM submission_files f
		                            WHERE f.network = s.network AND f.tx_hash = s.tx_hash AND f.msg_index = s.msg_index)), 0)
		FROM package_submissions s WHERE s.network = ?`, network).Scan(&s.Submissions, &s.WithFiles); err != nil {
		return s, err
	}
	if err := d.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(LENGTH(CAST(b.body AS BLOB))), 0)
		FROM submission_files f JOIN blobs b ON b.hash = f.hash
		WHERE f.network = ?`, network).Scan(&s.FileRows, &s.LogicalBytes); err != nil {
		return s, err
	}
	if err := d.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(LENGTH(CAST(body AS BLOB))), 0) FROM blobs
		WHERE hash IN (SELECT hash FROM submission_files WHERE network = ?)`, network).Scan(&s.Blobs, &s.BlobBytes); err != nil {
		return s, err
	}
	err := d.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(LENGTH(CAST(body AS BLOB))), 0) FROM package_files WHERE network = ?`,
		network).Scan(&s.CurrentFileRows, &s.CurrentBytes)
	return s, err
}

// SubmissionInfo is one MsgAddPackage at a path, as the source manifest lists
// it.
type SubmissionInfo struct {
	Height   int    `json:"height"`
	TxHash   string `json:"tx_hash"`
	MsgIndex int    `json:"msg_index"`
	Time     string `json:"time,omitempty"`
	Success  bool   `json:"success"`
	// Files is how many files the message carried.
	Files int `json:"files"`
	// Stored reports whether those files are held here, which is what decides
	// whether `at=<height>` can serve them. False only until the backfill
	// reaches a submission synced before per-submission source existed.
	Stored bool `json:"stored"`
}

// PathSubmissions lists every MsgAddPackage at one path on one network,
// oldest first.
func (d *DB) PathSubmissions(network, path string) ([]SubmissionInfo, error) {
	rows, err := d.db.Query(`
		SELECT s.block_height, s.tx_hash, s.msg_index, COALESCE(s.block_time, ''), s.success, s.num_files,
		       EXISTS (SELECT 1 FROM submission_files f
		               WHERE f.network = s.network AND f.tx_hash = s.tx_hash AND f.msg_index = s.msg_index)
		FROM package_submissions s
		WHERE s.network = ? AND s.path = ?
		ORDER BY s.block_height, s.tx_hash, s.msg_index`, network, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SubmissionInfo{}
	for rows.Next() {
		var s SubmissionInfo
		if err := rows.Scan(&s.Height, &s.TxHash, &s.MsgIndex, &s.Time, &s.Success, &s.Files, &s.Stored); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ErrNoSubmissionSource is returned when no submission at that height has its
// files stored.
var ErrNoSubmissionSource = errors.New("no stored submission at that height")

// SubmissionSource reads the source one submission carried, in the shape
// PackageSource returns, with Stamp naming that submission.
//
// A block can hold more than one MsgAddPackage at a path (two transactions,
// or one multi-message transaction). txHash picks one when given; otherwise a
// successful submission is preferred over a failed one, then the one whose
// tx_hash sorts last, so the choice is stable. The current stamp is never
// ambiguous: PackageSource answers that one from the packages row.
//
// Failed reports that the chain rejected the submission: its bytes are what
// was sent, not what was ever published.
func (d *DB) SubmissionSource(network, path string, height int, txHash string, withBodies bool) (*PackageSource, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	q := `
		SELECT s.tx_hash, s.msg_index, s.name, s.is_realm, COALESCE(s.block_time, ''), s.success
		FROM package_submissions s
		WHERE s.network = ? AND s.path = ? AND s.block_height = ?
		  AND EXISTS (SELECT 1 FROM submission_files f
		              WHERE f.network = s.network AND f.tx_hash = s.tx_hash AND f.msg_index = s.msg_index)`
	args := []any{network, path, height}
	if txHash != "" {
		q += ` AND s.tx_hash = ?`
		args = append(args, txHash)
	}
	q += ` ORDER BY s.success DESC, s.tx_hash DESC, s.msg_index DESC LIMIT 1`

	src := PackageSource{Network: network, Path: path, Files: []SourceFile{}}
	var msgIndex int
	var success bool
	err = tx.QueryRow(q, args...).Scan(&src.Stamp.TxHash, &msgIndex, &src.Name, &src.IsRealm, &src.Stamp.Time, &success)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSubmissionSource
	}
	if err != nil {
		return nil, err
	}
	src.Stamp.Height = height
	src.Failed = !success

	// Same counting as PackageSource: bytes, and a last line without a
	// newline still counts.
	bodyCol := `NULL`
	if withBodies {
		bodyCol = `b.body`
	}
	rows, err := tx.Query(`
		SELECT f.file_name,
		       LENGTH(CAST(b.body AS BLOB)),
		       LENGTH(b.body) - LENGTH(REPLACE(b.body, char(10), ''))
		         + (CASE WHEN b.body != '' AND SUBSTR(b.body, -1) != char(10) THEN 1 ELSE 0 END),
		       `+bodyCol+`
		FROM submission_files f JOIN blobs b ON b.hash = f.hash
		WHERE f.network = ? AND f.tx_hash = ? AND f.msg_index = ?
		ORDER BY f.file_name`, network, src.Stamp.TxHash, msgIndex)
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
	return &src, rows.Err()
}

// RepairCandidate is one path's current state beside the submission that
// should have produced it.
type RepairCandidate struct {
	Path string
	// Current is the packages row's stamp; HasCurrent is false when there is
	// no row.
	HasCurrent bool
	Current    SourceStamp
	// Newest is the newest successful submission at the path. HasNewest is
	// false when every submission there failed.
	HasNewest      bool
	Newest         SubmissionRef
	NewestName     string
	NewestCreator  string
	NewestTime     string
	NewestIsRealm  bool
	NewestNumFiles int
	// NewestStored is whether Newest's files are all held here; a candidate
	// without them cannot be judged and is skipped.
	NewestStored bool
}

// RepairCandidates lists, per path that has submissions on one network, the
// current stamp and the newest successful submission.
//
// Newest is by height, then by tx_hash and msg_index. Within one block the
// order the chain applied two submissions at the same path is not stored, so
// when the packages row names one of the successful submissions at the newest
// height, that one is taken: it is the one the forward sync applied last.
func (d *DB) RepairCandidates(network string) ([]RepairCandidate, error) {
	cur := map[string]SourceStamp{}
	rows, err := d.db.Query(`SELECT path, block_height, tx_hash FROM packages WHERE network = ?`, network)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		var s SourceStamp
		if err := rows.Scan(&p, &s.Height, &s.TxHash); err != nil {
			rows.Close()
			return nil, err
		}
		cur[p] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = d.db.Query(`
		SELECT s.path, s.tx_hash, s.msg_index, s.block_height, s.name, s.creator, COALESCE(s.block_time, ''),
		       s.is_realm, s.num_files, s.success,
		       (SELECT COUNT(*) FROM submission_files f
		         WHERE f.network = s.network AND f.tx_hash = s.tx_hash AND f.msg_index = s.msg_index)
		FROM package_submissions s
		WHERE s.network = ?
		ORDER BY s.path, s.block_height, s.tx_hash, s.msg_index`, network)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byPath := map[string]*RepairCandidate{}
	var order []string
	for rows.Next() {
		var (
			ref                  SubmissionRef
			name, creator, btime string
			isRealm, success     bool
			numFiles, stored     int
		)
		if err := rows.Scan(&ref.Path, &ref.TxHash, &ref.MsgIndex, &ref.Height, &name, &creator, &btime,
			&isRealm, &numFiles, &success, &stored); err != nil {
			return nil, err
		}
		c, ok := byPath[ref.Path]
		if !ok {
			c = &RepairCandidate{Path: ref.Path}
			c.Current, c.HasCurrent = cur[ref.Path]
			byPath[ref.Path] = c
			order = append(order, ref.Path)
		}
		if !success {
			continue
		}
		// Ascending, so a later row replaces an earlier one, except that at
		// the same height the one the packages row names is kept.
		if c.HasNewest && c.Newest.Height == ref.Height && c.HasCurrent &&
			c.Current.Height == ref.Height && c.Current.TxHash == c.Newest.TxHash {
			continue
		}
		c.HasNewest = true
		c.Newest = ref
		c.NewestName, c.NewestCreator, c.NewestTime = name, creator, btime
		c.NewestIsRealm, c.NewestNumFiles = isRealm, numFiles
		c.NewestStored = stored > 0 && stored == numFiles
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(order)
	out := make([]RepairCandidate, 0, len(order))
	for _, p := range order {
		out = append(out, *byPath[p])
	}
	return out, nil
}

// CurrentFileHashes returns the content address of every file package_files
// holds for one path.
func (d *DB) CurrentFileHashes(network, path string) (map[string]string, error) {
	rows, err := d.db.Query(`SELECT file_name, body FROM package_files WHERE network = ? AND package_path = ?`, network, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, body string
		if err := rows.Scan(&name, &body); err != nil {
			return nil, err
		}
		out[name] = BlobHash(body)
	}
	return out, rows.Err()
}

// SubmissionFileHashes returns the content address of every file one
// submission carried.
func (d *DB) SubmissionFileHashes(network, txHash string, msgIndex int) (map[string]string, error) {
	rows, err := d.db.Query(`SELECT file_name, hash FROM submission_files WHERE network = ? AND tx_hash = ? AND msg_index = ?`,
		network, txHash, msgIndex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, h string
		if err := rows.Scan(&name, &h); err != nil {
			return nil, err
		}
		out[name] = h
	}
	return out, rows.Err()
}

// DropCurrentPackage removes one path's current state: its packages row, its
// source, search index and tags. For a path whose every submission failed,
// which a database synced before failed submissions stopped writing current
// state can still carry. Dependencies go through SetDependencies, which also
// recomputes the library tag of what the path imported; symbols follow
// package_files on the symbol pass's own orphan sweep.
func (d *DB) DropCurrentPackage(network, path string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, q := range []string{
		`DELETE FROM packages WHERE network = ? AND path = ?`,
		`DELETE FROM package_files WHERE network = ? AND package_path = ?`,
		`DELETE FROM code_index WHERE network = ? AND package_path = ?`,
		`DELETE FROM package_tags WHERE network = ? AND path = ?`,
		`DELETE FROM package_tags_state WHERE network = ? AND path = ?`,
	} {
		if _, err := tx.Exec(q, network, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}
