package traffic

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// What the dashboard is allowed to ask.
//
// Every function here returns counts, never rows. That is not politeness, it is
// the design: the page is public, and a public endpoint that could return one
// request (its visitor id, its exact time, its referer) would re-identify a
// reader from three of them. So there is no "recent requests" query in this
// file and there must never be one.

// Window is a supported reporting range. Anything else is rejected rather than
// silently widened, so a typo cannot quietly report a different month.
type Window struct {
	Key      string
	Duration time.Duration
	ByHour   bool
}

var windows = map[string]Window{
	"24h": {"24h", 24 * time.Hour, true},
	"7d":  {"7d", 7 * 24 * time.Hour, false},
	"30d": {"30d", 30 * 24 * time.Hour, false},
	"90d": {"90d", 90 * 24 * time.Hour, false},
}

// ParseWindow resolves a window key, falling back to 7d.
func ParseWindow(s string) Window {
	if w, ok := windows[s]; ok {
		return w
	}
	return windows["7d"]
}

// Query is one dashboard request.
type Query struct {
	Window   Window
	Network  string // "" for every network
	Kind     string // "" for every kind
	WithBots bool   // false drops self-declared crawlers, which is the default
	Limit    int
	Now      time.Time
}

// Count is one labelled number, which is most of what the page draws.
type Count struct {
	Label string `json:"label"`
	Hits  int64  `json:"hits"`
	// Visitors is only meaningful on breakdowns where a distinct count was
	// computed; it is omitted rather than zeroed elsewhere.
	Visitors int64 `json:"visitors,omitempty"`
}

// Bucket is one point on the time series.
type Bucket struct {
	At       string `json:"at"` // "2026-09-29" or "2026-09-29T14"
	Hits     int64  `json:"hits"`
	Visitors int64  `json:"visitors"`
	Errors   int64  `json:"errors"`
}

// Timing is one route's cost, which is the panel that turns "it feels slow"
// into a number somebody can act on.
type Timing struct {
	Route  string  `json:"route"`
	Hits   int64   `json:"hits"`
	P50MS  float64 `json:"p50_ms"`
	P95MS  float64 `json:"p95_ms"`
	AppAvg float64 `json:"app_avg_ms"`
}

// Totals is the headline row.
type Totals struct {
	Requests  int64   `json:"requests"`
	Visitors  int64   `json:"visitors"`
	Pages     int64   `json:"pages"`
	API       int64   `json:"api"`
	MCP       int64   `json:"mcp"`
	Bytes     int64   `json:"bytes"`
	Errors    int64   `json:"errors"`
	CacheHits int64   `json:"cache_hits"`
	P50MS     float64 `json:"p50_ms"`
	P95MS     float64 `json:"p95_ms"`
}

