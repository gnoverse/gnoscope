package traffic

import (
	"fmt"
	"strings"
)

// Engagement is the behaviour half of the dashboard: not which pages were
// served, but what happened on them.
//
// Every figure here comes from the events table, so every figure here is about
// browsers that ran the instrumentation. A crawler produces request rows and
// no events at all, which is why the panels do not simply repeat the request
// log at a different angle: an empty Engagement with a busy Report means the
// traffic was machines.
type Engagement struct {
	// Visits is leave events: one per page a reader actually opened and then
	// left. Lower than page views by whatever is still open right now.
	Visits int64 `json:"visits"`
	// Readers is distinct visitors within the window, same daily hash as the
	// request log, so "somebody here on two days" counts twice.
	Readers int64 `json:"readers"`
	// MedianDwellMS and P90DwellMS, not a mean. One tab left open for an hour
	// moves a mean by minutes and a median not at all, and "how long does a
	// normal visit last" is the question.
	MedianDwellMS int64 `json:"median_dwell_ms"`
	P90DwellMS    int64 `json:"p90_dwell_ms"`
	MedianScroll  int   `json:"median_scroll_pct"`
	// BounceRate is the share of visits under 10 seconds with no interaction:
	// opened, nothing happened, gone. The single number that says a page is
	// not doing its job.
	BounceRate float64 `json:"bounce_rate"`
	// EngagedRate is the opposite end, and deliberately not "1 - bounce":
	// 30 seconds or more, or half the page scrolled, or an interaction. The
	// middle between the two is real and is not worth rounding into either.
	EngagedRate float64 `json:"engaged_rate"`

	// DwellBuckets and ScrollBuckets give the shape the two medians hide. A
	// median of 20s over a bimodal split (half bounce, half read for minutes)
	// describes nobody.
	DwellBuckets  []Count `json:"dwell_buckets"`
	ScrollBuckets []Count `json:"scroll_buckets"`

	// StickyPages ranks pages by median dwell among pages with enough visits
	// to mean anything. Where people stay.
	StickyPages []Count `json:"sticky_pages"`
	// SkimmedPages is the same list from the other end: opened often, left
	// fast. Where the app is failing somebody.
	SkimmedPages []Count `json:"skimmed_pages"`
	// DwellByKind is median dwell per kind of page, which is the comparison
	// that survives one realm being popular.
	DwellByKind []Count `json:"dwell_by_kind"`
	// ScrollByKind says how much of each kind of page is ever seen. A page
	// whose median reader sees the top 30% has its answer in the wrong place.
	ScrollByKind []Count `json:"scroll_by_kind"`

	// Entries is where visits start, Exits where they end, Edges the moves in
	// between. Together they are the path through the app, built from the
	// from_path on each visit rather than from a session table.
	Entries []Count `json:"entries"`
	Exits   []Count `json:"exits"`
	Edges   []Count `json:"edges"`

	// Actions is every named interaction, ranked. A control nobody touches in
	// 30 days is a control to remove; a tab everybody opens first is a tab to
	// make the default.
	Actions []Count `json:"actions"`
	// Searches is what people typed, ZeroSearches the subset that found
	// nothing. The second list is a feature request written by readers.
	Searches     []Count `json:"searches"`
	ZeroSearches []Count `json:"zero_searches"`
	// Errors is what broke in somebody's browser. Nobody reports these.
	Errors []Count `json:"errors"`

	// Empty says no events landed in this window, so the page can say the
	// instrumentation is off or too new rather than draw a dozen zeroes.
	Empty bool `json:"empty"`
}

