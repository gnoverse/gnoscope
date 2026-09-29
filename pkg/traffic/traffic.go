// Package traffic records what this server was asked for, and answers
// aggregate questions about it.
//
// Nothing here ever sees a reader as a person. The chain is public, this
// explorer is public, and the interesting question is which realms and which
// networks people actually open, not who opened them. So the only identity
// stored is a keyed hash whose key is regenerated daily and never written down
// (see visitor.go), and the dashboard built on top answers in aggregates only.
//
// Why a separate database file rather than a table in the chain index: the two
// have nothing in common but the process. The index is ~1.7 GB of chain
// history, backed up before a migration and rebuilt from the chain when it is
// wrong. Traffic is small, has a retention policy, is not reconstructible from
// anywhere, and must be droppable on its own. Sharing a file would tie a
// retention delete to a 1.7 GB write lock and put reader behaviour inside every
// backup of the chain index.
package traffic

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// flushInterval is how often buffered records reach disk. A request must
	// not cost a write. That is the same reasoning as pkg/httpapi's
	// ViewCounter, and it matters more here because this counts *every*
	// request rather than realm page opens.
	flushInterval = 5 * time.Second

	// bufferLimit caps what an unflushed burst can cost in memory, and is the
	// reason Record can never block or allocate without bound. A crawler
	// walking pagination at a thousand requests a second fills this in two
	// flush intervals; past it, records are dropped and counted, because
	// dropping request *logs* is always better than dropping requests.
	bufferLimit = 50000

	// pruneInterval is how often expired rows are deleted. Retention is a
	// promise, and a promise nothing enforces is a lie: this runs on a timer
	// from process start, not once at boot, because the process lives for days.
	pruneInterval = time.Hour
)

// Record is one served request, already reduced to what may be stored.
//
// Every field here is either bounded (a route pattern, a status, a class) or
// public (a realm path, a network). What is deliberately absent: the IP, the
// user-agent string, the referer path, the query string, and any request body.
type Record struct {
	At      time.Time
	Visitor string // keyed hash, rotates daily, see visitor.go
	// Host is the name the reader asked for, lowercased and without its port.
	//
	// Recorded rather than assumed. This server answers to more than one name
	// (the canonical one, its www form, and whatever a redirect or a future
	// rename adds), and every panel here was silently treating them as one
	// place. "Only the canonical host" is a claim, and a claim needs a column
	// to be checkable.
	Host string
	// EntityKind and Entity say what a page view is *about*: one realm read
	// through its overview, its usage tab and its source browser is three
	// paths and one subject, and a list of paths splits it three ways.
	EntityKind string
	Entity     string
	// PageKind is the opposite question: not which realm, but what sort of
	// page. Both are cheap and neither substitutes for the other.
	PageKind string
	Method   string // GET, POST
	Route    string // mux pattern, e.g. "/api/realm/{path...}", bounded by the routing table
	Target   string // the wildcard part, e.g. "r/moul/home", the interesting half
	Network  string // ?network=, "" when absent
	Kind     string // page | api | mcp | badge | asset
	Tool     string // MCP tool name, when Kind is mcp
	Status   int
	Bytes    int64   // bytes on the wire, so after compression
	DurMS    float64 // total, measured outermost
	AppMS    float64 // handler cost from Server-Timing, 0 on a cache hit
	Cache    string  // HIT | MISS | STALE | WAIT | ""
	RefHost  string  // referer host only, never its path
	Client   string  // browser | agent | bot | unknown
	Robot    bool
}

// Store buffers records and writes them in batches.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex

	salt          *saltRotator
	retentionDays int

	mu      sync.Mutex
	buf     []Record
	dropped int64
	written int64
}

// Open prepares the traffic database at path, creating it if absent.
//
// retentionDays bounds how long raw rows live. Zero keeps them forever, which
// is a choice a deployment may make and this package will not make for it.
func Open(path string, retentionDays int) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{
		db:            db,
		salt:          newSaltRotator(),
		retentionDays: retentionDays,
		buf:           make([]Record, 0, 1024),
	}, nil
}

