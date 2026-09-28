package store

import (
	"database/sql"
	"errors"
	"log"

	"github.com/moul/mygnoscan/pkg/gnoaddr"
)

// The address -> package index, kept in the database rather than rebuilt per
// request.
//
// pkg/gnoaddr answers path -> address. This answers address -> path, which is
// the direction every reader actually asks in: an explorer shows an account and
// has to say whether it is a person or a realm's treasury. Nothing on chain can
// tell them apart, because a package account is a truncated SHA-256 of its path
// and the preimage is never stored.
//
// It used to be built ad hoc, once per request, by /api/pulse. That works at a
// few hundred paths and is the wrong shape for anything that wants to *join*
// against it, which is what the rich list, the flows table and the address page
// all want. A table costs two rows per deploy and makes all three a lookup.

// PackageAccount is one derived account and the package it belongs to.
type PackageAccount struct {
	Address string `json:"address"`
	Path    string `json:"path"`
	// Deposit distinguishes the storage deposit account from the banker. They
	// are different accounts holding different money and conflating them
	// attributes a deposit refund to a realm's treasury.
	Deposit bool `json:"deposit"`
}

// upsertPackageAccounts writes the two rows a path owns.
//
// Caller holds writeMu. A run path (gno.land/e/<g1…>/run) is skipped: its
// "address" is the caller's own, so a row for it would label a human's account
// with a realm path.
func (d *DB) upsertPackageAccounts(network, path string) error {
	if path == "" || gnoaddr.IsRunPath(path) {
		return nil
	}
	accounts := []PackageAccount{
		{Address: gnoaddr.Derive(path), Path: path},
		{Address: gnoaddr.DeriveStorageDeposit(path), Path: path, Deposit: true},
	}
	for _, pa := range accounts {
		// Empty for a path with no account of its own: a bare std library name
		// with no domain. See gnoaddr.Derive.
		if pa.Address == "" {
			continue
		}
		// REPLACE, not IGNORE: the address is a hash of the path, so two paths
		// colliding is not a case to design for, but a row left over from a
		// bad write must not outlive the correction.
		if _, err := d.db.Exec(`
			INSERT OR REPLACE INTO package_accounts (network, address, path, deposit)
			VALUES (?, ?, ?, ?)
		`, network, pa.Address, pa.Path, pa.Deposit); err != nil {
			return err
		}
	}
	return nil
}

// RefreshPackageAccounts derives the accounts of every package the database
// holds and stores any that are missing.
//
// UpsertPackage already writes them as it goes, which keeps the table in step
// on a live syncer. This is the backfill: it is what fills the table on a
// database that predates it, and what repairs a path whose second statement
// failed. Cheap enough to run on every rollup tick — two hashes per path, a few
// hundred paths per chain — so it is not worth a dirty flag to skip it.
//
// Returns the number of paths processed, which is the figure worth logging: the
// number of rows *written* is the same on every pass because the write is a
// REPLACE, so it would say nothing about whether anything was missing.
func (d *DB) RefreshPackageAccounts() (int, error) {
	d.mu.RLock()
	rows, err := d.db.Query(`SELECT network, path FROM packages`)
	if err != nil {
		d.mu.RUnlock()
		return 0, err
	}
	type pkg struct{ network, path string }
	var pkgs []pkg
	for rows.Next() {
		var p pkg
		if err := rows.Scan(&p.network, &p.path); err != nil {
			rows.Close()
			d.mu.RUnlock()
			return 0, err
		}
		pkgs = append(pkgs, p)
	}
	err = rows.Err()
	rows.Close()
	d.mu.RUnlock()
	if err != nil {
		return 0, err
	}

	// Collected first, written second. Holding the read lock across the writes
	// would nest two locks that every other path takes independently.
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	for i, p := range pkgs {
		if err := d.upsertPackageAccounts(p.network, p.path); err != nil {
			return i, err
		}
	}
	return len(pkgs), nil
}

