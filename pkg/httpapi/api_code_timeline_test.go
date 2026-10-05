package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/analyzer"
	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// tlNow pins the heatmap's year: it ends on 2026-10-02.
var tlNow = time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)

func pinTimelineClock(t *testing.T) {
	t.Helper()
	prev := codeTimelineNow
	codeTimelineNow = func() time.Time { return tlNow }
	t.Cleanup(func() { codeTimelineNow = prev })
}

func tlDeploy(t *testing.T, db *store.DB, network, path, tx string, height int, when, creator string, ok bool, files ...indexer.MemFile) {
	t.Helper()
	if len(files) == 0 {
		files = []indexer.MemFile{{Name: "a.gno", Body: "package a\n"}}
	}
	name := path[strings.LastIndex(path, "/")+1:]
	pkg := &indexer.MemPackage{Name: name, Path: path, Files: files}
	if err := analyzer.NewAnalyzer(db).ProcessPackage(network, pkg, creator, tx, height, 0, when, "", ok); err != nil {
		t.Fatalf("ProcessPackage(%s %s): %v", network, path, err)
	}
}

// seedTimeline: on alpha, a genesis package, an app family deployed at v1,
// republished, then released at v2, a failed submission, and an unrelated
// package; beta carries the same paths in a different order, so a feed that
// mixed the two would classify them wrong.
func seedTimeline(t *testing.T, db *store.DB) {
	t.Helper()
	f := func(name, body string) indexer.MemFile { return indexer.MemFile{Name: name, Body: body} }
	day := func(d int, hour int) string {
		return time.Date(2026, 9, d, hour, 0, 0, 0, time.UTC).Format(time.RFC3339)
	}
	tlDeploy(t, db, "alpha", "gno.land/p/sys/base", "tx-gen", 0, day(1, 0), "g1genesis", true)
	tlDeploy(t, db, "alpha", "gno.land/r/ns/app/v1", "tx-a1", 10, day(20, 1), "g1alice", true)
	tlDeploy(t, db, "alpha", "gno.land/r/ns/app/v1", "tx-a1b", 11, day(20, 2), "g1alice", true)
	tlDeploy(t, db, "alpha", "gno.land/r/ns/app/v2", "tx-a2", 20, day(21, 1), "g1alice", true,
		f("app.gno", "package v2\n\nimport \"gno.land/p/sys/base\"\n\nfunc A() {}\n"),
		f("README.md", "# app\n\nApp keeps a ledger of things.\n"))
	tlDeploy(t, db, "alpha", "gno.land/r/ns/app/v2", "tx-a2x", 21, day(21, 2), "g1alice", false)
	tlDeploy(t, db, "alpha", "gno.land/r/bob/tool", "tx-b1", 30, day(22, 1), "g1bob", true)
	// beta: v2 first, so it is new there, and v1 after it is new too (an
	// older generation arriving later is not a release of a newer one).
	tlDeploy(t, db, "beta", "gno.land/r/ns/app/v2", "tx-b-a2", 5, day(10, 1), "g1alice", true)
	tlDeploy(t, db, "beta", "gno.land/r/ns/app/v1", "tx-b-a1", 6, day(10, 2), "g1alice", true)

	if err := db.UpsertUser("alpha", store.User{Name: "alice", Address: "g1alice", TxHash: "tx-u", BlockHeight: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}
}

type tlBody struct {
	Network string        `json:"network"`
	Height  int           `json:"height"`
	Total   int           `json:"total"`
	Rows    []timelineRow `json:"rows"`
	Next    string        `json:"next"`
}

func getTimeline(t *testing.T, api *API, query string) (*httptest.ResponseRecorder, tlBody) {
	t.Helper()
	rec := sourceGET(t, api, "/api/code/timeline?"+query)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", query, rec.Code, rec.Body.String())
	}
	var body tlBody
	decodeInto(t, rec, &body)
	return rec, body
}

func tlKinds(rows []timelineRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Tx+":"+r.Kind)
	}
	return strings.Join(out, " ")
}

func TestHandleCodeTimelineRequiresOneNetwork(t *testing.T) {
	api, _ := newTestAPI(t)
	for _, u := range []string{CodeTimelinePath, CodeTimelinePath + "?network=all",
		CodeTimelineHeatmapPath, CodeTimelineHeatmapPath + "?network=all"} {
		if rec := sourceGET(t, api, u); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", u, rec.Code)
		}
	}
}

