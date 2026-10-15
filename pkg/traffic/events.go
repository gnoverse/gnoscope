package traffic

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// Events record what a reader *did* on a page, which the request log
// structurally cannot see.
//
// A request row says a page was served. It cannot say whether the reader read
// it or bounced off it in two seconds, how far down they got, which tab they
// opened once they were there, what they typed into the search box, or where
// they went next. Those are the questions "improve the app from how it is
// used" is actually made of, and every one of them is answered on the client
// or nowhere.
//
// The privacy design is the one the request log already set, unchanged: the
// only identity is the same daily keyed hash (visitor.go), derived server-side
// from the IP the connection arrived on, never sent by the page. The client
// sends behaviour, not identity. Nothing here is exposed per-row: the
// dashboard reads aggregates, same as /api/traffic.
//
// Everything the client sends is clamped on arrival. A beacon endpoint is the
// one piece of this server anybody on the internet can write to, so the rule
// is that no field reaches SQLite at a size or a value the server did not
// choose: a dwell of a year, a megabyte of "action label" and a scroll depth
// of -1 are all things a browser will never send and a curl will.
const (
	// maxDwellMS caps a visit at four hours. Dwell is measured as wall time
	// between opening a page and leaving it, so a tab left open overnight
	// reports a real number that describes a laptop lid, not a reader. Four
	// hours is past any plausible reading of an explorer page and still small
	// enough that one clamped row cannot move a median.
	maxDwellMS = 4 * 60 * 60 * 1000
	// maxActions bounds the interaction counter. Past this the number stops
	// meaning "engaged" and starts meaning "a script".
	maxActions = 1000
	// maxValueLen bounds a search term or an error message. Both are free
	// text from the page.
	maxValueLen = 80
	// maxLabelLen bounds an action label. Labels are written by this app's own
	// markup and are short by construction; the cap is for everyone else.
	maxLabelLen = 64
	// eventBufferLimit is the events half of bufferLimit. Smaller, because a
	// page produces events in the ones per minute where a crawler produces
	// requests in the thousands per second, and because dropping the whole
	// buffer is the failure mode to avoid: a cap that is never reached cannot
	// protect anything, and one that is reached silently truncates the day.
	eventBufferLimit = 20000
)

// EventKinds are the only kinds that reach disk. An unknown kind is dropped
// rather than stored, because a column whose values nobody enumerated is a
// column no query can trust.
var EventKinds = map[string]bool{
	// leave: one visit to one page, reported when the reader navigates away,
	// hides the tab or closes it. The row that carries dwell and scroll.
	"leave": true,
	// action: one deliberate interaction, named by the markup that owns it
	// (a tab, a filter, a chart control, a copy button, an outbound link).
	"action": true,
	// search: what somebody typed, and how many results it found. A term with
	// zero results is a missing feature with a name on it.
	"search": true,
	// error: a JavaScript error or an unhandled rejection. The reader saw a
	// broken page and did not file an issue about it; this is the only way
	// anyone finds out.
	"error": true,
}

// Event is one thing a reader did, already reduced to what may be stored.
//
// Same contract as Record: every field is bounded, public, or a hash. What is
// deliberately absent is the same list, plus anything the page could have put
// in it that would identify a person: there is no free-text field here except
// a search term and an error message, both clipped, and neither is joined to
// anything but a daily hash.
type Event struct {
	At      time.Time
	Visitor string
	Host    string
	Kind    string // leave | action | search | error
	// Path is the app path the event happened on, already the SPA's own path
	// rather than a URL: "/realm/r/moul/home", not the document that served it.
	Path       string
	PageKind   string
	EntityKind string
	Entity     string
	Network    string
	// From is the path the reader came from, so a sequence of visits is an
	// edge list without a session table. Empty on the first page of a visit,
	// which is exactly what makes it an entry point.
	From string
	// Action is the label for an action event, and the sub-kind for the rest:
	// an error carries the kind of error, a search carries where the box was.
	Action string
	// Value is the search term or the error message. Clipped, and never
	// returned per-row by any endpoint.
	Value     string
	DwellMS   int64
	ScrollPct int
	// Actions is how many interactions happened during the visit this leave
	// event closes. Dwell says how long somebody stayed; this says whether
	// they did anything while they were there, which is the difference
	// between reading and a tab left open.
	Actions int
	Results int // result count for a search
	Client  string
	Robot   bool
}