func initSchema(db *sql.DB) error {
	// One table, no rollups. At this explorer's volume a day is thousands of
	// rows, not millions, and a rollup would be a second thing to keep correct
	// in exchange for arithmetic SQLite does in milliseconds. If a GROUP BY
	// here ever becomes the slow part, that is the moment to add one, and the
	// raw rows will still be there to build it from.
	const ddl = `
CREATE TABLE IF NOT EXISTS requests (
	ts       INTEGER NOT NULL,
	day      TEXT    NOT NULL,
	hour     INTEGER NOT NULL,
	visitor  TEXT    NOT NULL DEFAULT '',
	host     TEXT    NOT NULL DEFAULT '',
	entity_kind TEXT NOT NULL DEFAULT '',
	entity      TEXT NOT NULL DEFAULT '',
	page_kind   TEXT NOT NULL DEFAULT '',
	method   TEXT    NOT NULL DEFAULT '',
	route    TEXT    NOT NULL DEFAULT '',
	target   TEXT    NOT NULL DEFAULT '',
	network  TEXT    NOT NULL DEFAULT '',
	kind     TEXT    NOT NULL DEFAULT '',
	tool     TEXT    NOT NULL DEFAULT '',
	status   INTEGER NOT NULL DEFAULT 0,
	bytes    INTEGER NOT NULL DEFAULT 0,
	dur_ms   REAL    NOT NULL DEFAULT 0,
	app_ms   REAL    NOT NULL DEFAULT 0,
	cache    TEXT    NOT NULL DEFAULT '',
	ref_host TEXT    NOT NULL DEFAULT '',
	client   TEXT    NOT NULL DEFAULT '',
	robot    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_requests_day      ON requests(day);
CREATE INDEX IF NOT EXISTS idx_requests_day_kind ON requests(day, kind);
CREATE INDEX IF NOT EXISTS idx_requests_day_rt   ON requests(day, route);
CREATE INDEX IF NOT EXISTS idx_requests_ts       ON requests(ts);
CREATE INDEX IF NOT EXISTS idx_requests_target   ON requests(day, target) WHERE target <> '';
CREATE INDEX IF NOT EXISTS idx_requests_day_host ON requests(day, host);
CREATE INDEX IF NOT EXISTS idx_requests_entity ON requests(day, entity_kind, entity) WHERE entity <> '';
CREATE INDEX IF NOT EXISTS idx_requests_pagekind ON requests(day, page_kind) WHERE page_kind <> '';
`
	// Migrate before the DDL, not after: the DDL creates an index on `host`,
	// and on a database written before that column existed the index is what
	// fails, with "no such column: host" at Open time. Caught by
	// TestOpenMigratesDatabaseWithoutHostColumn before it reached a real
	// database; the failure mode is a server that will not start.
	if err := migrateAddColumns(db); err != nil {
		return err
	}
	_, err := db.Exec(ddl)
	return err
}

// migrateAddHost adds the host column to a database written before it existed.
//
// Rows from before keep an empty host rather than being guessed at. They show
// under "(not recorded)" in the hosts panel and are excluded by a host filter,
// which is the honest handling: this process cannot know what name those
// readers typed, and backfilling the canonical one would make a filter return
// rows it has no evidence for. The bucket drains as retention advances.
// addedColumns are columns that arrived after the table first shipped, in the
// order they arrived. Adding one here is the whole migration: ALTER TABLE ADD
// COLUMN with a default is cheap in SQLite and rewrites nothing.
var addedColumns = []struct{ name, ddl string }{
	{"host", "host TEXT NOT NULL DEFAULT ''"},
	{"entity_kind", "entity_kind TEXT NOT NULL DEFAULT ''"},
	{"entity", "entity TEXT NOT NULL DEFAULT ''"},
	{"page_kind", "page_kind TEXT NOT NULL DEFAULT ''"},
}

func migrateAddColumns(db *sql.DB) error {
	// PRAGMA table_info, not a substring search of the CREATE statement. The
	// obvious `strings.Contains(ddl, "host")` is wrong and silently so: the
	// table already has a `ref_host` column, so the check passes on a database
	// that has no `host` at all, the ALTER is skipped, and Open fails on the
	// index instead. Caught by TestOpenMigratesDatabaseWithoutHostColumn.
	rows, err := db.Query(`PRAGMA table_info(requests)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	have, any := map[string]bool{}, false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		any = true
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !any {
		// No table yet. CREATE TABLE below writes every column.
		return nil
	}
	for _, c := range addedColumns {
		if have[c.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE requests ADD COLUMN ` + c.ddl); err != nil {
			return fmt.Errorf("add column %s: %w", c.name, err)
		}
	}
	return nil
}

