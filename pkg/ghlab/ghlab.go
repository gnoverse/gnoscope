// Package ghlab answers the questions about the gno ecosystem that the chain
// cannot: who is writing the code, at what rate, and which projects outside
// the monorepo have started depending on gno.
//
// Everything in this explorer until now reads a chain. A chain knows a realm
// was deployed and by whom; it does not know that the realm's author opened
// their first pull request against gnolang/gno three weeks earlier, and it has
// no idea a Go service on GitHub imports gnovm as a library and never touches
// gno.land at all. Those are the two halves here: activity on a curated set of
// ecosystem repositories, and discovery of repositories nobody curated.
//
// Why a separate database file, the same reasoning as pkg/traffic: this data
// is small, re-fetchable from GitHub in full at any time, and has nothing in
// common with the ~1.7 GB chain index but the process. Sharing a file would
// put a GitHub refresh behind the chain index's write lock and drag an
// entirely reconstructible table into every backup of the one thing that is
// expensive to rebuild.
//
// Nothing here is network-scoped, and that is deliberate rather than an
// oversight of the invariant in AGENTS.md. GitHub is not a chain. A repository
// is the same repository whichever gno.land network its realms end up on, and
// keying it by network would produce two rows that must always agree.
package ghlab

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the GitHub side of the explorer: a small SQLite file plus the
// queries that read it. Writes come from Sync, reads from query.go.
type Store struct {
	db *sql.DB
}

// Open prepares the GitHub database at path, creating it if absent.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the handle for tests. Not part of the package's contract.
func (s *Store) DB() *sql.DB { return s.db }