// ensurePackageAccounts builds the index on first use for a network that has
// packages but no derived accounts, and does so at most once per network.
//
// UpsertPackage keeps the table in step for anything the syncer writes, and the
// rollup tick backfills the rest. Neither covers the window this closes: a
// database that already had its packages when this code first ran answers every
// lookup with "no" until that tick fires. The cost of being wrong there is not
// an empty field, it is a realm's treasury rendered as an anonymous hash on the
// page somebody opened to identify it.
//
// Guarded on the count rather than on a flag alone, so a chain that genuinely
// has no packages is not rebuilt on every request, and a network that has since
// been filled by UpsertPackage never enters here at all.
func (d *DB) ensurePackageAccounts(network string) {
	d.pkgAccountsMu.Lock()
	defer d.pkgAccountsMu.Unlock()
	if d.pkgAccountsBuilt[network] {
		return
	}
	// Recorded before the work, not after: a build that fails must not be
	// retried on every subsequent request, and the rollup tick will try again.
	if d.pkgAccountsBuilt == nil {
		d.pkgAccountsBuilt = map[string]bool{}
	}
	d.pkgAccountsBuilt[network] = true

	if n, err := d.PackageAccountCount(network); err != nil || n > 0 {
		return
	}
	if _, err := d.RefreshPackageAccounts(); err != nil {
		log.Printf("package_accounts: first-use build: %v", err)
	}
}

// LookupPackageAccount answers "which package owns this address".
//
// The boolean is false for an address that belongs to no package this database
// knows, which on a synced chain reads as a person. It is deliberately not
// reported as "this is a person": a package the syncer has not reached yet
// looks identical, and claiming certainty there is how an explorer tells a
// reader that gnoswap's pool is somebody's wallet.
func (d *DB) LookupPackageAccount(network, address string) (PackageAccount, bool, error) {
	if address == "" {
		return PackageAccount{}, false, nil
	}
	d.ensurePackageAccounts(network)
	d.mu.RLock()
	defer d.mu.RUnlock()
	var pa PackageAccount
	err := d.db.QueryRow(`
		SELECT address, path, deposit FROM package_accounts
		 WHERE address = ? AND `+d.networkFilter("network", network)+`
		 ORDER BY network LIMIT 1
	`, address).Scan(&pa.Address, &pa.Path, &pa.Deposit)
	if errors.Is(err, sql.ErrNoRows) {
		return PackageAccount{}, false, nil
	}
	if err != nil {
		return PackageAccount{}, false, err
	}
	return pa, true, nil
}

// PackageAccounts is the whole index for a network, for callers that resolve a
// page full of addresses at once and want the path rather than a label.
//
// This is what /api/pulse's flows table reads. It used to rebuild the mapping
// per request by deriving every known path forward, which was correct and is
// now redundant: the table is written by UpsertPackage, so it is as live as the
// derivation was and one query replaces a query plus a thousand hashes.
func (d *DB) PackageAccounts(network string) (map[string]PackageAccount, error) {
	d.ensurePackageAccounts(network)
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`
		SELECT address, path, deposit FROM package_accounts
		 WHERE ` + d.networkFilter("network", network))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PackageAccount{}
	for rows.Next() {
		var pa PackageAccount
		if err := rows.Scan(&pa.Address, &pa.Path, &pa.Deposit); err != nil {
			return nil, err
		}
		out[pa.Address] = pa
	}
	return out, rows.Err()
}

// PackageAccountLabels is the whole index as a label map, for the endpoints
// that resolve a page full of addresses at once.
//
// Kind is "derived" because that is exactly what it is: proved from chain data
// by recomputing the derivation, not a human's assertion and not a guess. Why
// carries the rule, which is what makes the claim checkable.
func (d *DB) PackageAccountLabels(network string) (map[string]AddressLabel, error) {
	d.ensurePackageAccounts(network)
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`
		SELECT address, path, deposit FROM package_accounts
		 WHERE ` + d.networkFilter("network", network))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AddressLabel{}
	for rows.Next() {
		var pa PackageAccount
		if err := rows.Scan(&pa.Address, &pa.Path, &pa.Deposit); err != nil {
			return nil, err
		}
		// Terse on purpose. This map is two rows per deployed package and it is
		// fetched on the first page load, so a sentence of prose per entry is
		// 40KB of payload for a tooltip nobody reads twice: 268KB -> 228KB raw
		// on mainnet's 542 packages, measured 2026-09-28. The address page
		// carries the full explanation, preimage included.
		label, why := pa.Path, "derived: the account of "+pa.Path
		if pa.Deposit {
			label = pa.Path + " (storage deposit)"
			why = "derived: the storage deposit account of " + pa.Path + ", not its treasury"
		}
		out[pa.Address] = AddressLabel{Label: label, Kind: "derived", Why: why}
	}
	return out, rows.Err()
}

// PackageAccountCount is how many accounts the index resolves, for callers that
// want to say so.
func (d *DB) PackageAccountCount(network string) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM package_accounts WHERE ` +
		d.networkFilter("network", network)).Scan(&n)
	return n, err
}
