// Package healthlog records what each configured network looked like, one
// sample per probe, and answers "when was it last unreachable, and for how
// long".
//
// The health page used to report a state and not a history. `probeNode` in
// pkg/httpapi/chainhealth.go is a point-in-time question and nothing kept the
// answer, so a chain down for three days and one that blipped thirty seconds
// ago rendered identically, and the 38 breaker trips measured on the
// production instance over one day were invisible on the page whose whole job
// is to show exactly that.
//
// Why a separate database file rather than a table in the chain index: the
// same reasoning as pkg/traffic, which this is modelled on. The index is ~1.7
// GB of chain history, rebuilt from the chain when it is wrong. This is small,
// has a retention policy, is not reconstructible from anywhere once dropped,
// and must be droppable on its own. Sharing a file would tie a retention
// delete to a 1.7 GB write lock and put an availability log inside every
// backup of the index.
//
// What is stored is this instance's *view* of each chain, which is the only
// thing it can honestly claim. A row saying `indexer_down` means we could not
// read that chain from here at that minute; it is not a statement about the
// chain's availability to anyone else.
package healthlog

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const (
	// SampleInterval is how often the background sampler probes. One minute,
	// for three reasons that happen to agree. It is the unit the thing being
	// measured moves in: breakerCooldown is 60s, so no outage worth naming is
	// shorter. It is cheap: four chains is 5,760 rows a day, a few hundred KB
	// a month, against a chain index measured in gigabytes. And it is the
	// finest resolution the strip can show, since a 7-day strip is drawn in
	// hourly cells and a sample every second would buy 3,600 rows per cell
	// that collapse to one colour.
	SampleInterval = time.Minute

	// MinGap throttles writes per network, whoever asks.
	//
	// Samples come from two places: the sampler above, and every reader who
	// opens /sanity or /cockpit, since recording happens inside the probe
	// itself so that no probe path is silently unrecorded. /cockpit is polled
	// by a wall display, so without this the row count would be a function of
	// how many screens are pointed at it. Half the interval, so an ordinary
	// tick is never dropped for arriving a few seconds early.
	MinGap = 30 * time.Second

	// GapTolerance is how long a hole in the series has to be before it counts
	// as unobserved rather than as more of the same state. Three intervals:
	// long enough that one slow probe or one missed tick does not draw a fake
	// outage, short enough that a deploy shows up as the gap in the record it
	// actually is.
	GapTolerance = 3 * SampleInterval

	// DefaultRetentionDays matches -traffic-retention-days. The strip shows
	// seven days and the summary answers "when was it last unreachable", which
	// wants more history than the strip draws; a month covers "has this been
	// happening all along" without becoming a second thing to size.
	DefaultRetentionDays = 30

	// pruneInterval is how often expired rows go. On a timer from process
	// start rather than once at boot, because the process lives for days, and
	// a retention nothing enforces is not a retention.
	pruneInterval = time.Hour

	// detailLimit bounds the stored sentence. Detail is the diagnosis text,
	// which can carry an upstream error body; a row is a log line, not a
	// transcript.
	detailLimit = 500

	// bufferLimit caps what an unflushed burst costs in memory. Unreachable in
	// practice given MinGap and a handful of networks; it is here so that a
	// misconfigured caller cannot turn a buffer into a leak.
	bufferLimit = 10000
)

// Sample is one network's state at one moment, as this instance saw it.
type Sample struct {
	At      time.Time
	Network string
	State   string
	// Height is whatever height the probe could see: the node's own, or the
	// indexer's when the node did not answer. Zero means neither did.
	Height int64
	// Detail is the diagnosis sentence, which carries the error when there was
	// one. Stored because "unreachable" six times in a week is one question
	// and "unreachable for six different reasons" is another.
	Detail string
}

// Store buffers samples and writes them in batches.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex

	retentionDays int
	minGap        time.Duration

	mu       sync.Mutex
	buf      []Sample
	last     map[string]time.Time
	written  int64
	throttle int64
	dropped  int64
}

// Open prepares the health database at path, creating it if absent.
//
// retentionDays bounds how long rows live; zero keeps them forever, which is a
// choice a deployment may make and this package will not make for it.
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
		retentionDays: retentionDays,
		minGap:        MinGap,
		buf:           make([]Sample, 0, 64),
		last:          map[string]time.Time{},
	}, nil
}

func initSchema(db *sql.DB) error {
	// One table, no rollups, for the same reason pkg/traffic has none: a day
	// is thousands of rows and the GROUP BY over them is milliseconds. The
	// transitions are derived on read (see transitions.go) rather than stored,
	// so there is nothing here that has to be kept in step with anything.
	const ddl = `
CREATE TABLE IF NOT EXISTS health_samples (
	ts      INTEGER NOT NULL,
	network TEXT    NOT NULL,
	state   TEXT    NOT NULL,
	height  INTEGER NOT NULL DEFAULT 0,
	detail  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_health_net_ts ON health_samples(network, ts);
CREATE INDEX IF NOT EXISTS idx_health_ts     ON health_samples(ts);
`
	_, err := db.Exec(ddl)
	return err
}

// SetMinGap overrides the per-network write throttle. For tests, which have to
// write a week of history in a millisecond.
func (s *Store) SetMinGap(d time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.minGap = d
}

