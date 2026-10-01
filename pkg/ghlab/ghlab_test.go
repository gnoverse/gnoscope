package ghlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "gh.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// openTracked is openTest plus a tracked o/r, the fixture repository the
// window tests write into: the window figures read gh_tracked_prs, and a
// pull request in a repository with no gh_repos row is invisible to them.
func openTracked(t *testing.T) *Store {
	t.Helper()
	s := openTest(t)
	addRepo(t, s, "o/r", "seed", true)
	return s
}

func addRepo(t *testing.T, s *Store, name, source string, tracked bool) {
	t.Helper()
	owner, n, _ := strings.Cut(name, "/")
	if err := s.UpsertRepo(Repo{FullName: name, Owner: owner, Name: n, Source: source, Tracked: tracked}, time.Now()); err != nil {
		t.Fatalf("add repo %s: %v", name, err)
	}
}

func ago(days int) string {
	return time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
}

func seedPR(t *testing.T, s *Store, repo string, n int, author string, createdDays, mergedDays int) {
	t.Helper()
	p := PR{FullName: repo, Number: n, Title: fmt.Sprintf("pr %d", n), Author: author,
		State: "closed", CreatedAt: ago(createdDays), UpdatedAt: ago(mergedDays)}
	if mergedDays >= 0 {
		p.MergedAt = ago(mergedDays)
	} else {
		p.State = "open"
		p.UpdatedAt = ago(createdDays)
	}
	if err := s.UpsertPRs([]PR{p}); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
}

func TestIsBot(t *testing.T) {
	tests := []struct {
		login string
		want  bool
	}{
		{"moul", false},
		{"thehowl", false},
		{"dependabot[bot]", true},
		{"Dependabot", true},
		{"github-actions[bot]", true},
		{"gnolang-bot", true},
		{"renovate", true},
		{"", true},
		{"  ", true},
		{"robot-arm", false}, // -bot is a suffix rule, not a substring one
	}
	for _, tc := range tests {
		if got := IsBot(tc.login); got != tc.want {
			t.Errorf("IsBot(%q) = %v, want %v", tc.login, got, tc.want)
		}
	}
}