// normalize clamps everything a client sent and reports whether the event may
// be stored at all. Called on arrival, before the event reaches the buffer.
func (e *Event) normalize() bool {
	e.Kind = strings.ToLower(strings.TrimSpace(e.Kind))
	if !EventKinds[e.Kind] {
		return false
	}
	e.Path = PageTarget(clip(strings.TrimSpace(e.Path)))
	e.From = clip(strings.TrimSpace(e.From))
	if e.From == e.Path {
		// A tab switch inside a page replaces the URL without leaving it. An
		// edge from a page to itself is not navigation and would be the
		// loudest row in the panel.
		e.From = ""
	}
	e.PageKind = clipTo(e.PageKind, maxLabelLen)
	e.EntityKind = clipTo(e.EntityKind, maxLabelLen)
	e.Entity = clip(e.Entity)
	e.Network = clipTo(e.Network, maxLabelLen)
	e.Action = clipTo(strings.TrimSpace(e.Action), maxLabelLen)
	e.Value = clipTo(strings.TrimSpace(e.Value), maxValueLen)
	if e.DwellMS < 0 {
		e.DwellMS = 0
	}
	if e.DwellMS > maxDwellMS {
		e.DwellMS = maxDwellMS
	}
	if e.ScrollPct < 0 {
		e.ScrollPct = 0
	}
	if e.ScrollPct > 100 {
		e.ScrollPct = 100
	}
	if e.Actions < 0 {
		e.Actions = 0
	}
	if e.Actions > maxActions {
		e.Actions = maxActions
	}
	if e.Results < 0 {
		e.Results = 0
	}
	// A search with no term is a keystroke, not a search, and an action with
	// no label cannot be grouped by anything.
	switch e.Kind {
	case "search":
		if e.Value == "" {
			return false
		}
	case "action":
		if e.Action == "" {
			return false
		}
	case "error":
		if e.Value == "" {
			return false
		}
	}
	return true
}

// clipTo is clip with a caller-chosen bound. clip's own bound is sized for a
// realm path; a label and a search term are smaller things.
func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

const eventsDDL = `
CREATE TABLE IF NOT EXISTS events (
	ts       INTEGER NOT NULL,
	day      TEXT    NOT NULL,
	hour     INTEGER NOT NULL,
	visitor  TEXT    NOT NULL DEFAULT '',
	host     TEXT    NOT NULL DEFAULT '',
	kind     TEXT    NOT NULL DEFAULT '',
	path     TEXT    NOT NULL DEFAULT '',
	page_kind   TEXT NOT NULL DEFAULT '',
	entity_kind TEXT NOT NULL DEFAULT '',
	entity      TEXT NOT NULL DEFAULT '',
	network  TEXT    NOT NULL DEFAULT '',
	from_path TEXT   NOT NULL DEFAULT '',
	action   TEXT    NOT NULL DEFAULT '',
	value    TEXT    NOT NULL DEFAULT '',
	dwell_ms INTEGER NOT NULL DEFAULT 0,
	scroll_pct INTEGER NOT NULL DEFAULT 0,
	actions  INTEGER NOT NULL DEFAULT 0,
	results  INTEGER NOT NULL DEFAULT 0,
	client   TEXT    NOT NULL DEFAULT '',
	robot    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_events_day       ON events(day);
CREATE INDEX IF NOT EXISTS idx_events_ts        ON events(ts);
CREATE INDEX IF NOT EXISTS idx_events_day_kind  ON events(day, kind);
CREATE INDEX IF NOT EXISTS idx_events_day_act   ON events(day, action) WHERE action <> '';
CREATE INDEX IF NOT EXISTS idx_events_day_pk    ON events(day, page_kind) WHERE page_kind <> '';
`

func initEventSchema(db *sql.DB) error {
	_, err := db.Exec(eventsDDL)
	return err
}

// RecordEvent buffers one event. Same contract as Record: never blocks on I/O,
// never returns an error, safe on a nil Store, and drops rather than growing
// without bound.
func (s *Store) RecordEvent(e Event) {
	if s == nil {
		return
	}
	if !e.normalize() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.evbuf) >= eventBufferLimit {
		s.dropped++
		return
	}
	s.evbuf = append(s.evbuf, e)
}

// flushEvents writes the buffered events. Called by Flush, inside the same
// write lock, so a dashboard read that flushes first sees both halves or
// neither.
func (s *Store) flushEvents(batch []Event) {
	if len(batch) == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("traffic: begin events: %v", err)
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO events
		(ts, day, hour, visitor, host, kind, path, page_kind, entity_kind, entity,
		 network, from_path, action, value, dwell_ms, scroll_pct, actions, results, client, robot)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		log.Printf("traffic: prepare events: %v", err)
		return
	}
	for _, e := range batch {
		utc := e.At.UTC()
		robot := 0
		if e.Robot {
			robot = 1
		}
		if _, err := stmt.Exec(
			utc.Unix(), utc.Format("2006-01-02"), utc.Hour(),
			e.Visitor, e.Host, e.Kind, e.Path, e.PageKind, e.EntityKind, e.Entity,
			e.Network, e.From, e.Action, e.Value, e.DwellMS, e.ScrollPct, e.Actions,
			e.Results, e.Client, robot,
		); err != nil {
			stmt.Close()
			tx.Rollback()
			log.Printf("traffic: insert event: %v", err)
			return
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		log.Printf("traffic: commit events: %v", err)
		return
	}
	s.mu.Lock()
	s.written += int64(len(batch))
	s.mu.Unlock()
}

// pruneEvents applies the same retention as the request log. Separate
// statement, same cutoff: two tables in one file with two retentions would be
// a promise nobody could read off the flag.
func (s *Store) pruneEvents(cutoff string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM events WHERE day < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune events: %w", err)
	}
	return res.RowsAffected()
}