func initSchema(db *sql.DB) error {
	const ddl = `
-- One row per repository we know about, seeded or discovered.
CREATE TABLE IF NOT EXISTS gh_repos (
	full_name    TEXT PRIMARY KEY,
	owner        TEXT NOT NULL DEFAULT '',
	name         TEXT NOT NULL DEFAULT '',
	source       TEXT NOT NULL DEFAULT 'seed',   -- seed | discovered
	kind         TEXT NOT NULL DEFAULT '',       -- core | go-import | js-import | realm | topic
	tracked      INTEGER NOT NULL DEFAULT 0,     -- 1 = shown in the activity tables; every seed and discovered repo is walked for the score
	description  TEXT NOT NULL DEFAULT '',
	homepage     TEXT NOT NULL DEFAULT '',
	language     TEXT NOT NULL DEFAULT '',
	license      TEXT NOT NULL DEFAULT '',
	stars        INTEGER NOT NULL DEFAULT 0,
	forks        INTEGER NOT NULL DEFAULT 0,
	open_issues  INTEGER NOT NULL DEFAULT 0,
	archived     INTEGER NOT NULL DEFAULT 0,
	is_fork      INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL DEFAULT '',
	pushed_at    TEXT NOT NULL DEFAULT '',
	-- evidence is why this row claims to be gno-related: the search that found
	-- it and the file that matched. A discovery with no evidence is a guess,
	-- and a guess presented beside measured rows reads exactly like a fact.
	evidence     TEXT NOT NULL DEFAULT '',
	first_seen   TEXT NOT NULL DEFAULT '',
	last_seen    TEXT NOT NULL DEFAULT '',
	last_synced  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_gh_repos_kind   ON gh_repos(kind);
CREATE INDEX IF NOT EXISTS idx_gh_repos_seen   ON gh_repos(first_seen);
CREATE INDEX IF NOT EXISTS idx_gh_repos_pushed ON gh_repos(pushed_at);

-- One row per (repo, pull request). The window queries are all built on this:
-- opened, merged, time to merge, and first-ever merge per author.
CREATE TABLE IF NOT EXISTS gh_prs (
	full_name  TEXT    NOT NULL,
	number     INTEGER NOT NULL,
	title      TEXT    NOT NULL DEFAULT '',
	author     TEXT    NOT NULL DEFAULT '',
	state      TEXT    NOT NULL DEFAULT '',   -- open | closed
	draft      INTEGER NOT NULL DEFAULT 0,
	created_at TEXT    NOT NULL DEFAULT '',
	updated_at TEXT    NOT NULL DEFAULT '',
	merged_at  TEXT    NOT NULL DEFAULT '',
	closed_at  TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (full_name, number)
);
CREATE INDEX IF NOT EXISTS idx_gh_prs_merged  ON gh_prs(merged_at) WHERE merged_at <> '';
CREATE INDEX IF NOT EXISTS idx_gh_prs_created ON gh_prs(created_at);
CREATE INDEX IF NOT EXISTS idx_gh_prs_author  ON gh_prs(author);

-- All-time commit counts per contributor per repo, straight from GitHub's own
-- contributors endpoint. It carries no dates, which is why "new contributor"
-- is derived from gh_prs and not from here.
CREATE TABLE IF NOT EXISTS gh_contributors (
	full_name TEXT    NOT NULL,
	login     TEXT    NOT NULL,
	commits   INTEGER NOT NULL DEFAULT 0,
	avatar    TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (full_name, login)
);
CREATE INDEX IF NOT EXISTS idx_gh_contrib_login ON gh_contributors(login);

-- Who reviewed and who commented on each pull request, one row per review
-- submitted or comment posted, dated so a window can be scored as well as
-- all time. Rewritten whole each time a pull request is re-read, which is
-- what keeps an edited or deleted comment from being counted forever.
--
-- The author's own reviews and comments on their own pull request are never
-- written: replying to a reviewer is part of the pull request, already
-- counted once as the merge, and counting it again would pay people for long
-- threads on their own work.
CREATE TABLE IF NOT EXISTS gh_pr_events (
	full_name TEXT    NOT NULL,
	number    INTEGER NOT NULL,
	login     TEXT    NOT NULL,
	kind      TEXT    NOT NULL,   -- review | comment
	at        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_gh_pr_events_pr    ON gh_pr_events(full_name, number);
CREATE INDEX IF NOT EXISTS idx_gh_pr_events_login ON gh_pr_events(login);

-- Where each repository's pull-request walk stands. A first walk reads every
-- pull request a repository ever had, which for gnolang/gno is ~90 GraphQL
-- pages, and a pass that stops halfway (rate limit, restart) resumes from
-- cursor instead of starting over. watermark is when the last complete walk
-- started: the next one stops once it reaches pull requests not updated
-- since, so a steady-state pass costs a page per repository.
CREATE TABLE IF NOT EXISTS gh_walks (
	full_name   TEXT PRIMARY KEY,
	started     TEXT NOT NULL DEFAULT '',
	cursor      TEXT NOT NULL DEFAULT '',
	watermark   TEXT NOT NULL DEFAULT '',
	last_walked TEXT NOT NULL DEFAULT ''
);

-- gh_prs holds every walked repository, discovered ones included, because
-- the contributor score reads all of them. Every window figure on the page
-- (opened, merged, median, new contributors, recent) is about the curated
-- set, and reads it through this view so that adding a repository to the
-- score never quietly changes what "merged this month" means.
CREATE VIEW IF NOT EXISTS gh_tracked_prs AS
	SELECT p.* FROM gh_prs p JOIN gh_repos r ON r.full_name = p.full_name WHERE r.tracked = 1;

-- Free-form sync bookkeeping: last run, per-stage timings, rate-limit state
-- and the last error, so the page can say why it is showing stale numbers
-- instead of showing them silently.
CREATE TABLE IF NOT EXISTS gh_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);
`
	_, err := db.Exec(ddl)
	return err
}

