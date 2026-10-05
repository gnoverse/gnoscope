package store

import (
	"database/sql"
	"sort"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/tags"
)

// Code-derived tags, stored.
//
// pkg/tags decides what a package's source earns; this file keeps the answer
// beside the package, network-scoped like every other table. Two writers and
// one rule between them: ReplacePackage computes a package's tags in the same
// transaction as its files, so the tags a reader sees always describe the
// source served beside them, and RefreshPackageTags catches up everything
// written any other way (a database that predates the table, a fixture, a bump
// of tags.Version). Both are idempotent: running either twice writes the same
// rows.
//
// library is the one tag that is not a function of a package's own source: it
// says other packages import this one, so it moves when they are stored.
// SetDependencies recomputes it for every path whose importers it changed,
// and the refresh pass rebuilds it for the whole network.

// TagsStateKey is the sync-state key the refresh pass records the rule set it
// last completed under.
const TagsStateKey = "code_tags_version"

func tagFiles(files []indexer.MemFile) []tags.File {
	out := make([]tags.File, len(files))
	for i, f := range files {
		out[i] = tags.File{Name: f.Name, Body: f.Body}
	}
	return out
}

// writeTagsTx replaces one package's tags: what its source earns, and library
// from what imports it.
func writeTagsTx(tx *sql.Tx, network, path, txHash string, files []tags.File) error {
	if _, err := tx.Exec(`DELETE FROM package_tags WHERE network = ? AND path = ?`, network, path); err != nil {
		return err
	}
	for _, t := range tags.Derive(path, files) {
		if _, err := tx.Exec(`INSERT INTO package_tags (network, path, tag, evidence) VALUES (?, ?, ?, ?)`,
			network, path, t.Tag, t.Why); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO package_tags_state (network, path, tx_hash, rules) VALUES (?, ?, ?, ?)`,
		network, path, txHash, tags.Version); err != nil {
		return err
	}
	return refreshLibraryTx(tx, network, path)
}

// refreshLibraryTx recomputes library for one path from its importers on the
// same network. A path that is not a stored pure package never carries it.
func refreshLibraryTx(tx *sql.Tx, network, path string) error {
	if _, err := tx.Exec(`DELETE FROM package_tags WHERE network = ? AND path = ? AND tag = ?`,
		network, path, tags.Library); err != nil {
		return err
	}
	var isRealm bool
	err := tx.QueryRow(`SELECT is_realm FROM packages WHERE network = ? AND path = ?`, network, path).Scan(&isRealm)
	if err == sql.ErrNoRows || isRealm {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT package_path FROM dependencies WHERE network = ? AND import_path = ?
		ORDER BY package_path`, network, path)
	if err != nil {
		return err
	}
	var deps []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		deps = append(deps, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	t, ok := tags.LibraryTag(path, deps)
	if !ok {
		return nil
	}
	_, err = tx.Exec(`INSERT INTO package_tags (network, path, tag, evidence) VALUES (?, ?, ?, ?)`,
		network, path, t.Tag, t.Why)
	return err
}

// TagsRefresh is what one refresh pass did.
type TagsRefresh struct {
	// Packages is how many packages had their tags recomputed, Removed how
	// many no longer exist and lost theirs.
	Packages, Removed int
}

// RefreshPackageTags recomputes the tags of every package whose stored source
// or rule set is not the one its tags were computed from, then rebuilds
// library across every network and drops the tags of packages that are gone.
//
// Cheap when nothing moved: one join over packages and package_tags_state
// that reads no source. Idempotent, and safe to interrupt: each package is
// written in its own transaction with its state row, so a pass that stops
// half way leaves the rest to the next one.
func (d *DB) RefreshPackageTags() (TagsRefresh, error) {
	var res TagsRefresh
	type cand struct{ network, path, tx string }
	d.mu.RLock()
	rows, err := d.db.Query(`
		SELECT p.network, p.path, p.tx_hash FROM packages p
		LEFT JOIN package_tags_state s ON s.network = p.network AND s.path = p.path
		WHERE (s.path IS NULL OR s.tx_hash <> p.tx_hash OR s.rules <> ?)
		  AND EXISTS (SELECT 1 FROM package_files f WHERE f.network = p.network AND f.package_path = p.path)
		ORDER BY p.network, p.path`, tags.Version)
	if err != nil {
		d.mu.RUnlock()
		return res, err
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.network, &c.path, &c.tx); err != nil {
			rows.Close()
			d.mu.RUnlock()
			return res, err
		}
		cands = append(cands, c)
	}
	err = rows.Err()
	rows.Close()
	d.mu.RUnlock()
	if err != nil {
		return res, err
	}

	for _, c := range cands {
		files, err := d.StoredPackageFiles(c.network, c.path)
		if err != nil {
			return res, err
		}
		if err := d.inWriteTx(func(tx *sql.Tx) error {
			return writeTagsTx(tx, c.network, c.path, c.tx, tagFiles(files))
		}); err != nil {
			return res, err
		}
		res.Packages++
	}

	err = d.inWriteTx(func(tx *sql.Tx) error {
		// Packages that are gone.
		r, err := tx.Exec(`DELETE FROM package_tags_state WHERE NOT EXISTS (
			SELECT 1 FROM packages p WHERE p.network = package_tags_state.network AND p.path = package_tags_state.path)`)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		res.Removed = int(n)
		if _, err := tx.Exec(`DELETE FROM package_tags WHERE NOT EXISTS (
			SELECT 1 FROM packages p WHERE p.network = package_tags.network AND p.path = package_tags.path)`); err != nil {
			return err
		}
		// library, whole: importers can be written by anything, a backfill
		// of the dependency table included, and the set is small.
		var pure []StoredPackageRef
		rows, err := tx.Query(`SELECT network, path FROM packages WHERE is_realm = 0`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p StoredPackageRef
			if err := rows.Scan(&p.Network, &p.Path); err != nil {
				rows.Close()
				return err
			}
			pure = append(pure, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, p := range pure {
			if err := refreshLibraryTx(tx, p.Network, p.Path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, d.SetSyncState(TagsStateKey, tags.Version)
}

func (d *DB) inWriteTx(fn func(*sql.Tx) error) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// PackageTags returns the tags of the given paths on one network, in the
// table's order. No paths means every package on the network.
func (d *DB) PackageTags(network string, paths []string) (map[string][]tags.Tag, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := map[string][]tags.Tag{}
	q := `SELECT path, tag, evidence FROM package_tags WHERE network = ?`
	args := []any{network}
	if len(paths) > 0 {
		q += ` AND path IN (` + strings.TrimSuffix(strings.Repeat("?,", len(paths)), ",") + `)`
		for _, p := range paths {
			args = append(args, p)
		}
	}
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var t tags.Tag
		if err := rows.Scan(&path, &t.Tag, &t.Why); err != nil {
			return nil, err
		}
		out[path] = append(out[path], t)
	}
	for _, ts := range out {
		tags.Sort(ts)
	}
	return out, rows.Err()
}

// TagCount is how many packages on a network carry a tag.
type TagCount struct {
	Tag      string `json:"tag"`
	Packages int    `json:"packages"`
	Realms   int    `json:"realms"`
	Pure     int    `json:"pure"`
}

// TagCounts counts every tag on one network over the packages the filter
// keeps, ignoring its own tag: a control has to keep showing every tag after
// one is picked. In the table's order, tags nothing carries left out.
func (d *DB) TagCounts(network string, f PackageFilter) ([]TagCount, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	f.Tag = ""
	where, args := f.where("p")
	rows, err := d.db.Query(`
		SELECT t.tag, COUNT(*), COALESCE(SUM(p.is_realm = 1), 0), COALESCE(SUM(p.is_realm = 0), 0)
		FROM package_tags t JOIN packages p ON p.network = t.network AND p.path = t.path
		WHERE `+where+` AND t.network = ?
		GROUP BY t.tag`, append(args, network)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var c TagCount
		if err := rows.Scan(&c.Tag, &c.Packages, &c.Realms, &c.Pure); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return tags.Rank(out[i].Tag) < tags.Rank(out[j].Tag) })
	return out, rows.Err()
}
