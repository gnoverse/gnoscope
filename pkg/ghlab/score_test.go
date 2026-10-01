package ghlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRepoTier(t *testing.T) {
	for _, tc := range []struct {
		repo, source string
		want         Tier
	}{
		{"gnolang/gno", "seed", TierCore},
		{"GnoLang/Gno", "seed", TierCore},
		{"gnolang/tx-indexer", "seed", TierGnolang},
		{"gnolang/gno-js-client", "seed", TierGnolang},
		{"gnoverse/gnoscope", "seed", TierPicked},
		{"gnoverse/whatever", "discovered", TierPicked},
		{"onbloc/adena-wallet", "seed", TierPicked},
		{"gnoswap-labs/gnoswap", "seed", TierPicked},
		{"someone/realm", "discovered", TierOther},
		{"someone/realm", "", TierOther},
	} {
		if got := RepoTier(tc.repo, tc.source); got != tc.want {
			t.Errorf("RepoTier(%q, %q) = %d, want %d", tc.repo, tc.source, got, tc.want)
		}
	}
}

func mergedPRs(t *testing.T, s *Store, repo, author string, from, n int) {
	t.Helper()
	var prs []PR
	for i := 0; i < n; i++ {
		prs = append(prs, PR{FullName: repo, Number: from + i, Author: author, State: "closed",
			CreatedAt: ago(400), UpdatedAt: ago(399), MergedAt: ago(399)})
	}
	if err := s.UpsertPRs(prs); err != nil {
		t.Fatal(err)
	}
}

// TestScoreRanksWorkNotMergeStrategy is the bug this score replaced, in the
// numbers measured on 2026-10-01 (scaled down by ten): a squash-merging core
// repository against a merge-commit app repository. Ranked by commits the app
// author wins by 1,072 to 464; by tiered merged pull requests they do not.
func TestScoreRanksWorkNotMergeStrategy(t *testing.T) {
	s := openTest(t)
	addRepo(t, s, "gnolang/gno", "seed", true)
	addRepo(t, s, "app/wallet", "seed", true)
	addRepo(t, s, "rando/thing", "discovered", false)

	mergedPRs(t, s, "gnolang/gno", "core", 1, 43)
	mergedPRs(t, s, "app/wallet", "app", 1, 61)
	mergedPRs(t, s, "rando/thing", "app", 1, 10)
	if err := s.ReplaceContributors("gnolang/gno", []Contributor{{Login: "core", Commits: 46}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceContributors("app/wallet", []Contributor{{Login: "app", Commits: 107}}); err != nil {
		t.Fatal(err)
	}
	// core reviews app's work twice; app comments on its own PR, which must
	// not count, and a bot reviews, which must not either.
	if err := s.StorePage(nil, []PREvent{
		{FullName: "app/wallet", Number: 1, Login: "core", Kind: "review", At: ago(2)},
		{FullName: "app/wallet", Number: 2, Login: "core", Kind: "review", At: ago(400)},
	}); err != nil {
		t.Fatal(err)
	}

	top, err := s.TopContributors(30, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].Login != "core" {
		t.Fatalf("top = %+v, want core first", top)
	}
	core, app := top[0], top[1]
	// core: 43 + 0.1*3 extra commits, x1.0; plus 2 reviews x0.1 x0.25 = 43.35.
	if math.Abs(core.Score-43.35) > 0.051 {
		t.Errorf("core score = %v, want 43.35 to one decimal", core.Score)
	}
	// app: (61 + 0.1*46) x0.25 = 16.4, plus 10 x0.1 = 1, = 17.4.
	if app.Score != 17.4 {
		t.Errorf("app score = %v, want 17.4", app.Score)
	}
	if app.Commits != 107 || app.MergedPRs != 71 {
		t.Errorf("app commits/prs = %d/%d, want 107/71", app.Commits, app.MergedPRs)
	}
	if core.ByTier[0] != 43.3 || core.ByTier[2] != 0.1 {
		t.Errorf("core by tier = %v", core.ByTier)
	}
	if core.WindowScore != 0 {
		// One review in the window: 0.1 x 0.25 = 0.025, which rounds to 0.
		t.Errorf("core window score = %v, want 0", core.WindowScore)
	}
	if got := strings.Join(app.Repos, ","); got != "app/wallet,rando/thing" {
		t.Errorf("app repos = %s, want biggest share first", got)
	}
}

// TestTrackedViewKeepsWindowStatsCurated: walking a discovered repository for
// the score must not change what "merged this month" counts.
func TestTrackedViewKeepsWindowStatsCurated(t *testing.T) {
	s := openTracked(t)
	addRepo(t, s, "rando/thing", "discovered", false)
	seedPR(t, s, "o/r", 1, "a", 3, 2)
	seedPR(t, s, "rando/thing", 1, "b", 3, 2)
	w, err := s.windowStats(30)
	if err != nil {
		t.Fatal(err)
	}
	if w.PRsMerged != 1 || w.Authors != 1 {
		t.Fatalf("merged=%d authors=%d, want 1 and 1: the discovered repository leaked into the window", w.PRsMerged, w.Authors)
	}
}

// gqlServer answers the walk query from pages keyed by cursor, and the
// follow-up queries from extra, and records every cursor it was asked for.
type gqlServer struct {
	pages  map[string]string // cursor -> pullRequests JSON
	extra  string            // comments follow-up JSON
	asked  []string
	failOn string // a cursor to answer with RATE_LIMITED
}

func (g *gqlServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(body, &req)
	cursor, _ := req.Variables["cursor"].(string)
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4990")
	if strings.Contains(req.Query, "pullRequest(number") {
		fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"comments":%s}}}}`, g.extra)
		return
	}
	g.asked = append(g.asked, cursor)
	if cursor == g.failOn && g.failOn != "" {
		fmt.Fprint(w, `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`)
		return
	}
	fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":%s}}}`, g.pages[cursor])
}

