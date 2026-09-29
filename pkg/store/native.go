package store

import (
	"database/sql"
	"sort"
	"time"
)

// The chain's own coin, presented as an asset.
//
// ugnot is not a GRC20 and never will be: it has no realm, emits no Transfer
// event, and its ledger is `bank_sends` rather than `token_transfers`. But to a
// reader it is the same kind of thing as GNS, and until now this explorer made
// them look like different subjects with different pages, so comparing them
// meant holding two mental models at once.
//
// This file gives ugnot the same shape every other asset has, so one set of
// components can draw both. What it does NOT do is paper over the two places
// they genuinely differ, because both are load-bearing and they point in
// opposite directions:
//
//  1. **Holders come from a sweep, not a replay.** A GRC20's holder list is
//     reconstructed from every Transfer event, so it is exact inside the ledger
//     window and blind before it. ugnot's comes from `balances`, which holds
//     whatever addresses this indexer has read a balance for, because gno
//     offers no way to enumerate accounts. So the GRC20 figure is incomplete at
//     the START and the native figure is incomplete at the TOP, and a reader
//     ranking the two against each other gets a wrong answer that looks right.
//
//  2. **Transfers count BankMsgSend only.** ugnot that a realm moves inside a
//     call is not a bank send and is not in this ledger; it surfaces as gas and
//     as realm activity instead. A GRC20's count includes every realm-internal
//     move. So the native figure under-counts movement and the GRC20 figure
//     does not.
//
// Every summary this file returns carries those two facts as fields rather than
// as prose somewhere else, so the API layer cannot forget to say them.

// NativeKey is the asset key for the chain's own coin.
//
// A bare denom rather than a path, because that is what the chain calls it and
// there is no realm to name. It cannot collide with a GRC20 event key, which
// always contains a "/" when it has a realm at all, and the two mainnet tokens
// that emit a bare symbol emit `COVID` and `META` rather than a denom.
const NativeKey = "ugnot"

// Asset kinds. The three ledgers this explorer knows about, named so a row can
// say which one it came from and a reader can tell why two figures in the same
// column are not comparable.
const (
	KindNative = "native"
	KindGRC20  = "grc20"
	KindGRC721 = "grc721"
)

// NativeStats is the native coin rendered as an asset summary.
//
// Deliberately NOT a TokenSummary: the fields that do not apply would have to be
// filled with something, and a zero in `minted` for a coin that has no mint
// event is a claim. The API layer projects both into one row and marks the
// difference.
type NativeStats struct {
	Network string `json:"network"`
	// Supply and Locked come from the indexer's own supply read, not from this
	// ledger. ugnot is the only asset on the chain where circulating and fully
	// diluted genuinely differ: 83.2% of it was locked when measured
	// 2026-09-29, which is why its market capitalisation is a seventh of its
	// fully diluted value. No GRC20 here has that distinction.
	Supply      int64 `json:"supply"`
	Locked      int64 `json:"locked"`
	SupplyKnown bool  `json:"supply_known"`

	Transfers    int   `json:"transfers"`
	Transfers24h int   `json:"transfers_24h"`
	Volume       int64 `json:"volume"`

	// Holders is how many swept addresses hold a positive balance, and
	// HoldersSwept how many were read at all. The pair is the honest form:
	// alone, the first reads as a total.
	Holders       int `json:"holders"`
	HoldersSwept  int `json:"holders_swept"`
	UniqueSenders int `json:"unique_senders"`

	FirstSeenTime string `json:"first_seen_time,omitempty"`
	LastSeenTime  string `json:"last_seen_time,omitempty"`
	FirstBlock    int    `json:"first_block"`
	LastBlock     int    `json:"last_block"`
}

// NativeSummary builds the native coin's asset row.
//
// Supply is not read here: it comes from a live chain call the API layer already
// makes for the storage map, so it is passed in rather than fetched twice.
func (d *DB) NativeSummary(network string) (NativeStats, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	s := NativeStats{Network: network}

	// Successful sends only. A failed BankMsgSend moved nothing, and counting
	// it would make the volume figure describe attempts rather than transfers.
	err := d.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(ugnot_amount), 0),
		       COALESCE(MIN(block_time), ''), COALESCE(MAX(block_time), ''),
		       COALESCE(MIN(block_height), 0), COALESCE(MAX(block_height), 0),
		       COUNT(DISTINCT from_address)
		  FROM bank_sends
		 WHERE network = ? AND success = 1`, network).Scan(
		&s.Transfers, &s.Volume, &s.FirstSeenTime, &s.LastSeenTime,
		&s.FirstBlock, &s.LastBlock, &s.UniqueSenders)
	if err != nil && err != sql.ErrNoRows {
		return s, err
	}

	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	if err := d.db.QueryRow(`
		SELECT COUNT(*) FROM bank_sends
		 WHERE network = ? AND success = 1 AND block_time >= ?`,
		network, cutoff).Scan(&s.Transfers24h); err != nil && err != sql.ErrNoRows {
		return s, err
	}

	if err := d.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN ugnot > 0 THEN 1 ELSE 0 END), 0)
		  FROM balances WHERE network = ?`, network).Scan(
		&s.HoldersSwept, &s.Holders); err != nil && err != sql.ErrNoRows {
		return s, err
	}
	return s, nil
}

