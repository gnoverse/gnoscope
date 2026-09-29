package httpapi

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/traffic"
)

func trafficTestStore(t *testing.T) *traffic.Store {
	t.Helper()
	s, err := traffic.Open(filepath.Join(t.TempDir(), "traffic.db"), 30)
	if err != nil {
		t.Fatalf("open traffic db: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// trafficTestMux mirrors the real shape: wildcard API routes plus the SPA
// catch-all that every non-API URL falls through to.
func trafficTestMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/realm/{path...}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Cache", "HIT")
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server-Timing", "app;dur=42.5")
		w.Header().Set("X-Cache", "MISS")
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST "+MCPPath, func(w http.ResponseWriter, r *http.Request) {
		NoteMCPTool(r.Context(), "get_realm_state")
		w.Write([]byte(`{}`))
	})
	// The beacon has to be a registered route, or mux.Handler falls through to
	// the SPA catch-all and the row is classified as a document.
	mux.HandleFunc("GET "+traffic.PageViewPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nope" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		w.Write([]byte("<html></html>"))
	})
	return mux
}

func fire(t *testing.T, h http.Handler, method, target, ua string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "203.0.113.7:51234"
	r.Header.Set("User-Agent", ua)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const browserUA = "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/120 Safari/537.36"

func TestAccessLogRecordsRouteAndTarget(t *testing.T) {
	store := trafficTestStore(t)
	mux := trafficTestMux()
	h := WithAccessLog(store, mux, "gnoscope.com", mux)

	fire(t, h, "GET", "/api/realm/r/moul/home?network=mainnet", browserUA)
	fire(t, h, "GET", "/api/stats?network=mainnet", browserUA)
	fire(t, h, "GET", "/realms?network=pearl", browserUA)
	store.Flush()

	rep, err := store.Report(traffic.Query{Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now()})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.Totals.Requests != 3 {
		t.Fatalf("requests = %d, want 3", rep.Totals.Requests)
	}
	// /realms is a *document* now, not a page view. The document that
	// bootstraps the app and the page views a reader then makes are different
	// events: one document load carries many page views, and only the beacon
	// reports those.
	if rep.Totals.API != 2 || rep.Totals.Pages != 0 {
		t.Errorf("api/pages = %d/%d, want 2/0", rep.Totals.API, rep.Totals.Pages)
	}

	// The realm path is what "which realms do people read" is built on.
	if len(rep.TopRealms) != 1 || rep.TopRealms[0].Label != "r/moul/home" {
		t.Errorf("top realms = %+v, want one row for r/moul/home", rep.TopRealms)
	}
	// The document is still identified by its path: the SPA catch-all matches
	// every non-API URL, so the pattern says nothing about which one it was.
	docs, err := store.Report(traffic.Query{
		Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll,
		Kind: "document", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// TopPages ranks page views, so it is empty here by construction: this
	// test makes no beacon call. What it pins is that the document itself was
	// recorded and identified by its path.
	if docs.Totals.Requests != 1 {
		t.Errorf("documents = %d, want 1 for /realms", docs.Totals.Requests)
	}
	// #448: the query string is where the network lives, and dropping it is
	// what made every chain one row in the old client-side analytics.
	labels := map[string]int64{}
	for _, c := range rep.Networks {
		labels[c.Label] = c.Hits
	}
	if labels["mainnet"] != 2 || labels["pearl"] != 1 {
		t.Errorf("networks = %+v, want mainnet:2 pearl:1", rep.Networks)
	}
}

// The beacon is the only thing that produces a page view, and it is what makes
// in-app navigation visible at all: the frontend moves with history.pushState,
// so going from /realms to /apps sends no document request whatsoever.
func TestAccessLogRecordsPageViewsFromTheBeacon(t *testing.T) {
	store := trafficTestStore(t)
	mux := trafficTestMux()
	h := WithAccessLog(store, mux, "", mux)

	fire(t, h, "GET", traffic.PageViewPath+"?path=/realm/gno.land/r/moul/home", browserUA)
	fire(t, h, "GET", traffic.PageViewPath+"?path=/gnohub/r/moul/home/-/blob/x.gno", browserUA)
	fire(t, h, "GET", traffic.PageViewPath+"?path=/address/g1abc", browserUA)
	fire(t, h, "GET", traffic.PageViewPath+"?path=/realms", browserUA)
	store.Flush()

	rep, err := store.Report(traffic.Query{Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Pages != 4 {
		t.Errorf("page views = %d, want 4", rep.Totals.Pages)
	}
	// Two paths, one realm. That is the whole point of grouping by entity: a
	// list of raw paths would split this realm in two and rank neither.
	if len(rep.Realms) != 1 || rep.Realms[0].Label != "r/moul/home" || rep.Realms[0].Hits != 2 {
		t.Errorf("realms = %+v, want r/moul/home with 2 hits", rep.Realms)
	}
	if len(rep.Addresses) != 1 || rep.Addresses[0].Label != "g1abc" {
		t.Errorf("addresses = %+v, want g1abc", rep.Addresses)
	}
	kinds := map[string]int64{}
	for _, c := range rep.PageKinds {
		kinds[c.Label] = c.Hits
	}
	if kinds["realm detail"] != 1 || kinds["source browser"] != 1 || kinds["address detail"] != 1 || kinds["listing"] != 1 {
		t.Errorf("page kinds = %+v, want one each of realm detail, source browser, address detail, listing", rep.PageKinds)
	}
}

// Scanners are recorded but never mixed into a view of anything else.
func TestAccessLogSeparatesProbes(t *testing.T) {
	store := trafficTestStore(t)
	mux := trafficTestMux()
	h := WithAccessLog(store, mux, "", mux)

	fire(t, h, "GET", "/wp-login.php", browserUA)
	fire(t, h, "GET", "/.env", browserUA)
	fire(t, h, "GET", "/nope", browserUA)
	store.Flush()

	rep, err := store.Report(traffic.Query{Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Requests != 1 {
		t.Errorf("default view = %d requests, want 1; probes must not be counted with everything else", rep.Totals.Requests)
	}
	// A real mistyped path is still a real 404, and is the one worth reading.
	if len(rep.NotFound) != 1 || rep.NotFound[0].Label != "/nope" {
		t.Errorf("not found = %+v, want only /nope", rep.NotFound)
	}
	// And the probes are reachable, because being scanned is worth seeing.
	probes, err := store.Report(traffic.Query{
		Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Kind: "probe", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if probes.Totals.Requests != 2 {
		t.Errorf("probes = %d, want 2", probes.Totals.Requests)
	}
}

func TestAccessLogCapturesCacheStateAndAppTime(t *testing.T) {
	store := trafficTestStore(t)
	mux := trafficTestMux()
	h := WithAccessLog(store, mux, "", mux)

	fire(t, h, "GET", "/api/realm/r/moul/home", browserUA) // X-Cache: HIT, no Server-Timing
	fire(t, h, "GET", "/api/stats", browserUA)             // X-Cache: MISS, app;dur=42.5
	store.Flush()

	rep, err := store.Report(traffic.Query{Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.CacheHits != 1 {
		t.Errorf("cache hits = %d, want 1", rep.Totals.CacheHits)
	}
	states := map[string]int64{}
	for _, c := range rep.Cache {
		states[c.Label] = c.Hits
	}
	if states["HIT"] != 1 || states["MISS"] != 1 {
		t.Errorf("cache states = %+v, want one HIT and one MISS", rep.Cache)
	}
}

func TestAccessLogRecordsMCPToolName(t *testing.T) {
	store := trafficTestStore(t)
	mux := trafficTestMux()
	h := WithAccessLog(store, mux, "", mux)

	fire(t, h, "POST", MCPPath, "claude-code/1.0")
	store.Flush()

	rep, err := store.Report(traffic.Query{Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.MCP != 1 {
		t.Fatalf("mcp requests = %d, want 1", rep.Totals.MCP)
	}
	if len(rep.Tools) != 1 || rep.Tools[0].Label != "get_realm_state" {
		t.Errorf("tools = %+v, want one row for get_realm_state; without it every "+
			"agent call is an indistinguishable POST to one path", rep.Tools)
	}
}

func TestAccessLogRecordsNotFound(t *testing.T) {
	store := trafficTestStore(t)
	mux := trafficTestMux()
	h := WithAccessLog(store, mux, "", mux)

	fire(t, h, "GET", "/nope", browserUA)
	store.Flush()

	rep, err := store.Report(traffic.Query{Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Errors != 1 {
		t.Errorf("errors = %d, want 1", rep.Totals.Errors)
	}
	if len(rep.NotFound) != 1 || rep.NotFound[0].Label != "/nope" {
		t.Errorf("not found = %+v, want one row for /nope", rep.NotFound)
	}
}

// The middleware must be invisible when no store is configured, and must not
// change what a reader receives when one is.
func TestAccessLogIsTransparent(t *testing.T) {
	mux := trafficTestMux()

	off := WithAccessLog(nil, mux, "", mux)
	w := fire(t, off, "GET", "/api/stats", browserUA)
	if w.Code != 200 || w.Body.String() != "{}" {
		t.Errorf("disabled: got %d %q, want 200 {}", w.Code, w.Body.String())
	}

	on := WithAccessLog(trafficTestStore(t), mux, "", mux)
	w = fire(t, on, "GET", "/api/stats", browserUA)
	if w.Code != 200 || w.Body.String() != "{}" {
		t.Errorf("enabled: got %d %q, want 200 {}", w.Code, w.Body.String())
	}
	if w.Header().Get("Server-Timing") != "app;dur=42.5" {
		t.Errorf("Server-Timing was disturbed: %q", w.Header().Get("Server-Timing"))
	}
}

func TestNoteMCPToolOutsideARequestIsSafe(t *testing.T) {
	NoteMCPTool(context.Background(), "whatever") // must not panic
}

func TestAppDurationMS(t *testing.T) {
	tests := []struct {
		header string
		want   float64
	}{
		{"", 0},
		{"app;dur=42.5", 42.5},
		{"db;dur=1, app;dur=8.25", 8.25},
		{"app;dur=8.25; other", 8.25},
		{"app;dur=nonsense", 0},
		{"other;dur=3", 0},
	}
	for _, tc := range tests {
		if got := appDurationMS(tc.header); got != tc.want {
			t.Errorf("appDurationMS(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

// An SSE connection returns from ServeHTTP when the reader disconnects, so its
// duration is how long they stayed, not how long they waited. Left in the api
// bucket it tops the slowest-routes panel forever and drags every percentile
// with it.
func TestAccessLogSeparatesSSEStreams(t *testing.T) {
	store := trafficTestStore(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: hi\n\n"))
	})
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{}"))
	})
	h := WithAccessLog(store, mux, "", mux)

	fire(t, h, "GET", "/api/live", browserUA)
	for i := 0; i < 6; i++ {
		fire(t, h, "GET", "/api/stats", browserUA)
	}
	store.Flush()

	rep, err := store.Report(traffic.Query{Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// Still counted: a stream is real load.
	if rep.Totals.Requests != 7 {
		t.Errorf("requests = %d, want 7", rep.Totals.Requests)
	}
	if rep.Totals.API != 6 {
		t.Errorf("api = %d, want 6; the stream must not be in the api bucket", rep.Totals.API)
	}
	// Excluded from timing: only /api/stats clears the 5-hit floor anyway, but
	// the stream must not appear even when it does.
	for _, tm := range rep.Slowest {
		if tm.Route == "/api/live" {
			t.Errorf("/api/live is in the slowest panel: %+v", tm)
		}
	}
}

// A server error used to reach the reader and nobody else. These two tests
// cover the two halves: the status line, which fires with no store configured,
// and the reason, which only jsonError knows.
func TestServerErrorsReachTheLog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/boom", func(w http.ResponseWriter, r *http.Request) {
		jsonError(w, "the indexer said no", http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /api/fine", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{}"))
	})
	mux.HandleFunc("GET /api/nope", func(w http.ResponseWriter, r *http.Request) {
		jsonError(w, "no such realm", http.StatusNotFound)
	})

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	// Deliberately with no store: the 5xx line must not depend on -traffic-db.
	h := WithAccessLog(nil, mux, "", mux)
	fire(t, h, "GET", "/api/boom", browserUA)
	fire(t, h, "GET", "/api/fine", browserUA)
	fire(t, h, "GET", "/api/nope", browserUA)

	out := buf.String()
	if !strings.Contains(out, "500") || !strings.Contains(out, "/api/boom") {
		t.Errorf("the 500 left no status line in the log; got:\n%s", out)
	}
	if !strings.Contains(out, "the indexer said no") {
		t.Errorf("the 500 left no reason in the log; got:\n%s", out)
	}
	if strings.Contains(out, "/api/fine") {
		t.Errorf("a 200 was logged, which buries real failures; got:\n%s", out)
	}
	if strings.Contains(out, "no such realm") {
		t.Errorf("a 404 reason was logged; a bad request is the reader's business, "+
			"and logging it buries 5xx under scanner traffic; got:\n%s", out)
	}
}