// Visitor derives the stored identity for one request's client.
//
// Exported because the middleware holds the IP and this package holds the salt,
// and the salt must not leave it.
func (s *Store) Visitor(ip, ua string, now time.Time) string {
	if s == nil {
		return ""
	}
	return s.salt.visitor(ip, ua, now)
}

// Record buffers one request. Never blocks on I/O, never returns an error, and
// is safe on a nil Store so the middleware needs no branch.
func (s *Store) Record(rec Record) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) >= bufferLimit {
		s.dropped++
		return
	}
	s.buf = append(s.buf, rec)
}

// Flush writes what is buffered. Safe to call on an empty buffer, and safe to
// call concurrently with Record.
func (s *Store) Flush() {
	if s == nil {
		return
	}
	s.mu.Lock()
	batch := s.buf
	s.buf = make([]Record, 0, 1024)
	s.mu.Unlock()
	if len(batch) == 0 {
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("traffic: begin: %v", err)
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO requests
		(ts, day, hour, visitor, host, entity_kind, entity, page_kind,
		 method, route, target, network, kind, tool,
		 status, bytes, dur_ms, app_ms, cache, ref_host, client, robot)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		log.Printf("traffic: prepare: %v", err)
		return
	}
	for _, r := range batch {
		utc := r.At.UTC()
		robot := 0
		if r.Robot {
			robot = 1
		}
		if _, err := stmt.Exec(
			utc.Unix(), utc.Format("2006-01-02"), utc.Hour(),
			r.Visitor, r.Host, r.EntityKind, r.Entity, r.PageKind,
			r.Method, r.Route, r.Target, r.Network, r.Kind, r.Tool,
			r.Status, r.Bytes, r.DurMS, r.AppMS, r.Cache, r.RefHost, r.Client, robot,
		); err != nil {
			stmt.Close()
			tx.Rollback()
			log.Printf("traffic: insert: %v", err)
			return
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		log.Printf("traffic: commit: %v", err)
		return
	}

	s.mu.Lock()
	s.written += int64(len(batch))
	s.mu.Unlock()
}

// Prune deletes rows past the retention horizon and reports how many went.
//
// A no-op when retention is zero, which is the "keep everything" setting.
func (s *Store) Prune(now time.Time) (int64, error) {
	if s == nil || s.retentionDays <= 0 {
		return 0, nil
	}
	cutoff := now.UTC().AddDate(0, 0, -s.retentionDays).Format("2006-01-02")
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM requests WHERE day < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Run flushes and prunes until ctx is done, then flushes once more.
//
// That last flush is the point of the goroutine. Deploys restart this process
// several times a day, and dropping the buffer each time would silently bias
// the record toward whatever nobody was reading at deploy time.
func (s *Store) Run(ctx context.Context) {
	if s == nil {
		return
	}
	flush := time.NewTicker(flushInterval)
	defer flush.Stop()
	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()

	if n, err := s.Prune(time.Now()); err != nil {
		log.Printf("traffic: prune: %v", err)
	} else if n > 0 {
		log.Printf("traffic: pruned %d rows past %d days", n, s.retentionDays)
	}

	for {
		select {
		case <-ctx.Done():
			s.Flush()
			return
		case <-flush.C:
			s.Flush()
		case <-prune.C:
			if n, err := s.Prune(time.Now()); err != nil {
				log.Printf("traffic: prune: %v", err)
			} else if n > 0 {
				log.Printf("traffic: pruned %d rows past %d days", n, s.retentionDays)
			}
		}
	}
}

// Stats reports the writer's own health, for the operator rather than the page.
type Stats struct {
	Buffered      int   `json:"buffered"`
	Written       int64 `json:"written"`
	Dropped       int64 `json:"dropped"`
	RetentionDays int   `json:"retention_days"`
}

func (s *Store) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Buffered:      len(s.buf),
		Written:       s.written,
		Dropped:       s.dropped,
		RetentionDays: s.retentionDays,
	}
}

// Close flushes and shuts the database.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.Flush()
	return s.db.Close()
}