// eventsWhere is where() for the events table.
//
// Deliberately a different function rather than a shared one with a table
// name: the request log's filters include `kind` (page, api, mcp) and `status`,
// neither of which exists here, and a shared builder would have to grow a
// branch per caller to stay correct. The two things it must keep identical
// are the window and the privacy filters, and those are three lines.
func (q Query) eventsWhere() (string, []any) {
	since := q.Now.UTC().Add(-q.Window.Duration).Unix()
	cond := []string{"ts >= ?"}
	args := []any{since}
	if q.Network != "" {
		cond = append(cond, "network = ?")
		args = append(args, q.Network)
	}
	// Same unconditional exclusion as the request log: this server talking to
	// itself is never a reader. The warmer runs no JavaScript and so writes no
	// events, but the filter stays, because "cannot happen" and "is excluded"
	// are different claims and only one of them survives a refactor.
	cond = append(cond, "client <> 'internal'")
	if q.Host != "" {
		cond = append(cond, "(host = ? OR host = '')")
		args = append(args, q.Host)
	}
	if c := q.Who.clause(); c != "" {
		cond = append(cond, c)
	}
	return strings.Join(cond, " AND "), args
}

// visits narrows to the leave events, which are the only ones carrying dwell
// and scroll. Kept separate from eventsWhere because the action, search and
// error panels must not be filtered by it.
const visitsOnly = " AND kind = 'leave'"

