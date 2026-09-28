package httpapi

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// seedBadgeRealm writes one realm and n calls spread one per day backwards from
// now, so a series over any window has something to draw and the bucket
// boundaries are real rather than all landing in the same hour.
func seedBadgeRealm(t *testing.T, api *API, path string, days int) {
	t.Helper()
	when := time.Now().UTC().Format(time.RFC3339Nano)
	if err := api.db.UpsertPackage("alpha", path, "pkg", "g1creator", "TXD", 100, when, true, 1); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	for i := 0; i < days; i++ {
		ts := time.Now().UTC().AddDate(0, 0, -i).Format(time.RFC3339Nano)
		caller := fmt.Sprintf("g1caller%d", i%3)
		if err := api.db.InsertCall("alpha", fmt.Sprintf("TX%d", i), 110+i, 0, ts, caller, path, "Post", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}
}

func badgeMux(t *testing.T) (*http.ServeMux, *API) {
	t.Helper()
	api, _ := newTestAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	return mux, api
}

// The whole point of these routes is that a document on someone else's origin
// can load them as an image, so the headers are the feature as much as the
// pixels are.
func TestBadgeRealmServesAnEmbeddableSVG(t *testing.T) {
	mux, api := badgeMux(t)
	seedBadgeRealm(t, api, "gno.land/r/alpha/board", 10)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/realm/r/alpha/board?network=alpha", nil))

	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	checks := []struct{ key, want string }{
		{"Content-Type", "image/svg+xml"},
		{"Cache-Control", "max-age=300"},
		{"X-Content-Type-Options", "nosniff"},
		{"Access-Control-Allow-Origin", "*"},
		{"Content-Security-Policy", "default-src 'none'"},
	}
	for _, c := range checks {
		if !strings.Contains(h.Get(c.key), c.want) {
			t.Errorf("%s = %q, want it to contain %q", c.key, h.Get(c.key), c.want)
		}
	}
	if h.Get("ETag") == "" {
		t.Error("no ETag, so a reader can never revalidate a badge it already holds")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "gno.land/r/alpha/board") {
		t.Errorf("the badge does not name its subject:\n%s", body)
	}
	if !strings.Contains(body, `class="line"`) {
		t.Errorf("ten days of calls drew no line:\n%s", body)
	}
}

// The extension is optional. Some markdown renderers and image proxies key off
// one, and a realm author will write whichever they saw first.
func TestBadgeRealmAcceptsAnSvgSuffix(t *testing.T) {
	mux, api := badgeMux(t)
	seedBadgeRealm(t, api, "gno.land/r/alpha/board", 5)

	for _, target := range []string{
		"/_badges/realm/r/alpha/board?network=alpha",
		"/_badges/realm/r/alpha/board.svg?network=alpha",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", target, rec.Code)
		}
		if h := rec.Header().Get("X-Badge-Error"); h != "" {
			t.Errorf("%s: %s", target, h)
		}
	}
}

// An <img> cannot read a status code or a JSON body, so every failure has to
// arrive as a picture that says what went wrong. The header and the no-store
// are how everything that is not a browser still tells the two apart.
func TestBadgeErrorsAreDrawnNotReturned(t *testing.T) {
	mux, api := badgeMux(t)
	seedBadgeRealm(t, api, "gno.land/r/alpha/board", 3)

	tests := []struct {
		name     string
		target   string
		wantErr  string
		wantSeen string
	}{
		{"unknown network", "/_badges/realm/r/alpha/board?network=nope", "unknown network", "nope"},
		{"unknown package", "/_badges/realm/r/alpha/nosuch?network=alpha", "no such package", "gno.land/r/alpha/nosuch"},
		{"network badge with no network", "/_badges/network", "?network= is required", ""},
		{"network badge, unknown network", "/_badges/network?network=nope", "unknown network", "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", tt.target, nil))

			if rec.Code != 200 {
				t.Fatalf("status %d: a browser discards the body of anything else", rec.Code)
			}
			if got := rec.Header().Get("X-Badge-Error"); got != tt.wantErr {
				t.Errorf("X-Badge-Error = %q, want %q", got, tt.wantErr)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store: a typo must not be pinned for five minutes", got)
			}
			body := rec.Body.String()
			if !strings.Contains(body, xmlText(tt.wantErr)) {
				t.Errorf("the card does not say what went wrong:\n%s", body)
			}
			if tt.wantSeen != "" && !strings.Contains(body, tt.wantSeen) {
				t.Errorf("the card does not echo %q:\n%s", tt.wantSeen, body)
			}
		})
	}
}