// --- meta ---

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO gh_meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) Meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM gh_meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// AllMeta returns every bookkeeping key, for /api/lab/github/overview.
func (s *Store) AllMeta() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM gh_meta`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// --- writes ---

// Repo is one repository row, as written by the sync and read by the API.
//
// Private is not a column and never reaches the database: it is the flag a
// caller checks before writing, so a repository the token can see and the
// public cannot is dropped rather than stored and filtered later. A filter is
// something a query can forget; a row that was never written cannot leak.
type Repo struct {
	Private     bool   `json:"-"`
	FullName    string `json:"full_name"`
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	Tracked     bool   `json:"tracked"`
	Description string `json:"description"`
	Homepage    string `json:"homepage,omitempty"`
	Language    string `json:"language,omitempty"`
	License     string `json:"license,omitempty"`
	Stars       int    `json:"stars"`
	Forks       int    `json:"forks"`
	OpenIssues  int    `json:"open_issues"`
	Archived    bool   `json:"archived"`
	Fork        bool   `json:"fork"`
	CreatedAt   string `json:"created_at,omitempty"`
	PushedAt    string `json:"pushed_at,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	FirstSeen   string `json:"first_seen,omitempty"`
	LastSeen    string `json:"last_seen,omitempty"`
}

// UpsertRepo writes a repository, preserving first_seen on a row that already
// exists. first_seen is the only column here that cannot be re-derived from
// GitHub: it says when *this instance* first saw the repository, which is what
// makes "discovered this week" a real answer rather than a sort by created_at.
func (s *Store) UpsertRepo(r Repo, now time.Time) error {
	// The last line of defence against the leak described on Repo.Private,
	// and an error rather than a silent skip: a caller that arrives here with
	// a private repository has a bug worth seeing in a log, not a row worth
	// quietly dropping.
	if r.Private {
		return fmt.Errorf("ghlab: refusing to store private repository %q", r.FullName)
	}
	ts := now.UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
INSERT INTO gh_repos(full_name,owner,name,source,kind,tracked,description,homepage,language,license,
                     stars,forks,open_issues,archived,is_fork,created_at,pushed_at,evidence,
                     first_seen,last_seen,last_synced)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(full_name) DO UPDATE SET
  owner=excluded.owner, name=excluded.name, kind=excluded.kind,
  tracked=MAX(gh_repos.tracked, excluded.tracked),
  description=excluded.description, homepage=excluded.homepage,
  language=excluded.language, license=excluded.license,
  stars=excluded.stars, forks=excluded.forks, open_issues=excluded.open_issues,
  archived=excluded.archived, is_fork=excluded.is_fork,
  created_at=excluded.created_at, pushed_at=excluded.pushed_at,
  evidence=CASE WHEN excluded.evidence <> '' THEN excluded.evidence ELSE gh_repos.evidence END,
  -- source never downgrades: a repo we curated stays 'seed' even if a search
  -- rediscovers it, because the curated list is the stronger claim.
  source=CASE WHEN gh_repos.source='seed' THEN 'seed' ELSE excluded.source END,
  last_seen=excluded.last_seen, last_synced=excluded.last_synced`,
		r.FullName, r.Owner, r.Name, r.Source, r.Kind, b2i(r.Tracked), r.Description, r.Homepage,
		r.Language, r.License, r.Stars, r.Forks, r.OpenIssues, b2i(r.Archived), b2i(r.Fork),
		r.CreatedAt, r.PushedAt, r.Evidence, ts, ts, ts)
	return err
}

// PR is one pull request row.
type PR struct {
	FullName  string `json:"repo"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	State     string `json:"state"`
	Draft     bool   `json:"draft"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at,omitempty"`
	MergedAt  string `json:"merged_at,omitempty"`
	ClosedAt  string `json:"closed_at,omitempty"`
}