// TestNotBotSQLMatchesIsBot keeps the SQL half of the bot filter honest.
//
// notBot is a string of SQL that cannot call IsBot, so the two can drift, and
// the drift is invisible: a bot counted as an author just makes "authors this
// month" one larger than it should be. This runs every login the Go side
// knows about through the SQL side and requires the same verdict.
func TestNotBotSQLMatchesIsBot(t *testing.T) {
	s := openTest(t)
	logins := []string{"moul", "thehowl", "robot-arm", "gnolang-bot", "renovate", "web-flow"}
	for l := range botLogins {
		logins = append(logins, l)
	}
	for i, l := range logins {
		if err := s.UpsertPRs([]PR{{FullName: "o/r", Number: i + 1, Author: l,
			CreatedAt: ago(1), MergedAt: ago(1), State: "closed"}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.db.Query(`SELECT author FROM gh_prs WHERE ` + notBot)
	if err != nil {
		t.Fatalf("notBot query: %v", err)
	}
	defer rows.Close()
	passed := map[string]bool{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		passed[a] = true
	}
	for _, l := range logins {
		if IsBot(l) == passed[l] {
			t.Errorf("login %q: IsBot=%v but notBot SQL passed=%v; the two filters disagree",
				l, IsBot(l), passed[l])
		}
	}
}

// TestNewContributorsIsFirstEverNotFirstInWindow is the whole point of the
// cohort: somebody who has been merging for years and merged again this month
// is not a new contributor, and the obvious query says they are.
func TestNewContributorsIsFirstEverNotFirstInWindow(t *testing.T) {
	s := openTracked(t)
	// A veteran: first merge 400 days ago, another one yesterday.
	seedPR(t, s, "o/r", 1, "veteran", 401, 400)
	seedPR(t, s, "o/r", 2, "veteran", 2, 1)
	// A newcomer: nothing before this month.
	seedPR(t, s, "o/r", 3, "newcomer", 5, 3)
	// A bot that also merged for the first time this month.
	seedPR(t, s, "o/r", 4, "dependabot[bot]", 4, 3)
	// Someone who only ever opened a PR, never merged one.
	seedPR(t, s, "o/r", 5, "hopeful", 6, -1)

	got, err := s.NewContributors(30, 0)
	if err != nil {
		t.Fatalf("NewContributors: %v", err)
	}
	if len(got) != 1 || got[0].Login != "newcomer" {
		var names []string
		for _, g := range got {
			names = append(names, g.Login)
		}
		t.Fatalf("new contributors = %v, want [newcomer]", names)
	}
	if got[0].Merged != 1 {
		t.Errorf("merged total = %d, want 1", got[0].Merged)
	}
}

// TestNewContributorsDedupesSameSecond covers the join: an author whose first
// two merges share a timestamp matches twice and is one person.
func TestNewContributorsDedupesSameSecond(t *testing.T) {
	s := openTracked(t)
	at := ago(3)
	prs := []PR{
		{FullName: "o/r", Number: 1, Author: "twin", State: "closed", CreatedAt: ago(4), MergedAt: at},
		{FullName: "o/r", Number: 2, Author: "twin", State: "closed", CreatedAt: ago(4), MergedAt: at},
	}
	if err := s.UpsertPRs(prs); err != nil {
		t.Fatal(err)
	}
	got, err := s.NewContributors(30, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
}

func TestWindowStatsAndMedian(t *testing.T) {
	s := openTracked(t)
	// Three merges at 1h, 5h and 100h. Median is 5, the mean would be 35.3.
	base := time.Now().UTC().AddDate(0, 0, -3)
	mk := func(n int, hours float64) PR {
		return PR{FullName: "o/r", Number: n, Author: "a", State: "closed",
			CreatedAt: base.Format(time.RFC3339),
			MergedAt:  base.Add(time.Duration(hours * float64(time.Hour))).Format(time.RFC3339)}
	}
	if err := s.UpsertPRs([]PR{mk(1, 1), mk(2, 5), mk(3, 100)}); err != nil {
		t.Fatal(err)
	}
	ov, err := s.Overview(30)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if ov.Window.PRsMerged != 3 {
		t.Errorf("merged = %d, want 3", ov.Window.PRsMerged)
	}
	if ov.Window.MedianMergeHrs != 5 {
		t.Errorf("median = %v, want 5 (a mean would be 35.3)", ov.Window.MedianMergeHrs)
	}
	if ov.Window.Authors != 1 {
		t.Errorf("authors = %d, want 1", ov.Window.Authors)
	}
}

func TestUpsertRepoKeepsFirstSeenAndSeedSource(t *testing.T) {
	s := openTest(t)
	first := time.Now().UTC().AddDate(0, 0, -40)
	if err := s.UpsertRepo(Repo{FullName: "o/r", Source: "seed", Kind: "core", Stars: 1}, first); err != nil {
		t.Fatal(err)
	}
	// A discovery rediscovers it a month later with more stars.
	if err := s.UpsertRepo(Repo{FullName: "o/r", Source: "discovered", Kind: "go-import", Stars: 9}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Repos("", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].Source != "seed" {
		t.Errorf("source = %q, want seed: a curated row must not be downgraded by a search", got[0].Source)
	}
	if got[0].Stars != 9 {
		t.Errorf("stars = %d, want 9", got[0].Stars)
	}
	if !strings.HasPrefix(got[0].FirstSeen, first.Format("2006-01-02")) {
		t.Errorf("first_seen = %q, want it pinned to %s", got[0].FirstSeen, first.Format("2006-01-02"))
	}
}

func TestReplaceContributorsDropsVanishedLogins(t *testing.T) {
	s := openTest(t)
	if err := s.ReplaceContributors("o/r", []Contributor{{Login: "a", Commits: 5}, {Login: "b", Commits: 3}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceContributors("o/r", []Contributor{{Login: "a", Commits: 6}}); err != nil {
		t.Fatal(err)
	}
	top, err := s.TopContributors(30, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || top[0].Login != "a" || top[0].Commits != 6 {
		t.Fatalf("top = %+v, want just a with 6 commits", top)
	}
}

func TestNewClientRefusesWithoutToken(t *testing.T) {
	if _, err := NewClient("  "); err != ErrNoToken {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

// TestFetchRepoFollowsRename is the trap that would otherwise write a second
// row: gnolang/docs answers as gnolang/docs.gno.land, and keying on what was
// asked for leaves a duplicate that never updates.
func TestFetchRepoFollowsRename(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/old" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"full_name": "o/new", "name": "new",
			"owner":            map[string]any{"login": "o"},
			"stargazers_count": 7,
			"license":          map[string]any{"spdx_id": "NOASSERTION"},
		})
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	got, err := c.FetchRepo(context.Background(), "o/old", "seed", "core", "")
	if err != nil {
		t.Fatalf("FetchRepo: %v", err)
	}
	if got.FullName != "o/new" {
		t.Errorf("full_name = %q, want o/new", got.FullName)
	}
	if got.License != "" {
		t.Errorf("license = %q, want empty: NOASSERTION is not a licence", got.License)
	}
}

func TestFetchContributorsDropsBots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"login":"moul","contributions":10,"type":"User"},
			{"login":"dependabot[bot]","contributions":900,"type":"Bot"},
			{"login":"gnolang-bot","contributions":400,"type":"User"}
		]`))
	}))
	defer srv.Close()
	got, err := testClient(srv.URL).FetchContributors(context.Background(), "o/r", 2)
	if err != nil {
		t.Fatalf("FetchContributors: %v", err)
	}
	if len(got) != 1 || got[0].Login != "moul" {
		t.Fatalf("got %+v, want just moul: a bot at the top of a contributors table is worse than no table", got)
	}
}

func TestIsNotFoundAndIsRateLimited(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		notFound, rl bool
	}{
		{"missing repo", 404, `{"message":"Not Found"}`, true, false},
		{"secondary limit", 403, `{"message":"You have exceeded a secondary rate limit"}`, false, true},
		{"abuse 429", 429, `{"message":"too many requests"}`, false, true},
		{"plain forbidden", 403, `{"message":"Resource not accessible"}`, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := &HTTPError{Status: tc.status, Path: "/repos/o/r", Body: tc.body}
			if IsNotFound(err) != tc.notFound {
				t.Errorf("IsNotFound = %v, want %v", IsNotFound(err), tc.notFound)
			}
			if IsRateLimited(err) != tc.rl {
				t.Errorf("IsRateLimited = %v, want %v", IsRateLimited(err), tc.rl)
			}
		})
	}
}

func TestSkipRepoDropsGnolangOwn(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"gnolang/gno", true},
		{"GnoLang/gno", true},
		{"someone/gno-fork", false},
		{"notapath", true},
	} {
		if got := skipRepo(tc.name); got != tc.want {
			t.Errorf("skipRepo(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func testClient(base string) *Client {
	c, _ := NewClient("t")
	c.base = base
	c.CoreSpacing = 0
	c.SearchSpacing = 0
	return c
}

// TestDiscoveryQueryVisibility holds both halves of the leak guard, which
// are not the same fix and look like they should be.
//
// A search runs as the token and returns private repositories its owner can
// read. Measured 2026-09-29 before this was handled: one query returned eight
// of them, two of them the operator's own. Repository search takes
// `is:public`. Code search does not, and appending it there is worse than
// forgetting it, because it is not an error: the query matches nothing and
// the section goes quietly empty (140 results without, 0 with). Code hits are
// filtered on the hit's own repository object instead, in SearchCode.
func TestDiscoveryQueryVisibility(t *testing.T) {
	for _, q := range DiscoveryQueries {
		has := strings.Contains(q.Query, "is:public")
		switch {
		case q.Code && has:
			t.Errorf("code query %q carries is:public, which matches nothing there; "+
				"code hits are filtered on repository.private in SearchCode", q.Query)
		case !q.Code && !has:
			t.Errorf("repository query %q has no is:public: it would return private "+
				"repositories the token can read", q.Query)
		}
	}
}

// TestSearchCodeDropsPrivateHits is the code-search half, against a payload
// shaped like GitHub's: a private repository the token can read, sitting
// beside a public one.
func TestSearchCodeDropsPrivateHits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":3,"items":[
			{"path":"go.mod","repository":{"full_name":"pub/one","private":false,"visibility":"public"}},
			{"path":"meta/177/probe.go.mod.txt","repository":{"full_name":"someone/secret","private":true,"visibility":"private"}},
			{"path":"go.mod","repository":{"full_name":"corp/internal","private":false,"visibility":"internal"}}
		]}`))
	}))
	defer srv.Close()
	hits, total, err := testClient(srv.URL).SearchCode(context.Background(), "q", 1)
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3: the reported total counts what was filtered out too", total)
	}
	if len(hits) != 1 || hits[0].FullName != "pub/one" {
		t.Fatalf("hits = %+v, want just pub/one", hits)
	}
}