// Engagement answers the behaviour half of the dashboard in one pass.
func (s *Store) Engagement(q Query) (*Engagement, error) {
	if s == nil {
		return &Engagement{Empty: true}, nil
	}
	w, args := q.eventsWhere()
	vw := w + visitsOnly
	lim := q.limit()
	out := &Engagement{}

	row := s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT CASE WHEN visitor <> '' THEN visitor END)
		FROM events WHERE `+vw, args...)
	if err := row.Scan(&out.Visits, &out.Readers); err != nil {
		return nil, fmt.Errorf("engagement totals: %w", err)
	}
	if out.Visits == 0 {
		out.Empty = true
		// Actions, searches and errors can exist without a single completed
		// visit (a reader still on the page has not left it yet), so the
		// panels below are still worth filling. Empty only drives the wording.
	}

	var err error
	if out.MedianDwellMS, out.P90DwellMS, err = s.dwellPercentiles(vw, args); err != nil {
		return nil, err
	}
	if out.MedianScroll, err = s.medianScroll(vw, args); err != nil {
		return nil, err
	}
	if out.Visits > 0 {
		var bounced, engaged int64
		r := s.db.QueryRow(`SELECT
			SUM(CASE WHEN dwell_ms < 10000 AND actions = 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN dwell_ms >= 30000 OR scroll_pct >= 50 OR actions > 0 THEN 1 ELSE 0 END)
			FROM events WHERE `+vw, args...)
		if err := r.Scan(&bounced, &engaged); err != nil {
			return nil, fmt.Errorf("engagement rates: %w", err)
		}
		out.BounceRate = float64(bounced) / float64(out.Visits)
		out.EngagedRate = float64(engaged) / float64(out.Visits)
	}

	// Buckets are written as a CASE rather than computed in Go so the whole
	// window stays in SQLite: at 90 days of retention this is the one query
	// shape that would otherwise pull every row into memory to count it.
	out.DwellBuckets, err = s.bucketBy(vw, args, `CASE
		WHEN dwell_ms < 5000   THEN '0-5s'
		WHEN dwell_ms < 15000  THEN '5-15s'
		WHEN dwell_ms < 60000  THEN '15-60s'
		WHEN dwell_ms < 300000 THEN '1-5m'
		ELSE '5m+' END`, `CASE
		WHEN dwell_ms < 5000   THEN 0
		WHEN dwell_ms < 15000  THEN 1
		WHEN dwell_ms < 60000  THEN 2
		WHEN dwell_ms < 300000 THEN 3
		ELSE 4 END`)
	if err != nil {
		return nil, err
	}
	out.ScrollBuckets, err = s.bucketBy(vw, args, `CASE
		WHEN scroll_pct < 25  THEN 'top quarter'
		WHEN scroll_pct < 50  THEN 'to half'
		WHEN scroll_pct < 75  THEN 'past half'
		ELSE 'to the end' END`, `CASE
		WHEN scroll_pct < 25  THEN 0
		WHEN scroll_pct < 50  THEN 1
		WHEN scroll_pct < 75  THEN 2
		ELSE 3 END`)
	if err != nil {
		return nil, err
	}

	// minVisits keeps a page that one person opened once off a "where people
	// stay" ranking, where a single 40 minute tab would otherwise top it.
	const minVisits = 3
	if out.StickyPages, err = s.dwellRank(vw, args, "path", minVisits, true, lim); err != nil {
		return nil, err
	}
	if out.SkimmedPages, err = s.dwellRank(vw, args, "path", minVisits, false, lim); err != nil {
		return nil, err
	}
	if out.DwellByKind, err = s.dwellRank(vw, args, "page_kind", 1, true, lim); err != nil {
		return nil, err
	}
	if out.ScrollByKind, err = s.scrollRank(vw, args, "page_kind", lim); err != nil {
		return nil, err
	}

	if out.Entries, err = s.topEvents(vw, args, "path", "from_path = ''", lim, true); err != nil {
		return nil, err
	}
	if out.Edges, err = s.topEvents(vw, args, "from_path || ' → ' || path", "from_path <> ''", lim, false); err != nil {
		return nil, err
	}
	if out.Exits, err = s.exits(vw, args, lim); err != nil {
		return nil, err
	}

	if out.Actions, err = s.topEvents(w+" AND kind = 'action'", args, "action", "", lim, true); err != nil {
		return nil, err
	}
	if out.Searches, err = s.topEvents(w+" AND kind = 'search'", args, "value", "", lim, false); err != nil {
		return nil, err
	}
	if out.ZeroSearches, err = s.topEvents(w+" AND kind = 'search'", args, "value", "results = 0", lim, false); err != nil {
		return nil, err
	}
	if out.Errors, err = s.topEvents(w+" AND kind = 'error'", args, "value", "", lim, false); err != nil {
		return nil, err
	}
	return out, nil
}

// pctIndex is the row number of the pth percentile over n ordered rows, in
// integer arithmetic.
//
// `(n * p + 99) / 100` is ceil(n * p / 100) with SQLite's integer division,
// and the ceiling is the part that matters: the floor picks the row below the
// median on every odd-sized set, which for three visits of 1s, 5s and 55s
// reports the median as 1s. Written as integers rather than with a CAST of a
// float because rounding a float index is the same bug one layer down.
func pctIndex(p int) string {
	return fmt.Sprintf("MAX(1, (n * %d + 99) / 100)", p)
}

// dwellPercentiles reads the median and the 90th from SQLite rather than
// averaging. Same reason the slowest-routes panel ranks on p95: the tail is
// the interesting part and a mean hides it in both directions.
func (s *Store) dwellPercentiles(w string, args []any) (int64, int64, error) {
	var p50, p90 int64
	err := s.db.QueryRow(`
		WITH d AS (
			SELECT dwell_ms, ROW_NUMBER() OVER (ORDER BY dwell_ms) AS rn, COUNT(*) OVER () AS n
			FROM events WHERE `+w+`
		)
		SELECT
			COALESCE(MAX(CASE WHEN rn = `+pctIndex(50)+` THEN dwell_ms END), 0),
			COALESCE(MAX(CASE WHEN rn = `+pctIndex(90)+` THEN dwell_ms END), 0)
		FROM d`, args...).Scan(&p50, &p90)
	if err != nil {
		return 0, 0, fmt.Errorf("dwell percentiles: %w", err)
	}
	return p50, p90, nil
}

func (s *Store) medianScroll(w string, args []any) (int, error) {
	var p50 int
	err := s.db.QueryRow(`
		WITH d AS (
			SELECT scroll_pct, ROW_NUMBER() OVER (ORDER BY scroll_pct) AS rn, COUNT(*) OVER () AS n
			FROM events WHERE `+w+`
		)
		SELECT COALESCE(MAX(CASE WHEN rn = `+pctIndex(50)+` THEN scroll_pct END), 0)
		FROM d`, args...).Scan(&p50)
	if err != nil {
		return 0, fmt.Errorf("median scroll: %w", err)
	}
	return p50, nil
}

// bucketBy counts rows per bucket and keeps the buckets in their natural
// order, not in count order. A distribution sorted by size is not a
// distribution: the shape is the answer, and ordering by frequency destroys it.
func (s *Store) bucketBy(w string, args []any, label, order string) ([]Count, error) {
	rows, err := s.db.Query(`SELECT `+label+` AS l, `+order+` AS o, COUNT(*)
		FROM events WHERE `+w+` GROUP BY l, o ORDER BY o`, args...)
	if err != nil {
		return nil, fmt.Errorf("bucket: %w", err)
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		var ord int
		if err := rows.Scan(&c.Label, &ord, &c.Hits); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// dwellRank ranks a column by median dwell. Hits carries the milliseconds and
// Visitors carries the visit count, so one panel shape draws both without a
// second type: the page labels what it is drawing.
func (s *Store) dwellRank(w string, args []any, col string, minVisits int, desc bool, limit int) ([]Count, error) {
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	q := `
		WITH r AS (
			SELECT ` + col + ` AS l, dwell_ms,
			       ROW_NUMBER() OVER (PARTITION BY ` + col + ` ORDER BY dwell_ms) AS rn,
			       COUNT(*)    OVER (PARTITION BY ` + col + `) AS n
			FROM events WHERE ` + w + ` AND ` + col + ` <> ''
		)
		SELECT l, MAX(CASE WHEN rn = ` + pctIndex(50) + ` THEN dwell_ms END) AS med, MAX(n)
		FROM r GROUP BY l HAVING MAX(n) >= ? ORDER BY med ` + dir + ` LIMIT ?`
	rows, err := s.db.Query(q, append(append([]any{}, args...), minVisits, limit)...)
	if err != nil {
		return nil, fmt.Errorf("dwell rank %s: %w", col, err)
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Label, &c.Hits, &c.Visitors); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// scrollRank is dwellRank for scroll depth, as an average rather than a
// median: scroll is already a bounded percentage, so there is no tail for a
// median to protect against, and an average of 0-100 is the figure a reader
// of the panel expects.
func (s *Store) scrollRank(w string, args []any, col string, limit int) ([]Count, error) {
	rows, err := s.db.Query(`SELECT `+col+` AS l, CAST(AVG(scroll_pct) AS INTEGER), COUNT(*)
		FROM events WHERE `+w+` AND `+col+` <> ''
		GROUP BY l ORDER BY COUNT(*) DESC LIMIT ?`,
		append(append([]any{}, args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("scroll rank %s: %w", col, err)
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Label, &c.Hits, &c.Visitors); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// topEvents is topBy against the events table.
func (s *Store) topEvents(w string, args []any, label, extra string, limit int, uniq bool) ([]Count, error) {
	cond := w
	if extra != "" {
		cond += " AND " + extra
	}
	sel := `SELECT ` + label + ` AS l, COUNT(*)`
	if uniq {
		sel += `, COUNT(DISTINCT CASE WHEN visitor <> '' THEN visitor END)`
	}
	rows, err := s.db.Query(sel+` FROM events WHERE `+cond+` AND `+label+` <> ''
		GROUP BY l ORDER BY COUNT(*) DESC LIMIT ?`,
		append(append([]any{}, args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("top events %s: %w", label, err)
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		var scanErr error
		if uniq {
			scanErr = rows.Scan(&c.Label, &c.Hits, &c.Visitors)
		} else {
			scanErr = rows.Scan(&c.Label, &c.Hits)
		}
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// exits ranks the last page of each visitor's day.
//
// "Last" is per visitor per day, which is the largest unit the privacy design
// allows: the visitor hash is regenerated at midnight UTC and never stored, so
// a session cannot be followed across a day boundary and is not meant to be.
// A reader here in the evening and again the next morning has two last pages,
// which is the honest reading of two visits.
func (s *Store) exits(w string, args []any, limit int) ([]Count, error) {
	rows, err := s.db.Query(`
		WITH last AS (
			SELECT path, visitor, day,
			       ROW_NUMBER() OVER (PARTITION BY visitor, day ORDER BY ts DESC) AS rn
			FROM events WHERE `+w+` AND visitor <> '' AND path <> ''
		)
		SELECT path, COUNT(*) FROM last WHERE rn = 1
		GROUP BY path ORDER BY COUNT(*) DESC LIMIT ?`,
		append(append([]any{}, args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("exits: %w", err)
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Label, &c.Hits); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