// Record buffers one sample, unless this network was sampled too recently.
//
// Never blocks on I/O, never returns an error, and is safe on a nil Store so
// the probe needs no branch. Reports whether the sample was taken, which is
// what the tests assert on and what Stats counts.
func (s *Store) Record(sm Sample) bool {
	if s == nil || sm.Network == "" || sm.State == "" {
		return false
	}
	if sm.At.IsZero() {
		sm.At = time.Now()
	}
	sm.Detail = truncate(sm.Detail, detailLimit)
	s.mu.Lock()
	defer s.mu.Unlock()
	// A gap of zero switches the throttle off entirely, which is what tests
	// writing a month of history in a millisecond need. Written as a guard
	// rather than relying on the comparison: a sample stamped *earlier* than
	// the last one yields a negative difference, so `delta < 0` would refuse
	// every backfilled row at a gap of zero.
	if s.minGap > 0 {
		if prev, ok := s.last[sm.Network]; ok && sm.At.Sub(prev) < s.minGap {
			s.throttle++
			return false
		}
	}
	if len(s.buf) >= bufferLimit {
		s.dropped++
		return false
	}
	if prev, ok := s.last[sm.Network]; !ok || sm.At.After(prev) {
		s.last[sm.Network] = sm.At
	}
	s.buf = append(s.buf, sm)
	return true
}

// truncate cuts a detail to at most n bytes without splitting a rune.
//
// Byte slicing would be enough for the sentences diagnose writes, and wrong
// for the ones that carry an upstream error body: half a rune survives the
// database and comes back out of the JSON encoder as a replacement character,
// in the one field whose job is to say what broke.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Flush writes what is buffered. Safe on an empty buffer and safe to call
// concurrently with Record.
func (s *Store) Flush() {
	if s == nil {
		return
	}
	s.mu.Lock()
	batch := s.buf
	s.buf = make([]Sample, 0, 64)
	s.mu.Unlock()
	if len(batch) == 0 {
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("healthlog: begin: %v", err)
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO health_samples (ts, network, state, height, detail) VALUES (?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		log.Printf("healthlog: prepare: %v", err)
		return
	}
	for _, sm := range batch {
		if _, err := stmt.Exec(sm.At.UTC().Unix(), sm.Network, sm.State, sm.Height, sm.Detail); err != nil {
			stmt.Close()
			tx.Rollback()
			log.Printf("healthlog: insert: %v", err)
			return
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		log.Printf("healthlog: commit: %v", err)
		return
	}
	s.mu.Lock()
	s.written += int64(len(batch))
	s.mu.Unlock()
}

// Samples returns one network's rows in [since, until), oldest first.
//
// Flushes first. The caller is a page asking what happened, and the most
// interesting sample is always the one taken a moment ago.
func (s *Store) Samples(network string, since, until time.Time) ([]Sample, error) {
	if s == nil {
		return nil, nil
	}
	s.Flush()
	rows, err := s.db.Query(
		`SELECT ts, state, height, detail FROM health_samples
		 WHERE network = ? AND ts >= ? AND ts < ? ORDER BY ts`,
		network, since.UTC().Unix(), until.UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var ts int64
		sm := Sample{Network: network}
		if err := rows.Scan(&ts, &sm.State, &sm.Height, &sm.Detail); err != nil {
			return nil, err
		}
		sm.At = time.Unix(ts, 0).UTC()
		out = append(out, sm)
	}
	return out, rows.Err()
}

// Earliest reports the oldest sample held for a network, which is how far back
// any claim about it can reach. Zero time when there is none.
//
// The page needs this to tell "100% available over seven days" from "100%
// available over the eleven minutes since this process started", which look
// identical in a percentage and are not the same statement.
func (s *Store) Earliest(network string) (time.Time, error) {
	if s == nil {
		return time.Time{}, nil
	}
	s.Flush()
	var ts sql.NullInt64
	err := s.db.QueryRow(`SELECT MIN(ts) FROM health_samples WHERE network = ?`, network).Scan(&ts)
	if err != nil {
		return time.Time{}, err
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return time.Unix(ts.Int64, 0).UTC(), nil
}

// Prune deletes rows past the retention horizon and reports how many went. A
// no-op when retention is zero, which is the "keep everything" setting.
func (s *Store) Prune(now time.Time) (int64, error) {
	if s == nil || s.retentionDays <= 0 {
		return 0, nil
	}
	cutoff := now.UTC().AddDate(0, 0, -s.retentionDays).Unix()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM health_samples WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Run flushes and prunes until ctx is done, then flushes once more.
//
// The last flush is the point of the goroutine: deploys restart this process
// several times a day, and dropping the buffer each time would put a hole in
// the record at exactly the moment the record is most likely to be read.
func (s *Store) Run(ctx context.Context) {
	if s == nil {
		return
	}
	flush := time.NewTicker(5 * time.Second)
	defer flush.Stop()
	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()

	s.pruneAndLog()
	for {
		select {
		case <-ctx.Done():
			s.Flush()
			return
		case <-flush.C:
			s.Flush()
		case <-prune.C:
			s.pruneAndLog()
		}
	}
}

func (s *Store) pruneAndLog() {
	if n, err := s.Prune(time.Now()); err != nil {
		log.Printf("healthlog: prune: %v", err)
	} else if n > 0 {
		log.Printf("healthlog: pruned %d samples past %d days", n, s.retentionDays)
	}
}

// Stats reports the writer's own health, for the operator rather than the page.
type Stats struct {
	Buffered      int   `json:"buffered"`
	Written       int64 `json:"written"`
	Throttled     int64 `json:"throttled"`
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
		Throttled:     s.throttle,
		Dropped:       s.dropped,
		RetentionDays: s.retentionDays,
	}
}

// RetentionDays is what this store was opened with, so a response can say how
// far back its own numbers could possibly reach.
func (s *Store) RetentionDays() int {
	if s == nil {
		return 0
	}
	return s.retentionDays
}

// Close flushes and shuts the database.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.Flush()
	return s.db.Close()
}