func TestHandleCodeTimelineClassifies(t *testing.T) {
	api, db := newTestAPI(t)
	seedTimeline(t, db)

	_, body := getTimeline(t, api, "network=alpha&failed=1")
	want := "tx-b1:new tx-a2x:failed tx-a2:version tx-a1b:redeploy tx-a1:new tx-gen:new"
	if got := tlKinds(body.Rows); got != want {
		t.Errorf("kinds:\n got %s\nwant %s", got, want)
	}
	by := map[string]timelineRow{}
	for _, r := range body.Rows {
		by[r.Tx] = r
	}
	if r := by["tx-a2"]; r.Prev != "gno.land/r/ns/app/v1" || r.Family != "gno.land/r/ns/app" || r.Gen != 2 {
		t.Errorf("version row: prev %q family %q gen %d", r.Prev, r.Family, r.Gen)
	}
	// Only a version says which one it is: a first publication is not "the
	// 1st version" of anything.
	if r := by["tx-a1"]; r.Gen != 0 {
		t.Errorf("new row carries gen %d", r.Gen)
	}
	// The current submission carries its size, imports and summary; the one
	// its path was republished over does not.
	if r := by["tx-a2"]; !r.Current || r.Lines != 8 || r.Imports != 1 || r.Summary != "App keeps a ledger of things." {
		t.Errorf("current row: %+v", r)
	}
	if r := by["tx-a1"]; r.Current || r.Lines != 0 || r.Summary != "" {
		t.Errorf("superseded row carries current-state numbers: %+v", r)
	}
	if r := by["tx-a1b"]; r.Nth != 2 || !r.Current {
		t.Errorf("redeploy: nth %d current %v", r.Nth, r.Current)
	}
	// Genesis is height 0, classified like anything else and marked.
	if r := by["tx-gen"]; !r.Genesis || r.Height != 0 || r.K != "p" || r.NS != "sys" {
		t.Errorf("genesis row: %+v", r)
	}
	if r := by["tx-a1"]; r.User != "alice" || !r.Debut {
		t.Errorf("alice's first row: user %q debut %v", r.User, r.Debut)
	}
	if by["tx-a2"].Debut || by["tx-a1b"].Debut {
		t.Error("a later publication is marked as the creator's debut")
	}
	if body.Height != 30 || body.Total != 6 {
		t.Errorf("height %d total %d", body.Height, body.Total)
	}

	// Failed submissions are hidden by default.
	_, def := getTimeline(t, api, "network=alpha")
	if got := tlKinds(def.Rows); strings.Contains(got, "failed") || def.Total != 5 {
		t.Errorf("default feed: total %d, %s", def.Total, got)
	}
}

func TestHandleCodeTimelineIsNetworkScoped(t *testing.T) {
	api, db := newTestAPI(t)
	seedTimeline(t, db)
	_, body := getTimeline(t, api, "network=beta")
	if got := tlKinds(body.Rows); got != "tx-b-a1:new tx-b-a2:new" {
		t.Errorf("beta: %s", got)
	}
	for _, r := range body.Rows {
		if r.User != "" {
			t.Errorf("beta row resolved alpha's name: %+v", r)
		}
	}
}

func TestHandleCodeTimelineFilters(t *testing.T) {
	api, db := newTestAPI(t)
	seedTimeline(t, db)
	cases := []struct{ q, want string }{
		{"kind=new", "tx-b1:new tx-a1:new tx-gen:new"},
		{"kind=version,redeploy", "tx-a2:version tx-a1b:redeploy"},
		{"kind=version&kind=failed", "tx-a2x:failed tx-a2:version"},
		{"ns=ns", "tx-a2:version tx-a1b:redeploy tx-a1:new"},
		{"creator=g1bob", "tx-b1:new"},
		{"creator=alice", "tx-a2:version tx-a1b:redeploy tx-a1:new"},
		{"creator=@alice&kind=new", "tx-a1:new"},
		{"day=2026-09-20", "tx-a1b:redeploy tx-a1:new"},
	}
	for _, c := range cases {
		_, body := getTimeline(t, api, "network=alpha&"+c.q)
		if got := tlKinds(body.Rows); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.q, got, c.want)
		}
	}
	for _, q := range []string{"kind=bogus", "failed=maybe", "day=yesterday", "limit=0", "limit=x", "before=nope"} {
		if rec := sourceGET(t, api, "/api/code/timeline?network=alpha&"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
}

// Paging walks the whole feed with no row twice and none skipped, including
// several rows in one block, and a cursor stays valid when newer rows land
// on top of it.
func TestHandleCodeTimelinePaging(t *testing.T) {
	api, db := newTestAPI(t)
	var want []string
	for i := 0; i < 23; i++ {
		// Three submissions per block, so the tie-break inside a block is
		// exercised on most page boundaries.
		h := 100 + i/3
		tx := fmt.Sprintf("tx-%02d", i)
		tlDeploy(t, db, "alpha", fmt.Sprintf("gno.land/r/p%02d/x", i), tx, h, "2026-09-01T00:00:00Z", "g1a", true)
		want = append([]string{tx}, want...)
	}
	var got []string
	before := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging does not terminate")
		}
		q := "network=alpha&limit=5"
		if before != "" {
			q += "&before=" + url.QueryEscape(before)
		}
		_, body := getTimeline(t, api, q)
		if pages == 0 && body.Total != 23 {
			t.Errorf("total %d on page %d", body.Total, pages)
		}
		for _, r := range body.Rows {
			got = append(got, r.Tx)
		}
		if pages == 0 {
			// Newer rows arriving between page 1 and page 2 do not move it.
			tlDeploy(t, db, "alpha", "gno.land/r/late/x", "tx-late", 999, "2026-09-02T00:00:00Z", "g1a", true)
		}
		if body.Next == "" {
			break
		}
		before = body.Next
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("paged:\n got %v\nwant %v", got, want)
	}

	_, capped := getTimeline(t, api, "network=alpha&limit=5000")
	if len(capped.Rows) != 24 {
		t.Errorf("limit above the cap: %d rows", len(capped.Rows))
	}
}