func pullJSON(n int, author, state, updated string, reviews, comments string) string {
	merged := "null"
	if state == "MERGED" {
		merged = `"` + updated + `"`
	}
	return fmt.Sprintf(`{"number":%d,"title":"t%d","state":%q,"isDraft":false,"createdAt":%q,"updatedAt":%q,
		"mergedAt":%s,"closedAt":null,"author":{"login":%q},"reviews":%s,"comments":%s}`,
		n, n, state, updated, updated, merged, author, reviews, comments)
}

const noEvents = `{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}`

func TestWalkPullsReadsEventsAndFollowsOverflow(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	reviews := `{"pageInfo":{"hasNextPage":false},"nodes":[
		{"author":{"login":"rev"},"submittedAt":"` + now + `"},
		{"author":{"login":"alice"},"submittedAt":"` + now + `"},
		{"author":{"login":"coderabbitai"},"submittedAt":"` + now + `"},
		{"author":{"login":"netlify","__typename":"Bot"},"submittedAt":"` + now + `"},
		{"author":{"login":"Gno2D2","__typename":"User"},"submittedAt":"` + now + `"},
		{"author":{"login":"pending"},"submittedAt":null},
		{"author":null,"submittedAt":"` + now + `"}]}`
	comments := `{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[{"author":{"login":"rev"},"createdAt":"` + now + `"}]}`
	g := &gqlServer{
		pages: map[string]string{"": `{"pageInfo":{"hasNextPage":false,"endCursor":"p1"},"nodes":[` +
			pullJSON(7, "alice", "MERGED", now, reviews, comments) + `]}`},
		extra: `{"pageInfo":{"hasNextPage":false},"nodes":[{"author":{"login":"other"},"createdAt":"` + now + `"}]}`,
	}
	srv := httptest.NewServer(g)
	defer srv.Close()

	var got WalkPage
	complete, err := testClient(srv.URL).WalkPulls(context.Background(), "o/r", "", time.Time{}, 5, func(p WalkPage) error {
		got = p
		return nil
	})
	if err != nil || !complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
	if len(got.PRs) != 1 || got.PRs[0].State != "closed" || got.PRs[0].MergedAt == "" || got.PRs[0].Author != "alice" {
		t.Fatalf("pr = %+v, want a closed merged pull request by alice", got.PRs)
	}
	var evs []string
	for _, e := range got.Events {
		evs = append(evs, e.Kind+":"+e.Login)
	}
	// Not alice (her own PR), not the bots (by name, by GraphQL's Bot type,
	// by the user-account list), not the pending review, not the ghost; and "other" arrives only through the overflow follow-up.
	if want := "review:rev,comment:rev,comment:other"; strings.Join(evs, ",") != want {
		t.Fatalf("events = %v, want %s", evs, want)
	}
}

