package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// discover_events: the Discover feed, built on the rollup tick rather than
// queried per request.
//
// Nothing here is a live query, and that is a decision rather than a caching
// habit. One of the twelve kinds needs two indexer calls and an RPC round trip
// per request, so a page built live would be as slow as its worst kind and
// would answer differently on two refreshes a second apart. Building on the
// tick also gives a feed reader a stable generation to dedupe against, which is
// the difference between "three new items" and "everything again".
//
// Not to be confused with discover.go in this package, which is the /apps
// directory: the chain proposing entries for a curated list. Same word, two
// features, and this one is the event feed.

// DiscoverEvent is one row.
//
// Facts and Layers are the canonical pair and everything a reader sees is
// derived from them. Facts is the closed set of values the SQL produced;
// Layers is what happened, what it means and why it matters, generated from
// Facts and never carrying anything absent from it (pkg/discover enforces that
// mechanically). Headline and Explanation are projections computed on read, so
// there is exactly one writer for the text.
type DiscoverEvent struct {
	Network    string          `json:"network"`
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	At         string          `json:"at"`
	Height     int64           `json:"height"`
	Actor      string          `json:"actor,omitempty"`
	Target     string          `json:"target,omitempty"`
	Namespace  string          `json:"namespace,omitempty"`
	Facts      json.RawMessage `json:"facts"`
	Layers     json.RawMessage `json:"layers"`
	EvidenceTx string          `json:"evidence_tx,omitempty"`
	FirstEver  bool            `json:"first_ever"`
	Reach      int64           `json:"reach"`
	Magnitude  float64         `json:"magnitude"`
	ScoreBase  float64         `json:"score_base"`
	BuiltAt    string          `json:"built_at"`
}

// EventID builds the deterministic id.
//
// <network>/<kind>/<subject>/<ordinal>, where subject is the transaction hash
// when there is one and the thing itself otherwise (an address, a package
// path, a day for a spike). Ordinal separates two events of one kind from one
// transaction: the message index, or 0.
//
// Deterministic and never a sequence, because the id is the only thing a
// consumer can dedupe on. A rebuild of this table has to produce byte-identical
// ids or every feed subscriber re-notifies and every stored pick in the
// publishing pipeline points at a row that no longer exists. That is also why
// nothing here is derived from row order, insertion time or a counter.
//
// The separator is "/" and the subject is not escaped, because a tx hash is
// base64 and a package path contains slashes already: the id is an opaque key
// for equality and never parsed back into its parts. Anything that needs the
// kind or the height reads the column.
func EventID(network, kind, subject string, ordinal int) string {
	return fmt.Sprintf("%s/%s/%s/%d", network, kind, subject, ordinal)
}

