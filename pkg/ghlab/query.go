package ghlab

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// TouchDiscovery refreshes the claim on a repository already in the table
// without spending a request on its metadata.
func (s *Store) TouchDiscovery(fullName, kind, evidence string, now time.Time) error {
	ts := now.UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE gh_repos
		SET kind = CASE WHEN kind='' THEN ? ELSE kind END,
		    evidence = CASE WHEN evidence='' THEN ? ELSE evidence END,
		    last_seen = ?
		WHERE full_name = ?`, kind, evidence, ts, fullName)
	return err
}

// Overview is what /api/lab/github/overview answers: the size of the picture,
// plus enough sync bookkeeping that a reader can tell fresh from stale.
type Overview struct {
	Repos        int         `json:"repos"`
	Tracked      int         `json:"tracked"`
	Discovered   int         `json:"discovered"`
	Contributors int         `json:"contributors"`
	PRs          int         `json:"prs"`
	ByKind       []KindCount `json:"by_kind"`
	Window       WindowStats `json:"window"`
	// Years holds at least one tracked pull request each, newest first: the
	// windows a reader can pick besides all time.
	Years []int             `json:"years"`
	Meta  map[string]string `json:"meta"`
}

// WindowStats are the counts over the requested window.
type WindowStats struct {
	Window
	PRsOpened       int     `json:"prs_opened"`
	PRsMerged       int     `json:"prs_merged"`
	Authors         int     `json:"authors"`
	NewContributors int     `json:"new_contributors"`
	MedianMergeHrs  float64 `json:"median_merge_hours"`
	ReposDiscovered int     `json:"repos_discovered"`
}

// KindCount is one bucket of the repository table.
type KindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

func (s *Store) Overview(win Window) (Overview, error) {
	var o Overview
	o.Meta = map[string]string{}
	row := s.db.QueryRow(`SELECT COUNT(*),
		SUM(CASE WHEN tracked=1 THEN 1 ELSE 0 END),
		SUM(CASE WHEN source='discovered' THEN 1 ELSE 0 END) FROM gh_repos`)
	var tracked, disc sql.NullInt64
	if err := row.Scan(&o.Repos, &tracked, &disc); err != nil {
		return o, err
	}
	o.Tracked, o.Discovered = int(tracked.Int64), int(disc.Int64)
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT c.login) FROM gh_contributors c
		JOIN gh_repos r ON r.full_name = c.full_name WHERE r.tracked = 1`).Scan(&o.Contributors); err != nil {
		return o, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM gh_tracked_prs`).Scan(&o.PRs); err != nil {
		return o, err
	}
	kinds, err := s.db.Query(`SELECT CASE WHEN kind='' THEN '(unclassified)' ELSE kind END AS k,
		COUNT(*) FROM gh_repos GROUP BY k ORDER BY COUNT(*) DESC`)
	if err != nil {
		return o, err
	}
	defer kinds.Close()
	for kinds.Next() {
		var kc KindCount
		if err := kinds.Scan(&kc.Kind, &kc.Count); err != nil {
			return o, err
		}
		o.ByKind = append(o.ByKind, kc)
	}
	if err := kinds.Err(); err != nil {
		return o, err
	}
	ws, err := s.windowStats(win)
	if err != nil {
		return o, err
	}
	o.Window = ws
	if o.Years, err = s.Years(); err != nil {
		return o, err
	}
	meta, err := s.AllMeta()
	if err != nil {
		return o, err
	}
	for k, v := range meta {
		// query_total:* is one key per discovery query and would swamp a
		// response nobody reads it from. It is on /repos, beside the results.
		if strings.HasPrefix(k, "query_total:") {
			continue
		}
		o.Meta[k] = v
	}
	return o, nil
}

// notBot is the SQL half of IsBot, for the counts that must not include a
// machine. It cannot call IsBot, so the two are kept side by side and
// TestNotBotSQLMatchesIsBot fails if they disagree about any known login: a
// bot slipping into "authors this month" inflates the one number on the page
// that is meant to count people.
const notBot = `author NOT LIKE '%[bot]' AND author NOT LIKE '%-bot' AND
	LOWER(author) NOT IN ('dependabot','github-actions','renovate','codecov','coderabbitai',
	                      'gnolang-bot','mergify','allcontributors','semantic-release',
	                      'web-flow','copilot-swe-agent','gno2d2')`

func (s *Store) windowStats(win Window) (WindowStats, error) {
	w := WindowStats{Window: win}
	created, cArgs := win.in("created_at")
	merged, mArgs := win.in("merged_at")
	seen, sArgs := win.in("first_seen")
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM gh_tracked_prs WHERE `+created, cArgs...).Scan(&w.PRsOpened); err != nil {
		return w, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM gh_tracked_prs WHERE merged_at <> '' AND `+merged, mArgs...).Scan(&w.PRsMerged); err != nil {
		return w, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT author) FROM gh_tracked_prs
		WHERE author <> '' AND `+notBot+` AND `+created, cArgs...).Scan(&w.Authors); err != nil {
		return w, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM gh_repos
		WHERE source='discovered' AND `+seen, sArgs...).Scan(&w.ReposDiscovered); err != nil {
		return w, err
	}
	news, err := s.NewContributors(win, 0)
	if err != nil {
		return w, err
	}
	w.NewContributors = len(news)
	w.MedianMergeHrs, err = s.medianMergeHours("", win)
	return w, err
}

// medianMergeHours is the middle time-to-merge over the window, across every
// tracked repository when repo is empty.
//
// Median rather than mean, and it is not a refinement: one pull request open
// for two years and merged last Tuesday moves a mean over a hundred PRs by
// several days, and that shape is normal on gnolang/gno rather than rare.
func (s *Store) medianMergeHours(repo string, win Window) (float64, error) {
	merged, args := win.in("merged_at")
	q := `SELECT created_at, merged_at FROM gh_tracked_prs WHERE merged_at <> '' AND ` + merged
	if repo != "" {
		q += ` AND full_name = ?`
		args = append(args, repo)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var hrs []float64
	for rows.Next() {
		var c, m string
		if err := rows.Scan(&c, &m); err != nil {
			return 0, err
		}
		ct, err1 := time.Parse(time.RFC3339, c)
		mt, err2 := time.Parse(time.RFC3339, m)
		if err1 != nil || err2 != nil || !mt.After(ct) {
			continue
		}
		hrs = append(hrs, mt.Sub(ct).Hours())
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(hrs) == 0 {
		return 0, nil
	}
	sort.Float64s(hrs)
	mid := len(hrs) / 2
	if len(hrs)%2 == 1 {
		return round1(hrs[mid]), nil
	}
	return round1((hrs[mid-1] + hrs[mid]) / 2), nil
}

func round1(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}

// NewContributor is somebody whose first merged pull request across every
// tracked repository landed inside the window.
//
// "First merged", not "first opened" and not "first commit". Opening a pull
// request is not contributing yet and the number would count drive-by
// duplicates; a first *commit* would be the better definition and GitHub's
// contributors endpoint does not carry dates, so getting it would mean
// walking every commit of every repository. Merged is the honest answer this
// data can support, and the page says so rather than calling it "new
// contributors" and leaving a reader to assume.
type NewContributor struct {
	Login     string `json:"login"`
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	MergedAt  string `json:"merged_at"`
	Merged    int    `json:"merged_total"`
	AvatarURL string `json:"avatar,omitempty"`
}

// NewContributors lists first-ever merges inside the window, newest first.
// limit <= 0 returns all of them.
func (s *Store) NewContributors(win Window, limit int) ([]NewContributor, error) {
	// The inner query is every author's earliest merge over the whole table,
	// not over the window. Filtering first and taking a minimum second is the
	// bug this shape avoids: it would call every author who merged something
	// this month "new", including people who have been merging since 2021.
	q := `
WITH firsts AS (
  SELECT author, MIN(merged_at) AS first_merge
  FROM gh_tracked_prs
  WHERE merged_at <> '' AND author <> ''
  GROUP BY author
),
totals AS (
  SELECT author, COUNT(*) AS n FROM gh_tracked_prs WHERE merged_at <> '' GROUP BY author
)
SELECT f.author, p.full_name, p.number, p.title, f.first_merge, totals.n
FROM firsts f
JOIN gh_tracked_prs p ON p.author = f.author AND p.merged_at = f.first_merge
JOIN totals ON totals.author = f.author
WHERE f.first_merge >= ? AND f.first_merge < ?
ORDER BY f.first_merge DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.Query(q, win.Since, win.Until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NewContributor{}
	seen := map[string]bool{}
	for rows.Next() {
		var n NewContributor
		if err := rows.Scan(&n.Login, &n.Repo, &n.Number, &n.Title, &n.MergedAt, &n.Merged); err != nil {
			return nil, err
		}
		// Two pull requests merged in the same second by the same author are
		// two rows out of that join, and one person is one new contributor.
		if seen[n.Login] || IsBot(n.Login) {
			continue
		}
		seen[n.Login] = true
		out = append(out, n)
	}
	return out, rows.Err()
}

// RepoActivity is one tracked repository's pull-request rate over a window.
type RepoActivity struct {
	FullName       string  `json:"repo"`
	Opened         int     `json:"opened"`
	Merged         int     `json:"merged"`
	Open           int     `json:"open_now"`
	Authors        int     `json:"authors"`
	MedianMergeHrs float64 `json:"median_merge_hours"`
	Stars          int     `json:"stars"`
	PushedAt       string  `json:"pushed_at,omitempty"`
}

func (s *Store) RepoActivity(win Window) ([]RepoActivity, error) {
	lo, hi := win.Since, win.Until
	rows, err := s.db.Query(`
SELECT r.full_name, r.stars, r.pushed_at,
  (SELECT COUNT(*) FROM gh_prs p WHERE p.full_name=r.full_name AND p.created_at >= ? AND p.created_at < ?),
  (SELECT COUNT(*) FROM gh_prs p WHERE p.full_name=r.full_name AND p.merged_at <> '' AND p.merged_at >= ? AND p.merged_at < ?),
  (SELECT COUNT(*) FROM gh_prs p WHERE p.full_name=r.full_name AND p.state='open'),
  (SELECT COUNT(DISTINCT p.author) FROM gh_prs p WHERE p.full_name=r.full_name AND p.created_at >= ? AND p.created_at < ? AND p.author NOT LIKE '%[bot]' AND p.author NOT LIKE '%-bot')
FROM gh_repos r
WHERE r.tracked=1
ORDER BY 4 DESC, r.stars DESC`, lo, hi, lo, hi, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RepoActivity{}
	for rows.Next() {
		var a RepoActivity
		if err := rows.Scan(&a.FullName, &a.Stars, &a.PushedAt, &a.Opened, &a.Merged, &a.Open, &a.Authors); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		h, err := s.medianMergeHours(out[i].FullName, win)
		if err != nil {
			return nil, err
		}
		out[i].MedianMergeHrs = h
	}
	return out, nil
}

// RecentPRs lists merged or open pull requests across tracked repositories.
// state is "merged", "open" or "" for both, newest activity first. "open" is
// what is open now, whatever the window: a pull request opened in 2024 and
// still waiting is not a 2024 fact.
func (s *Store) RecentPRs(win Window, limit int, state string) ([]PR, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	created, cArgs := win.in("created_at")
	merged, mArgs := win.in("merged_at")
	var (
		where string
		args  []any
	)
	switch state {
	case "merged":
		where, args = `merged_at <> '' AND `+merged, mArgs
	case "open":
		where, args = `state='open' AND draft=0`, nil
	default:
		where, args = `((`+created+`) OR (merged_at <> '' AND `+merged+`))`, append(cArgs, mArgs...)
	}
	q := `SELECT full_name,number,title,author,state,draft,created_at,updated_at,merged_at,closed_at
		FROM gh_tracked_prs WHERE ` + where + ` ORDER BY COALESCE(NULLIF(merged_at,''), updated_at) DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PR{}
	for rows.Next() {
		var p PR
		var draft int
		if err := rows.Scan(&p.FullName, &p.Number, &p.Title, &p.Author, &p.State, &draft,
			&p.CreatedAt, &p.UpdatedAt, &p.MergedAt, &p.ClosedAt); err != nil {
			return nil, err
		}
		p.Draft = draft == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// Repos lists the repository table. kind and source filter it; both empty
// returns everything, newest push first.
func (s *Store) Repos(kind, source string, limit int) ([]Repo, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	q := `SELECT full_name,owner,name,source,kind,tracked,description,homepage,language,license,
	       stars,forks,open_issues,archived,is_fork,created_at,pushed_at,evidence,first_seen,last_seen
	      FROM gh_repos WHERE 1=1`
	var args []any
	if kind != "" {
		q += " AND kind = ?"
		args = append(args, kind)
	}
	if source != "" {
		q += " AND source = ?"
		args = append(args, source)
	}
	q += " ORDER BY pushed_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repo{}
	for rows.Next() {
		var r Repo
		var tracked, archived, fork int
		if err := rows.Scan(&r.FullName, &r.Owner, &r.Name, &r.Source, &r.Kind, &tracked,
			&r.Description, &r.Homepage, &r.Language, &r.License, &r.Stars, &r.Forks,
			&r.OpenIssues, &archived, &fork, &r.CreatedAt, &r.PushedAt, &r.Evidence,
			&r.FirstSeen, &r.LastSeen); err != nil {
			return nil, err
		}
		r.Tracked, r.Archived, r.Fork = tracked == 1, archived == 1, fork == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// QueryTotals reports what each discovery query said GitHub's whole index
// holds, which is larger than what any of them can hand back: search caps at
// 1,000 results. Printed beside the table so a reader is not told 312 when
// the number is 1,504.
func (s *Store) QueryTotals() ([]QueryTotal, error) {
	meta, err := s.AllMeta()
	if err != nil {
		return nil, err
	}
	out := []QueryTotal{}
	for _, dq := range DiscoveryQueries {
		v, ok := meta["query_total:"+dq.Query]
		if !ok {
			continue
		}
		var n int
		fmt.Sscanf(v, "%d", &n)
		out = append(out, QueryTotal{Kind: dq.Kind, Query: dq.Query, Why: dq.Why, Total: n})
	}
	return out, nil
}

// QueryTotal is one discovery query and the size of its haystack.
type QueryTotal struct {
	Kind  string `json:"kind"`
	Query string `json:"query"`
	Why   string `json:"why"`
	Total int    `json:"total"`
}