// TestWalkRepoResumesThenStopsAtWatermark covers the three states of a walk:
// interrupted (cursor kept), resumed and finished (watermark set, cursor
// cleared), and incremental (stops at the first page older than the mark).
func TestWalkRepoResumesThenStopsAtWatermark(t *testing.T) {
	s := openTest(t)
	recent := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().UTC().AddDate(-2, 0, 0).Format(time.RFC3339)
	g := &gqlServer{
		pages: map[string]string{
			"":   `{"pageInfo":{"hasNextPage":true,"endCursor":"p1"},"nodes":[` + pullJSON(2, "a", "MERGED", recent, noEvents, noEvents) + `]}`,
			"p1": `{"pageInfo":{"hasNextPage":false,"endCursor":"p2"},"nodes":[` + pullJSON(1, "a", "MERGED", old, noEvents, noEvents) + `]}`,
		},
		failOn: "p1",
	}
	srv := httptest.NewServer(g)
	defer srv.Close()
	sy := NewSyncer(s, testClient(srv.URL), nil)
	noop := func(string, ...any) {}

	if err := sy.walkRepo(context.Background(), "o/r", noop); !IsRateLimited(err) {
		t.Fatalf("first walk err = %v, want the rate limit", err)
	}
	w, _ := s.Walk("o/r")
	if w.Cursor != "p1" || w.Watermark != "" {
		t.Fatalf("after interruption walk = %+v, want cursor p1 and no watermark", w)
	}
	started := w.Started

	g.failOn = ""
	if err := sy.walkRepo(context.Background(), "o/r", noop); err != nil {
		t.Fatal(err)
	}
	w, _ = s.Walk("o/r")
	if w.Cursor != "" || w.Watermark != started {
		t.Fatalf("after resume walk = %+v, want no cursor and watermark %s", w, started)
	}
	if got := strings.Join(g.asked, ","); got != ",p1,p1" {
		t.Fatalf("cursors asked = %q, want the top, then p1 twice (failed, resumed)", got)
	}

	// Incremental: page one already ends older than the watermark (the
	// recent PR is newer, the page's oldest is what matters), so a page with
	// a recent PR followed by an old one stops after one request.
	g.asked = nil
	g.pages[""] = `{"pageInfo":{"hasNextPage":true,"endCursor":"p1"},"nodes":[` +
		pullJSON(3, "a", "OPEN", recent, noEvents, noEvents) + `,` + pullJSON(1, "a", "MERGED", old, noEvents, noEvents) + `]}`
	if err := sy.walkRepo(context.Background(), "o/r", noop); err != nil {
		t.Fatal(err)
	}
	if len(g.asked) != 1 {
		t.Fatalf("incremental walk asked %v, want one page", g.asked)
	}
	var n int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM gh_prs WHERE full_name='o/r'`).Scan(&n)
	if n != 3 {
		t.Fatalf("stored %d pull requests, want 3", n)
	}
}

func TestGraphQLErrorsMapOntoHTTPErrors(t *testing.T) {
	for _, tc := range []struct {
		typ            string
		limited, found bool
	}{
		{"RATE_LIMITED", true, false},
		{"NOT_FOUND", false, true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"errors":[{"type":%q,"message":"x"}]}`, tc.typ)
		}))
		_, err := testClient(srv.URL).WalkPulls(context.Background(), "o/r", "", time.Time{}, 1, func(WalkPage) error { return nil })
		srv.Close()
		if IsRateLimited(err) != tc.limited || IsNotFound(err) != tc.found {
			t.Errorf("%s: err = %v, limited=%v notfound=%v", tc.typ, err, IsRateLimited(err), IsNotFound(err))
		}
	}
}

// TestTierOtherIsCapped: a discovered repository full of self-merged pull
// requests is a tiebreaker, not a way to the top.
func TestTierOtherIsCapped(t *testing.T) {
	s := openTest(t)
	addRepo(t, s, "gnolang/gno", "seed", true)
	addRepo(t, s, "spam/os", "discovered", false)
	mergedPRs(t, s, "spam/os", "spammer", 1, 2000)
	mergedPRs(t, s, "gnolang/gno", "dev", 1, 30)
	top, err := s.TopContributors(30, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].Login != "dev" || top[1].Score != TierOtherCap {
		t.Fatalf("top = %+v, want dev first and spammer at the cap of %v", top, TierOtherCap)
	}
}