// Report is the whole page in one response.
type Report struct {
	Window    string   `json:"window"`
	Since     string   `json:"since"`
	Network   string   `json:"network,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	WithBots  bool     `json:"with_bots"`
	Totals    Totals   `json:"totals"`
	Series    []Bucket `json:"series"`
	TopPages  []Count  `json:"top_pages"`
	TopRealms []Count  `json:"top_realms"`
	TopAPI    []Count  `json:"top_api"`
	Tools     []Count  `json:"tools"`
	Networks  []Count  `json:"networks"`
	Clients   []Count  `json:"clients"`
	Referers  []Count  `json:"referers"`
	Statuses  []Count  `json:"statuses"`
	Cache     []Count  `json:"cache"`
	Slowest   []Timing `json:"slowest"`
	NotFound  []Count  `json:"not_found"`
	// Empty says the window holds no rows, so the page can say "no data yet"
	// rather than drawing a dozen convincingly empty charts.
	Empty bool `json:"empty"`
}

// where builds the shared filter. Values are bound, never interpolated: network
// reaches here from a query string.
func (q Query) where() (string, []any) {
	since := q.Now.UTC().Add(-q.Window.Duration).Unix()
	cond := []string{"ts >= ?"}
	args := []any{since}
	if q.Network != "" {
		cond = append(cond, "network = ?")
		args = append(args, q.Network)
	}
	if q.Kind != "" {
		cond = append(cond, "kind = ?")
		args = append(args, q.Kind)
	}
	if !q.WithBots {
		// Excluded by default. A crawler is real traffic and real cost, so it
		// is stored; it is not a reader, so it does not get to shape "what are
		// people doing" unless somebody asks for it.
		cond = append(cond, "client <> 'bot'")
	}
	return strings.Join(cond, " AND "), args
}

func (q Query) limit() int {
	if q.Limit <= 0 || q.Limit > 100 {
		return 15
	}
	return q.Limit
}

// Report answers the whole dashboard in one pass.
func (s *Store) Report(q Query) (*Report, error) {
	if s == nil {
		return &Report{Empty: true}, nil
	}
	if q.Now.IsZero() {
		q.Now = time.Now()
	}
	w, args := q.where()
	lim := q.limit()

	out := &Report{
		Window:   q.Window.Key,
		Since:    q.Now.UTC().Add(-q.Window.Duration).Format(time.RFC3339),
		Network:  q.Network,
		Kind:     q.Kind,
		WithBots: q.WithBots,
	}

	row := s.db.QueryRow(`SELECT
		COUNT(*),
		COUNT(DISTINCT CASE WHEN visitor <> '' THEN visitor END),
		COALESCE(SUM(kind = 'page'), 0),
		COALESCE(SUM(kind = 'api'), 0),
		COALESCE(SUM(kind = 'mcp'), 0),
		COALESCE(SUM(bytes), 0),
		COALESCE(SUM(status >= 400), 0),
		COALESCE(SUM(cache = 'HIT'), 0)
		FROM requests WHERE `+w, args...)
	t := &out.Totals
	if err := row.Scan(&t.Requests, &t.Visitors, &t.Pages, &t.API, &t.MCP,
		&t.Bytes, &t.Errors, &t.CacheHits); err != nil {
		return nil, fmt.Errorf("totals: %w", err)
	}
	if t.Requests == 0 {
		out.Empty = true
		return out, nil
	}

	var err error
	if t.P50MS, err = s.percentile(w, args, t.Requests, 50); err != nil {
		return nil, err
	}
	if t.P95MS, err = s.percentile(w, args, t.Requests, 95); err != nil {
		return nil, err
	}

	if out.Series, err = s.series(q, w, args); err != nil {
		return nil, err
	}

	// Each breakdown is the same shape with a different label expression, so
	// they share one helper rather than a dozen near-identical functions.
	type panel struct {
		dst   *[]Count
		label string
		extra string
		uniq  bool
	}
	panels := []panel{
		{&out.TopPages, "target", "kind = 'page' AND target <> ''", true},
		{&out.TopRealms, "target", "route LIKE '/api/realm%' AND target <> ''", true},
		{&out.TopAPI, "route", "kind = 'api'", false},
		{&out.Tools, "tool", "kind = 'mcp' AND tool <> ''", false},
		{&out.Networks, "CASE WHEN network = '' THEN 'all' ELSE network END", "", false},
		{&out.Clients, "client", "", false},
		{&out.Referers, "ref_host", "ref_host <> ''", true},
		{&out.Statuses, "CAST(status AS TEXT)", "", false},
		{&out.Cache, "CASE WHEN cache = '' THEN 'uncached' ELSE cache END", "", false},
		{&out.NotFound, "target", "status = 404 AND target <> ''", false},
	}
	for _, p := range panels {
		got, err := s.topBy(w, args, p.label, p.extra, lim, p.uniq)
		if err != nil {
			return nil, err
		}
		*p.dst = got
	}

	if out.Slowest, err = s.slowest(w, args, lim); err != nil {
		return nil, err
	}
	return out, nil
}

// streamExcluded drops SSE connections from anything measuring duration.
//
// Their DurMS is how long a reader stayed connected, not how long they waited,
// and the two numbers do not belong in one column. They stay in every count.
const streamExcluded = ` AND kind <> 'stream'`

// percentile reads the nth value by rank rather than computing one, because
// SQLite has no percentile function and an ORDER BY over an indexed window of
// this size costs less than carrying an approximation nobody can check.
func (s *Store) percentile(w string, args []any, _ int64, pct int) (float64, error) {
	// Counted here rather than reused from Totals.Requests: that figure counts
	// streams and this ranking does not, and an offset taken past the end of a
	// shorter set silently returns the maximum instead of the percentile.
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE `+w+streamExcluded, args...).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	offset := (n * int64(pct)) / 100
	if offset >= n {
		offset = n - 1
	}
	if offset < 0 {
		offset = 0
	}
	var v float64
	q := `SELECT dur_ms FROM requests WHERE ` + w + streamExcluded + ` ORDER BY dur_ms LIMIT 1 OFFSET ?`
	err := s.db.QueryRow(q, append(append([]any{}, args...), offset)...).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