// UpsertDiscoverEvents writes events, refreshing the derived half of any row
// that already exists.
//
// The split is between identity and derivation. network, id, kind, at and
// height are what the event *is*, and they are a pure function of the chain, so
// a rebuild reproduces them exactly and there is nothing to update. facts,
// layers and the score are *derived* from source rows and from templates, and
// both of those legitimately change: a template fix, a new score term, a source
// query learning to fill a field it used to leave empty.
//
// This began as DO NOTHING, on the reasoning that the table is append-only.
// That froze the derived half, and it went wrong twice within a day. A layer 1
// template fix left already-stored rows carrying the old wording, and adding
// score_base left 930 rows scoring zero, which would have sorted every one of
// them off the bottom of the page with nothing to say why. Neither is history a
// subscriber read; both are this software's own rendering of a fact that has
// not changed.
//
// Updating is safe for the one contract that matters here: consumers dedupe on
// id, the id is unchanged, so a refreshed body is not a new item and nobody
// re-notifies. What must never change is the id, and nothing here can change
// it.
//
// Returns how many rows were new and how many were refreshed. Both are worth
// logging: a steady stream of refreshes on a tick that should be idempotent
// means a template or a source is not deterministic.
func (d *DB) UpsertDiscoverEvents(events []DiscoverEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO discover_events
			(network, id, kind, at, height, actor, target, namespace,
			 facts, layers, evidence_tx, first_ever, reach, magnitude, score_base, built_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (network, id) DO UPDATE SET
			facts      = excluded.facts,
			layers     = excluded.layers,
			actor      = excluded.actor,
			target     = excluded.target,
			namespace  = excluded.namespace,
			first_ever = excluded.first_ever,
			reach      = excluded.reach,
			magnitude  = excluded.magnitude,
			score_base = excluded.score_base
		WHERE facts      IS NOT excluded.facts
		   OR layers     IS NOT excluded.layers
		   OR score_base IS NOT excluded.score_base
		   OR reach      IS NOT excluded.reach
		   OR magnitude  IS NOT excluded.magnitude
		   OR first_ever IS NOT excluded.first_ever`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, e := range events {
		if e.Network == "" || e.ID == "" || e.At == "" {
			return 0, fmt.Errorf("discover event needs network, id and at: %+v", e)
		}
		res, err := stmt.Exec(e.Network, e.ID, e.Kind, e.At, e.Height, e.Actor, e.Target,
			e.Namespace, jsonOrEmpty(e.Facts), jsonOrEmpty(e.Layers), e.EvidenceTx,
			boolToInt(e.FirstEver), e.Reach, e.Magnitude, e.ScoreBase, e.BuiltAt)
		if err != nil {
			return 0, fmt.Errorf("insert %s: %w", e.ID, err)
		}
		// RowsAffected counts an insert and a real update alike, which is
		// what "this row changed" means here. The WHERE on the conflict
		// clause is what keeps an unchanged row from counting: without it
		// every tick would report 930 writes and the number would stop
		// meaning anything.
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, tx.Commit()
}

// DiscoverQuery is the filter set the feed offers.
//
// Every field is optional. Cursor is the keyset position returned by the
// previous page and is the only way to page: OFFSET over a table that grows at
// the head would skip or repeat rows between two requests, which on a feed
// reads as events vanishing.
type DiscoverQuery struct {
	Network   string
	Kinds     []string
	Namespace string
	Actor     string
	Since     string // inclusive lower bound on at
	Cursor    string // "at|id" from the previous page
	Limit     int
	// ByScore orders the candidate pool by stored score instead of by time.
	//
	// It exists because a ranked page cannot be built from a time-ordered
	// fetch. Capping at the most recent N and then sorting those by score
	// produces "the best of the most recent N", which is a different and
	// quietly wrong answer: measured on mainnet 2026-09-28, a 200-row
	// time-ordered pool contained 11 of the 21 deployer.first events, so half
	// the highest-scoring kind on the chain could not reach the page at all.
	//
	// Cursor paging is not available with this, and the field is deliberately
	// not combined with one: a keyset cursor needs the sort key to be unique
	// and stable, and score_base is neither.
	ByScore bool
}

// DiscoverLimitMax is the hard cap on a page.
//
// A cap rather than a default that callers may raise: this endpoint's rows
// carry two blocks of generated prose each, so an unbounded limit is a
// multi-megabyte response, and the one thing a feed must not do is time out.
const DiscoverLimitMax = 200

// DiscoverEvents returns one page, newest first, with the cursor for the next.
//
// The second return is empty when the page is the last one. A caller that pages
// until it comes back empty reads the whole feed exactly once, with no row seen
// twice and none skipped, even while the tick is inserting at the head.
func (d *DB) DiscoverEvents(q DiscoverQuery) ([]DiscoverEvent, string, error) {
	if q.Network == "" {
		return nil, "", fmt.Errorf("discover is per chain: a network is required")
	}
	if q.Limit <= 0 || q.Limit > DiscoverLimitMax {
		q.Limit = 50
	}

	where := []string{"network = ?"}
	args := []any{q.Network}

	if len(q.Kinds) > 0 {
		where = append(where, "kind IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Kinds)), ",")+")")
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	if q.Namespace != "" {
		where = append(where, "namespace = ?")
		args = append(args, q.Namespace)
	}
	if q.Actor != "" {
		where = append(where, "actor = ?")
		args = append(args, q.Actor)
	}
	if q.Since != "" {
		where = append(where, "at >= ?")
		args = append(args, q.Since)
	}
	// Keyset, on the same (at DESC, id DESC) the index is built for. The tuple
	// comparison is spelled out rather than written as a row value because
	// SQLite's row-value support is newer than the oldest build this has to run
	// on, and a silent fallback to a sort would only show up under load.
	if at, id, ok := splitCursor(q.Cursor); ok {
		where = append(where, "(at < ? OR (at = ? AND id < ?))")
		args = append(args, at, at, id)
	}

	// One more than asked for, so "is there a next page" is answered without a
	// second COUNT over the same predicate.
	args = append(args, q.Limit+1)

	d.mu.RLock()
	defer d.mu.RUnlock()

	order := "at DESC, id DESC"
	if q.ByScore {
		order = "score_base DESC, at DESC, id DESC"
	}
	rows, err := d.db.Query(`
		SELECT network, id, kind, at, height, actor, target, namespace,
		       facts, layers, evidence_tx, first_ever, reach, magnitude, score_base, built_at
		  FROM discover_events
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY `+order+`
		 LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := []DiscoverEvent{}
	for rows.Next() {
		var e DiscoverEvent
		var facts, layers string
		var firstEver int
		if err := rows.Scan(&e.Network, &e.ID, &e.Kind, &e.At, &e.Height, &e.Actor, &e.Target,
			&e.Namespace, &facts, &layers, &e.EvidenceTx, &firstEver, &e.Reach,
			&e.Magnitude, &e.ScoreBase, &e.BuiltAt); err != nil {
			return nil, "", err
		}
		e.Facts = json.RawMessage(facts)
		e.Layers = json.RawMessage(layers)
		e.FirstEver = firstEver != 0
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	next := ""
	if len(out) > q.Limit {
		out = out[:q.Limit]
		last := out[len(out)-1]
		next = last.At + "|" + last.ID
	}
	return out, next, nil
}

// DiscoverCounts is the per-kind tally over the same window the page asked for,
// so the filter chips can show numbers without a second request.
//
// Deliberately over the *unfiltered* window: a chip that showed the count after
// its own filter was applied would read "3" next to a kind the reader is not
// looking at, which is the one number it must not be.
func (d *DB) DiscoverCounts(network, since string) (map[string]int, error) {
	if network == "" {
		return nil, fmt.Errorf("discover is per chain: a network is required")
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	args := []any{network}
	q := `SELECT kind, COUNT(*) FROM discover_events WHERE network = ?`
	if since != "" {
		q += ` AND at >= ?`
		args = append(args, since)
	}
	q += ` GROUP BY kind`

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// DiscoverBuiltAt is the generation the stored rows belong to: the newest
// built_at in the table.
//
// The gap between this and the moment a response is written is the page's real
// staleness, and it is worth showing. A marketing reader who refreshes and sees
// nothing new deserves to know the tick has not run rather than concluding the
// chain is quiet.
func (d *DB) DiscoverBuiltAt(network string) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var at sql.NullString
	err := d.db.QueryRow(`SELECT MAX(built_at) FROM discover_events WHERE network = ?`,
		network).Scan(&at)
	if err != nil {
		return "", err
	}
	return at.String, nil
}

// splitCursor parses "at|id". A malformed cursor is treated as no cursor
// rather than as an error: it arrives in a URL, so it is attacker-controlled
// and the worst it should do is start the reader at the top.
func splitCursor(c string) (at, id string, ok bool) {
	if c == "" {
		return "", "", false
	}
	i := strings.Index(c, "|")
	if i <= 0 || i == len(c)-1 {
		return "", "", false
	}
	return c[:i], c[i+1:], true
}

func jsonOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DiscoverScorePercentiles returns the 90th and 60th percentile of score_base
// over a window, which are the interest-bucket boundaries.
//
// Over score_base and not over the final score, deliberately. The final score
// includes recency, which changes every second, so buckets drawn from it would
// make an event's interest level drift purely with the clock: something "high"
// at breakfast is "medium" by lunch, and the canonical "tell me what to share"
// query stops being reproducible. score_base never changes once written, so
// these boundaries move only when the chain's mix of events actually moves.
//
// Relative rather than absolute for the reason the design gives: a threshold
// set today quietly marks everything high the month the chain doubles.
func (d *DB) DiscoverScorePercentiles(network, since string) (p90, p60 float64, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	args := []any{network}
	q := `SELECT score_base FROM discover_events WHERE network = ? AND score_base > 0`
	if since != "" {
		q += ` AND at >= ?`
		args = append(args, since)
	}
	q += ` ORDER BY score_base`

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	var scores []float64
	for rows.Next() {
		var s float64
		if err := rows.Scan(&s); err != nil {
			return 0, 0, err
		}
		scores = append(scores, s)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	// No distribution yet means no basis for calling anything high. Returning
	// +Inf makes every event low, which is the honest answer on an empty table
	// and fails closed: an empty feed recommends nothing rather than
	// recommending everything.
	if len(scores) == 0 {
		return math.Inf(1), math.Inf(1), nil
	}
	return percentileOf(scores, 0.90), percentileOf(scores, 0.60), nil
}

// percentileOf takes the nearest-rank percentile of an ascending slice.
func percentileOf(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// DiscoverTotal counts the events matching a filter, ignoring paging.
//
// Separate from the page query because the page is bounded and the count is
// not. Reporting the size of the fetched slice as the total was the first
// shape of this, and it published "total: 200" for a window holding 474: a
// number in an envelope that a consumer has no way to check is worse than no
// number, because it will be believed.
func (d *DB) DiscoverTotal(q DiscoverQuery) (int, error) {
	if q.Network == "" {
		return 0, fmt.Errorf("discover is per chain: a network is required")
	}
	where := []string{"network = ?"}
	args := []any{q.Network}
	if len(q.Kinds) > 0 {
		where = append(where, "kind IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Kinds)), ",")+")")
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	if q.Namespace != "" {
		where = append(where, "namespace = ?")
		args = append(args, q.Namespace)
	}
	if q.Actor != "" {
		where = append(where, "actor = ?")
		args = append(args, q.Actor)
	}
	if q.Since != "" {
		where = append(where, "at >= ?")
		args = append(args, q.Since)
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM discover_events WHERE `+
		strings.Join(where, " AND "), args...).Scan(&n)
	return n, err
}