func TestHandleCodeTimelineHeatmap(t *testing.T) {
	pinTimelineClock(t)
	api, db := newTestAPI(t)
	seedTimeline(t, db)
	// A year and a day ago: outside the calendar.
	tlDeploy(t, db, "alpha", "gno.land/r/old/x", "tx-old", 2, "2025-10-01T12:00:00Z", "g1old", true)

	rec := sourceGET(t, api, CodeTimelineHeatmapPath+"?network=alpha")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var h timelineHeatmap
	decodeInto(t, rec, &h)
	if h.From != "2025-10-03" || h.To != "2026-10-02" {
		t.Errorf("range %s..%s", h.From, h.To)
	}
	got := fmt.Sprint(h.Days)
	want := "[{2026-09-01 1 0 0} {2026-09-20 1 0 1} {2026-09-21 0 1 0} {2026-09-22 1 0 0}]"
	if got != want {
		t.Errorf("days:\n got %s\nwant %s", got, want)
	}
	if h.Total != 5 || h.Max != 2 {
		t.Errorf("total %d max %d", h.Total, h.Max)
	}

	rec = sourceGET(t, api, CodeTimelineHeatmapPath+"?network=alpha&creator=g1bob")
	decodeInto(t, rec, &h)
	if fmt.Sprint(h.Days) != "[{2026-09-22 1 0 0}]" {
		t.Errorf("bob's calendar: %v", h.Days)
	}
	rec = sourceGET(t, api, CodeTimelineHeatmapPath+"?network=beta")
	decodeInto(t, rec, &h)
	if fmt.Sprint(h.Days) != "[{2026-09-10 2 0 0}]" {
		t.Errorf("beta's calendar: %v", h.Days)
	}
}

func TestHandleCodeTimelineETagAndCaching(t *testing.T) {
	api, db := newTestAPI(t)
	seedTimeline(t, db)

	head, body := getTimeline(t, api, "network=alpha&limit=2")
	etag := head.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"tl-30-`) {
		t.Errorf("ETag %q", etag)
	}
	if cc := head.Header().Get("Cache-Control"); cc != "public, max-age=15" {
		t.Errorf("head Cache-Control %q", cc)
	}
	if rec := sourceGET(t, api, "/api/code/timeline?network=alpha&limit=2", "If-None-Match", etag); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: status %d, want 304", rec.Code)
	}
	page2, _ := getTimeline(t, api, "network=alpha&limit=2&before="+url.QueryEscape(body.Next))
	if cc := page2.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("cursor page Cache-Control %q", cc)
	}
	if page2.Header().Get("ETag") == etag {
		t.Error("two pages share an ETag")
	}
	beta, _ := getTimeline(t, api, "network=beta&limit=2")
	if beta.Header().Get("ETag") == etag {
		t.Error("two networks share an ETag")
	}

	// A deploy moves the head's validator.
	tlDeploy(t, db, "alpha", "gno.land/r/ns/new", "tx-new", 40, "2026-09-23T00:00:00Z", "g1bob", true)
	after, _ := getTimeline(t, api, "network=alpha&limit=2")
	if after.Header().Get("ETag") == etag {
		t.Error("a deploy left the head's ETag unchanged")
	}

	c := NewResponseCache(CacheTTL)
	if got := c.ttlForRequest(httptest.NewRequest(http.MethodGet, CodeTimelinePath+"?network=alpha&before=1.0.x", nil)); got != codeTimelineCursorTTL {
		t.Errorf("cursor page ttl %s", got)
	}
	if got := c.ttlForRequest(httptest.NewRequest(http.MethodGet, CodeTimelinePath+"?network=alpha", nil)); got != CacheTTL {
		t.Errorf("head ttl %s", got)
	}
}

func TestCodeTimelineIsListedInEndpoints(t *testing.T) {
	api, _ := newTestAPI(t)
	api.RegisterRoutes(http.NewServeMux())
	found := map[string]bool{}
	for _, e := range api.routes {
		if e.Method == http.MethodGet {
			found[e.Path] = true
		}
	}
	for _, p := range []string{CodeTimelinePath, CodeTimelineHeatmapPath} {
		if !found[p] {
			t.Errorf("%s is not in /api/endpoints", p)
		}
	}
}