// NativeTopHolders ranks swept addresses by balance.
//
// The counterpart of TopHolders, and the one place the two are least alike: this
// is a leaderboard over a sample, not over a population. Whoever calls it has to
// say so, which is why NativeStats carries HoldersSwept beside Holders.
func (d *DB) NativeTopHolders(network string, limit int) ([]TokenHolder, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT address, ugnot FROM balances
		 WHERE network = ? AND ugnot > 0
		 ORDER BY ugnot DESC LIMIT ?`, network, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TokenHolder{}
	for rows.Next() {
		var h TokenHolder
		if err := rows.Scan(&h.Address, &h.Balance); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// NativeTransfers returns the most recent bank sends, shaped like a token
// transfer so one table component draws both.
//
// An empty From or To means something different here than it does for a GRC20:
// there it is a mint or a burn, here it cannot happen, because a bank send
// always has both ends. Nothing downstream should read these as mints.
func (d *DB) NativeTransfers(network string, limit int) ([]TokenTransfer, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT from_address, to_address, COALESCE(ugnot_amount, 0),
		       tx_hash, block_height, COALESCE(block_time, '')
		  FROM bank_sends
		 WHERE network = ? AND success = 1
		 ORDER BY block_height DESC LIMIT ?`, network, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TokenTransfer{}
	for rows.Next() {
		t := TokenTransfer{Token: NativeKey, Network: network}
		if err := rows.Scan(&t.From, &t.To, &t.Value,
			&t.TxHash, &t.BlockHeight, &t.BlockTime); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// NativeFlowOverTime returns daily transfer count and volume.
//
// Returns TokenFlowPoint, the same type the GRC20 series uses, rather than a
// native-shaped twin: the whole reason this file exists is that one set of
// components should draw either ledger, and two identical structs would have
// forced a conversion at every call site for no information gained.
//
// Volume rather than supply, and that is the point: a GRC20's chart shows supply
// because minting is what moves it, and ugnot's supply is set by the chain
// rather than by this ledger. What `bank_sends` can honestly draw is how much
// moved and how many addresses moved it.
func (d *DB) NativeFlowOverTime(network string, days int) ([]TokenFlowPoint, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	cutoff := ""
	if days > 0 {
		cutoff = time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	}
	rows, err := d.db.Query(`
		SELECT substr(block_time, 1, 10) AS day,
		       COUNT(*), COALESCE(SUM(ugnot_amount), 0), COUNT(DISTINCT from_address)
		  FROM bank_sends
		 WHERE network = ? AND success = 1
		   AND block_time <> '' AND substr(block_time, 1, 10) >= ?
		 GROUP BY day ORDER BY day ASC`, network, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TokenFlowPoint{}
	for rows.Next() {
		var p TokenFlowPoint
		if err := rows.Scan(&p.Time, &p.Transfers, &p.Volume, &p.Senders); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TokenFlowPoint is one day of one GRC20's movement, the counterpart of
// NativeFlowPoint so the same chart draws either.
type TokenFlowPoint struct {
	Time      string `json:"time"`
	Transfers int    `json:"transfers"`
	Volume    int64  `json:"volume"`
	Senders   int    `json:"senders"`
}

// TokenFlowOverTime returns daily transfer count and volume for one GRC20.
//
// Mints and burns are excluded from the volume, and kept in the count. A mint is
// a transfer for the purpose of "how busy is this token" and is not movement
// between holders, so summing it into volume would make an issuance look like
// trading. TokenSupplyOverTime is where mints belong and it already draws them.
func (d *DB) TokenFlowOverTime(network, token string, days int) ([]TokenFlowPoint, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	cutoff := ""
	if days > 0 {
		cutoff = time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	}
	rows, err := d.db.Query(`
		SELECT substr(block_time, 1, 10) AS day,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN from_addr <> '' AND to_addr <> '' THEN value ELSE 0 END), 0),
		       COUNT(DISTINCT from_addr)
		  FROM token_transfers
		 WHERE network = ? AND token = ?
		   AND block_time <> '' AND substr(block_time, 1, 10) >= ?
		 GROUP BY day ORDER BY day ASC`, network, token, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TokenFlowPoint{}
	for rows.Next() {
		var p TokenFlowPoint
		if err := rows.Scan(&p.Time, &p.Transfers, &p.Volume, &p.Senders); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AssetActivityPoint is one day of movement across every asset, split by kind.
//
// Split rather than summed, because the three counts are not the same
// measurement: the native one is BankMsgSend only and the GRC20 one includes
// realm-internal moves, so a single "transfers today" line would add two
// differently-defined numbers and present the total as a fact. Drawn as three
// series, a reader can see which is which.
type AssetActivityPoint struct {
	Time string `json:"time"`
	// NativeTransfers and NativeVolume come from bank_sends.
	NativeTransfers int   `json:"native_transfers"`
	NativeVolume    int64 `json:"native_volume"`
	// GRC20Transfers and GRC721Transfers come from token_transfers, separated
	// by whether the day's rows carried an amount at all.
	GRC20Transfers  int `json:"grc20_transfers"`
	GRC721Transfers int `json:"grc721_transfers"`
	// ActiveAssets is how many distinct assets moved that day, which is the one
	// number on this chart that IS comparable across kinds.
	ActiveAssets int `json:"active_assets"`
}

// AssetActivityOverTime is the defi home's chart: daily movement across the
// whole chain, by kind.
//
// Two queries rather than a union, because the two ledgers live in different
// tables with different columns and different meanings for an empty address.
// Merged by day in Go, which also lets a day that exists in one and not the
// other come back with a zero rather than being absent from the series.
func (d *DB) AssetActivityOverTime(network string, days int) ([]AssetActivityPoint, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	cutoff := ""
	if days > 0 {
		cutoff = time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	}
	byDay := map[string]*AssetActivityPoint{}
	at := func(day string) *AssetActivityPoint {
		p, ok := byDay[day]
		if !ok {
			p = &AssetActivityPoint{Time: day}
			byDay[day] = p
		}
		return p
	}

	nat, err := d.db.Query(`
		SELECT substr(block_time, 1, 10) AS day, COUNT(*), COALESCE(SUM(ugnot_amount), 0)
		  FROM bank_sends
		 WHERE network = ? AND success = 1
		   AND block_time <> '' AND substr(block_time, 1, 10) >= ?
		 GROUP BY day`, network, cutoff)
	if err != nil {
		return nil, err
	}
	for nat.Next() {
		var day string
		var n int
		var vol int64
		if err := nat.Scan(&day, &n, &vol); err != nil {
			nat.Close()
			return nil, err
		}
		p := at(day)
		p.NativeTransfers, p.NativeVolume = n, vol
	}
	nat.Close()
	if err := nat.Err(); err != nil {
		return nil, err
	}

	// A token is non-fungible when none of its transfers ever carried an
	// amount, which is a property of the TOKEN and not of the day: a
	// collection whose only transfer today happens to be amountless is still
	// whatever it has always been. So fungibility is decided over the token's
	// whole history and joined back, rather than read off the day's rows.
	tok, err := d.db.Query(`
		WITH kinds AS (
			SELECT token, MAX(value) > 0 AS fungible
			  FROM token_transfers WHERE network = ?1 GROUP BY token
		)
		SELECT substr(t.block_time, 1, 10) AS day,
		       COALESCE(SUM(CASE WHEN k.fungible THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN k.fungible THEN 0 ELSE 1 END), 0),
		       COUNT(DISTINCT t.token)
		  FROM token_transfers t JOIN kinds k ON k.token = t.token
		 WHERE t.network = ?1
		   AND t.block_time <> '' AND substr(t.block_time, 1, 10) >= ?2
		 GROUP BY day`, network, cutoff)
	if err != nil {
		return nil, err
	}
	for tok.Next() {
		var day string
		var fung, nonFung, active int
		if err := tok.Scan(&day, &fung, &nonFung, &active); err != nil {
			tok.Close()
			return nil, err
		}
		p := at(day)
		p.GRC20Transfers, p.GRC721Transfers, p.ActiveAssets = fung, nonFung, active
	}
	tok.Close()
	if err := tok.Err(); err != nil {
		return nil, err
	}

	// The native coin counts as an active asset on any day it moved.
	out := make([]AssetActivityPoint, 0, len(byDay))
	for _, p := range byDay {
		if p.NativeTransfers > 0 {
			p.ActiveAssets++
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}