// TestUpsertRepoRefusesPrivate is the second line: even if a query slips
// through, a private row must not reach the table.
func TestUpsertRepoRefusesPrivate(t *testing.T) {
	s := openTest(t)
	err := s.UpsertRepo(Repo{FullName: "someone/secret", Private: true}, time.Now())
	if err == nil {
		t.Fatal("UpsertRepo accepted a private repository")
	}
	got, qerr := s.Repos("", "", 10)
	if qerr != nil {
		t.Fatal(qerr)
	}
	if len(got) != 0 {
		t.Fatalf("stored %d rows, want 0", len(got))
	}
}

// TestToRepoMarksPrivate covers both spellings GitHub uses. `private` is the
// old boolean and `visibility` the newer string, and an internal repository
// inside an enterprise reports visibility "internal" with private false.
func TestToRepoMarksPrivate(t *testing.T) {
	tests := []struct {
		name       string
		private    bool
		visibility string
		want       bool
	}{
		{"plain public", false, "public", false},
		{"no visibility field", false, "", false},
		{"private bool", true, "", true},
		{"internal", false, "internal", true},
		{"private both", true, "private", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := ghRepo{FullName: "o/r", Private: tc.private, Visibility: tc.visibility}
			if got := g.toRepo("discovered", "", "").Private; got != tc.want {
				t.Errorf("Private = %v, want %v", got, tc.want)
			}
		})
	}
}