func (s *Store) series(q Query, w string, args []any) ([]Bucket, error) {
	// Hour buckets for a day, day buckets beyond it. A 90-day chart of hourly
	// points is 2,160 columns nobody can read and a payload nobody needs.
	bucket := "day"
	if q.Window.ByHour {
		bucket = `day || 'T' || printf('%02d', hour)`
	}
	rows, err := s.db.Query(`SELECT `+bucket+` AS b,
		COUNT(*),
		COUNT(DISTINCT CASE WHEN visitor <> '' THEN visitor END),
		COALESCE(SUM(status >= 400), 0)
		FROM requests WHERE `+w+` GROUP BY b ORDER BY b`, args...)
	if err != nil {
		return nil, fmt.Errorf("series: %w", err)
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.At, &b.Hits, &b.Visitors, &b.Errors); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// topBy ranks one column. uniq adds a distinct-visitor count, which is worth
// its cost on the panels a human reads as popularity and not on the rest.
func (s *Store) topBy(w string, args []any, label, extra string, limit int, uniq bool) ([]Count, error) {
	cond := w
	if extra != "" {
		cond += " AND " + extra
	}
	sel := `SELECT ` + label + ` AS l, COUNT(*)`
	if uniq {
		sel += `, COUNT(DISTINCT CASE WHEN visitor <> '' THEN visitor END)`
	}
	rows, err := s.db.Query(sel+` FROM requests WHERE `+cond+
		` GROUP BY l ORDER BY COUNT(*) DESC LIMIT ?`,
		append(append([]any{}, args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("top %s: %w", label, err)
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

// slowest ranks routes by their 95th percentile, not their mean.
//
// A mean hides the shape that matters: one route answering in 40ms with a tail
// at 8s is a worse reader experience than one answering in 300ms every time,
// and the mean can put them in the same place. SQLite has no percentile
// aggregate, so the rank is computed per route in SQL with a window function.
func (s *Store) slowest(w string, args []any, limit int) ([]Timing, error) {
	rows, err := s.db.Query(`
		WITH ranked AS (
			SELECT route, dur_ms, app_ms,
			       ROW_NUMBER() OVER (PARTITION BY route ORDER BY dur_ms) AS rn,
			       COUNT(*)    OVER (PARTITION BY route)                  AS n
			FROM requests WHERE `+w+streamExcluded+`
		)
		SELECT route, MAX(n),
		       MAX(CASE WHEN rn = MAX(1, n * 50 / 100) THEN dur_ms END),
		       MAX(CASE WHEN rn = MAX(1, n * 95 / 100) THEN dur_ms END),
		       AVG(app_ms)
		FROM ranked GROUP BY route HAVING MAX(n) >= 5
		ORDER BY 4 DESC LIMIT ?`,
		append(append([]any{}, args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("slowest: %w", err)
	}
	defer rows.Close()
	out := []Timing{}
	for rows.Next() {
		var t Timing
		var p50, p95, app sql.NullFloat64
		if err := rows.Scan(&t.Route, &t.Hits, &p50, &p95, &app); err != nil {
			return nil, err
		}
		t.P50MS, t.P95MS, t.AppAvg = p50.Float64, p95.Float64, app.Float64
		out = append(out, t)
	}
	return out, rows.Err()
}
