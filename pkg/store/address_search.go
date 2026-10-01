package store

import (
	"regexp"
	"strings"
)

// Address autocomplete: the addresses this instance knows, by prefix.
//
// Typing part of an address used to answer nothing at all. The search box
// recognised a complete 40-character g1… by shape and offered it as a
// destination, and /api/search matches package paths, names and creators, so a
// prefix of an account that never deployed fell between the two. Measured on
// 2026-10-01: `g1qyfled5ulf6wmu`, the first 15 characters of a mainnet account
// with 774 transactions and ~709k GNOT, returned an empty array, which reads as
// "no such account" rather than "this box does not search that".
//
// Read from `balances`, not from knownAddressesQuery. The union is the
// definition of "every address seen", but it is thirteen scans over the message
// and transfer tables, and this runs on a keystroke. The sweep keeps balances
// within a couple of rows of the union (4,859 of 4,861 on mainnet the same
// day), its primary key is (network, address), so a prefix is one index range
// scan, and it carries the balance the results are ranked by. An address seen
// but not yet swept is missing for one sweep, which is the price of that.

// AddressMatch is one address a prefix matched.
type AddressMatch struct {
	Network string `json:"network"`
	Address string `json:"address"`
	// Ugnot is the cached balance, for ranking and for telling two matches
	// apart: a short prefix matches several accounts, and the one the reader
	// meant is usually the one with money in it.
	Ugnot int64 `json:"ugnot"`
	// Name is the address's current r/sys/users registration, when it has
	// one. A previous name or a tombstone is not what it is called now.
	Name string `json:"name,omitempty"`
}

// addressPrefixRe is what may be a prefix of a gno address: the hrp, the
// separator, and at least two characters of data. "g1" alone would match every
// account on the chain, and a page of them ranked by balance is the rich list,
// which has its own page.
var addressPrefixRe = regexp.MustCompile(`^g1[0-9a-z]{2,38}$`)

// IsAddressPrefix reports whether q is shaped like the start of an address.
func IsAddressPrefix(q string) bool {
	return addressPrefixRe.MatchString(strings.ToLower(strings.TrimSpace(q)))
}

// SearchAddressPrefix returns the known addresses starting with prefix, richest
// first. Anything not shaped like an address prefix answers empty rather than
// erroring: the search box asks on every keystroke and most of them are names.
func (d *DB) SearchAddressPrefix(network, prefix string, limit int) ([]AddressMatch, error) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if !addressPrefixRe.MatchString(prefix) {
		return []AddressMatch{}, nil
	}
	if limit <= 0 {
		limit = 8
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	// A range, not LIKE. SQLite's LIKE is case-insensitive by default and so
	// cannot use the primary key, which would turn this into a scan of the
	// table on every keystroke. Every address character is [0-9a-z] and '{'
	// sorts directly after 'z', so [prefix, prefix+"{") is exactly the set of
	// strings that start with prefix.
	rows, err := d.db.Query(`
		SELECT b.network, b.address, b.ugnot,
		       COALESCE((SELECT u.name FROM users u
		                  WHERE u.network = b.network AND u.address = b.address
		                    AND u.alias = 0 AND u.deleted = 0
		                  ORDER BY u.block_height DESC LIMIT 1), '')
		  FROM balances b
		 WHERE b.address >= ? AND b.address < ?
		   AND `+d.networkFilter("b.network", network)+`
		 ORDER BY b.ugnot DESC, b.address ASC, b.network ASC
		 LIMIT ?`,
		prefix, prefix+"{", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AddressMatch{}
	for rows.Next() {
		var m AddressMatch
		if err := rows.Scan(&m.Network, &m.Address, &m.Ugnot, &m.Name); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