// Every badge is parsed by somebody else's XML parser. This is the integration
// half of pkg/badge's own well-formedness test: it proves the handler does not
// wrap or truncate what the renderer produced.
func TestBadgeBodiesAreWellFormedXML(t *testing.T) {
	mux, api := badgeMux(t)
	seedBadgeRealm(t, api, "gno.land/r/alpha/board", 8)
	seedActivity(t, api.db, "alpha", 6, 2, 2)

	for _, target := range []string{
		"/_badges/realm/r/alpha/board?network=alpha",
		"/_badges/realm/r/alpha/board?network=alpha&metric=callers",
		"/_badges/realm/r/alpha/board?network=alpha&days=365",
		"/_badges/realm/r/alpha/board?network=alpha&days=2",
		"/_badges/realm/r/alpha/nosuch?network=alpha",
		"/_badges/network?network=alpha",
		"/_badges/network?network=alpha&days=200",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		d := xml.NewDecoder(strings.NewReader(rec.Body.String()))
		for {
			if _, err := d.Token(); err != nil {
				if err.Error() == "EOF" {
					break
				}
				t.Fatalf("%s: not well-formed: %v\n%s", target, err, rec.Body.String())
			}
		}
	}
}

// A badge is fetched on every read of whatever embeds it, so revalidation is
// the difference between a conditional request and a redraw.
func TestBadgeRevalidatesWithETag(t *testing.T) {
	mux, api := badgeMux(t)
	seedBadgeRealm(t, api, "gno.land/r/alpha/board", 4)
	const target = "/_badges/realm/r/alpha/board?network=alpha"

	first := httptest.NewRecorder()
	mux.ServeHTTP(first, httptest.NewRequest("GET", target, nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the first response")
	}

	for _, inm := range []string{etag, "*", "W/" + etag, `"other", ` + etag} {
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("If-None-Match", inm)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("If-None-Match %q: status %d, want 304", inm, rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %q: 304 carried a body", inm)
		}
	}

	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("If-None-Match", `"nomatch"`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("a non-matching validator got %d, want a full 200", rec.Code)
	}
}

// The two metrics answer different questions and must not draw the same line:
// one bot calling every hour is a wall of messages and a flat 1 caller.
func TestBadgeMetricsDiffer(t *testing.T) {
	mux, api := badgeMux(t)
	const path = "gno.land/r/alpha/bot"
	when := time.Now().UTC().Format(time.RFC3339Nano)
	if err := api.db.UpsertPackage("alpha", path, "bot", "g1creator", "TXD", 100, when, true, 1); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	for i := 0; i < 20; i++ {
		if err := api.db.InsertCall("alpha", fmt.Sprintf("TXB%d", i), 200+i, 0, when, "g1bot", path, "Tick", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}

	get := func(metric string) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/realm/r/alpha/bot?network=alpha&metric="+metric, nil))
		return rec.Body.String()
	}
	if !strings.Contains(get("messages"), "20 messages") {
		t.Errorf("messages badge did not count 20:\n%s", get("messages"))
	}
	if !strings.Contains(get("callers"), "peak 1 caller") {
		t.Errorf("callers badge did not see one bot:\n%s", get("callers"))
	}
	if strings.Contains(get("callers"), "20 messages") {
		t.Error("the callers badge printed the messages headline")
	}
}

func TestBadgeDaysIsClamped(t *testing.T) {
	tests := []struct {
		query string
		want  int
	}{
		{"", badgeDefaultDays},
		{"?days=7", 7},
		{"?days=0", badgeDefaultDays},
		{"?days=1", badgeMinDays},
		{"?days=-5", badgeDefaultDays},
		{"?days=100000", badgeMaxDays},
		{"?days=banana", badgeDefaultDays},
	}
	for _, tt := range tests {
		got := badgeDays(httptest.NewRequest("GET", "/_badges/network"+tt.query, nil))
		if got != tt.want {
			t.Errorf("badgeDays(%q) = %d, want %d", tt.query, got, tt.want)
		}
	}
}

func TestBadgeGranularity(t *testing.T) {
	tests := []struct {
		days int
		want string
	}{
		{2, "hourly"},
		{3, "hourly"},
		{4, "daily"},
		{30, "daily"},
		{120, "daily"},
		{121, "weekly"},
		{365, "weekly"},
	}
	for _, tt := range tests {
		if got := badgeGranularity(tt.days); got != tt.want {
			t.Errorf("badgeGranularity(%d) = %q, want %q", tt.days, got, tt.want)
		}
	}
}

func TestHumanInt(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"}, {7, "7"}, {999, "999"}, {1000, "1,000"},
		{12345, "12,345"}, {1234567, "1,234,567"}, {-4321, "-4,321"},
	}
	for _, tt := range tests {
		if got := humanInt(tt.in); got != tt.want {
			t.Errorf("humanInt(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The badge routes are registered through the same recorder as /api/, so they
// have to turn up in the endpoint reference. They are the part of the surface
// most likely to be pasted into somebody else's document.
func TestBadgeRoutesAreDocumented(t *testing.T) {
	_, api := badgeMux(t)
	want := map[string]bool{
		BadgePrefix + "realm/{path...}": false,
		BadgePrefix + "network":         false,
	}
	for _, e := range api.routes {
		if _, ok := want[e.Path]; ok {
			want[e.Path] = true
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("%s is not in /api/endpoints", p)
		}
	}
}

// cacheable() is what puts badges in the response cache, and a badge is the
// most expensive thing here per request: a SQLite aggregate over a realm's
// whole message history, recomputed for every reader of every page embedding it.
func TestBadgesAreCacheable(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/_badges/realm/r/moul/home", true},
		{"/_badges/network", true},
		{"/api/stats", true},
		{"/api/live", false},
		{"/", false},
		{"/realm/r/moul/home", false},
	}
	for _, tt := range tests {
		if got := cacheable(httptest.NewRequest("GET", tt.path, nil)); got != tt.want {
			t.Errorf("cacheable(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// xmlText is what a literal ends up looking like once the renderer has escaped
// it, so a test can assert on wording without hand-encoding entities.
func xmlText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// A cache hit never runs the handler, so the client-facing caching of a badge
// depends entirely on the entry replaying what the handler set. Without it a
// HIT drops Cache-Control and the browser falls back to heuristic freshness:
// the badge is re-fetched on every page view, which is the one cost this whole
// route family exists to avoid.
func TestBadgeThroughTheResponseCacheKeepsItsHeaders(t *testing.T) {
	mux, api := badgeMux(t)
	seedBadgeRealm(t, api, "gno.land/r/alpha/board", 6)

	cached := WithResponseCache(NewResponseCache(time.Minute), mux)
	const target = "/_badges/realm/r/alpha/board?network=alpha"

	first := httptest.NewRecorder()
	cached.ServeHTTP(first, httptest.NewRequest("GET", target, nil))
	if got := first.Header().Get("X-Cache"); got != "MISS" {
		t.Fatalf("first request X-Cache = %q, want MISS", got)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the miss")
	}

	hit := httptest.NewRecorder()
	cached.ServeHTTP(hit, httptest.NewRequest("GET", target, nil))
	if got := hit.Header().Get("X-Cache"); got != "HIT" {
		t.Fatalf("second request X-Cache = %q, want HIT", got)
	}
	for _, c := range []struct{ key, want string }{
		{"Content-Type", first.Header().Get("Content-Type")},
		{"Cache-Control", first.Header().Get("Cache-Control")},
		{"ETag", etag},
	} {
		if got := hit.Header().Get(c.key); got != c.want {
			t.Errorf("hit dropped %s: %q, want %q", c.key, got, c.want)
		}
	}
	if hit.Body.String() != first.Body.String() {
		t.Error("the hit served different bytes from the miss")
	}

	// And the conditional answer, which on a hit can only come from the entry.
	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("If-None-Match", etag)
	cond := httptest.NewRecorder()
	cached.ServeHTTP(cond, req)
	if cond.Code != http.StatusNotModified {
		t.Errorf("conditional request on a cache hit got %d, want 304", cond.Code)
	}
	if cond.Body.Len() != 0 {
		t.Error("the 304 carried a body")
	}
}

// An endpoint that sets no validator must not acquire one from this change:
// a 304 for a reader who never had the bytes would be a blank response.
func TestResponseCacheDoesNotInventAValidator(t *testing.T) {
	mux, api := badgeMux(t)
	seedActivity(t, api.db, "alpha", 3, 1, 1)

	cached := WithResponseCache(NewResponseCache(time.Minute), mux)
	const target = "/api/stats?network=alpha"

	miss := httptest.NewRecorder()
	cached.ServeHTTP(miss, httptest.NewRequest("GET", target, nil))
	if miss.Header().Get("ETag") != "" {
		t.Skip("/api/stats now sets an ETag; this test needs another endpoint that does not")
	}

	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("If-None-Match", "*")
	hit := httptest.NewRecorder()
	cached.ServeHTTP(hit, req)
	if hit.Code != 200 {
		t.Fatalf("status %d, want 200: an endpoint with no ETag cannot answer 304", hit.Code)
	}
	if hit.Body.Len() == 0 {
		t.Fatal("empty body: If-None-Match: * was honoured by an endpoint with no validator")
	}
}