func (s *Store) UpsertPRs(prs []PR) error {
	if len(prs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
INSERT INTO gh_prs(full_name,number,title,author,state,draft,created_at,updated_at,merged_at,closed_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(full_name,number) DO UPDATE SET
  title=excluded.title, author=excluded.author, state=excluded.state, draft=excluded.draft,
  updated_at=excluded.updated_at, merged_at=excluded.merged_at, closed_at=excluded.closed_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range prs {
		if _, err := stmt.Exec(p.FullName, p.Number, p.Title, p.Author, p.State, b2i(p.Draft),
			p.CreatedAt, p.UpdatedAt, p.MergedAt, p.ClosedAt); err != nil {
			return fmt.Errorf("upsert pr %s#%d: %w", p.FullName, p.Number, err)
		}
	}
	return tx.Commit()
}

// Contributor is one (repo, login) commit count.
type Contributor struct {
	FullName string `json:"repo"`
	Login    string `json:"login"`
	Commits  int    `json:"commits"`
	Avatar   string `json:"avatar,omitempty"`
}

// ReplaceContributors rewrites one repo's contributor rows in a transaction.
//
// Replace rather than upsert: GitHub's list is authoritative and shrinks when
// a contributor's commits are rewritten out of history. Upserting would leave
// a login behind forever, and a phantom contributor in a "top contributors"
// table is the kind of wrong that nobody reports because it looks plausible.
func (s *Store) ReplaceContributors(fullName string, cs []Contributor) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM gh_contributors WHERE full_name=?`, fullName); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO gh_contributors(full_name,login,commits,avatar) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range cs {
		if _, err := stmt.Exec(fullName, c.Login, c.Commits, c.Avatar); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PREvent is one review or comment on a pull request, by somebody other than
// its author.
type PREvent struct {
	FullName string
	Number   int
	Login    string
	Kind     string // review | comment
	At       string
}

// StorePage writes one page of a pull-request walk: the pull requests, and
// for each of them its full set of reviews and comments, replacing what was
// there. One transaction, so a pull request is never stored without its
// events or with half of them.
func (s *Store) StorePage(prs []PR, evs []PREvent) error {
	if err := s.UpsertPRs(prs); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range prs {
		if _, err := tx.Exec(`DELETE FROM gh_pr_events WHERE full_name=? AND number=?`, p.FullName, p.Number); err != nil {
			return err
		}
	}
	stmt, err := tx.Prepare(`INSERT INTO gh_pr_events(full_name,number,login,kind,at) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range evs {
		if _, err := stmt.Exec(e.FullName, e.Number, e.Login, e.Kind, e.At); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Walk is where one repository's pull-request walk stands. See gh_walks.
type Walk struct {
	Started    string
	Cursor     string
	Watermark  string
	LastWalked string
}

func (s *Store) Walk(fullName string) (Walk, error) {
	var w Walk
	err := s.db.QueryRow(`SELECT started,cursor,watermark,last_walked FROM gh_walks WHERE full_name=?`, fullName).
		Scan(&w.Started, &w.Cursor, &w.Watermark, &w.LastWalked)
	if errors.Is(err, sql.ErrNoRows) {
		return Walk{}, nil
	}
	return w, err
}

func (s *Store) SetWalk(fullName string, w Walk) error {
	_, err := s.db.Exec(`INSERT INTO gh_walks(full_name,started,cursor,watermark,last_walked) VALUES(?,?,?,?,?)
		ON CONFLICT(full_name) DO UPDATE SET started=excluded.started, cursor=excluded.cursor,
		  watermark=excluded.watermark, last_walked=excluded.last_walked`,
		fullName, w.Started, w.Cursor, w.Watermark, w.LastWalked)
	return err
}

// ScoreQueue lists the discovered non-fork repositories, least recently
// walked first, so a budget that cannot reach all of them in one pass reaches
// the rest in the next. Seeds are not here: the curated pass walks every one
// of them, tracked or not, because it already holds their metadata.
func (s *Store) ScoreQueue() ([]string, error) {
	rows, err := s.db.Query(`SELECT r.full_name FROM gh_repos r
		LEFT JOIN gh_walks w ON w.full_name = r.full_name
		WHERE r.source = 'discovered' AND r.is_fork = 0
		ORDER BY COALESCE(w.last_walked, ''), r.full_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
