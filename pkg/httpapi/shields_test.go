package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/config"
)

// seedShieldRealm writes one realm, n calls one per day backwards from now
// (two of which share a transaction), and two accepted deploys.
func seedShieldRealm(t *testing.T, api *API, path string, days int) {
	t.Helper()
	when := time.Now().UTC().Format(time.RFC3339Nano)
	if err := api.db.UpsertPackage("alpha", path, "pkg", "g1creator", "TXD", 100, when, true, 1); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	for i := 0; i < days; i++ {
		ts := time.Now().UTC().AddDate(0, 0, -i).Format(time.RFC3339Nano)
		caller := fmt.Sprintf("g1caller%d", i%3)
		if err := api.db.InsertCall("alpha", fmt.Sprintf("TX%d", i), 110+i, 0, ts, caller, path, "Post", "", "", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}
	for i, h := range []int{100, 105} {
		if err := api.db.InsertPackageSubmission("alpha", fmt.Sprintf("S%d", i), 0, path, "pkg", "g1creator",
			h, time.Now().UTC().AddDate(0, 0, -50+i).Format(time.RFC3339Nano), true, 1, "", true); err != nil {
			t.Fatalf("InsertPackageSubmission: %v", err)
		}
	}
}

// One request per kind, checking the number rather than the picture: what a
// README carries is the count, and the rest of this file is about the shape.
func TestShieldKinds(t *testing.T) {
	mux, api := badgeMux(t)
	const path = "gno.land/r/alpha/board"
	seedShieldRealm(t, api, path, 40)

	tests := []struct {
		name, target string
		wantLabel    string
		wantMessage  string
	}{
		{"every transaction", "/_badges/shield/txs/r/alpha/board?network=alpha", ">txs<", ">40<"},
		{"a window", "/_badges/shield/txs/r/alpha/board?network=alpha&days=10", ">txs (10d)<", ">10<"},
		{"messages, not transactions", "/_badges/shield/messages/r/alpha/board?network=alpha", ">messages<", ">40<"},
		{"distinct callers", "/_badges/shield/users/r/alpha/board?network=alpha", ">users<", ">3<"},
		{"the release count", "/_badges/shield/version/r/alpha/board?network=alpha", ">version<", ">r2<"},
		{"the .svg suffix is optional", "/_badges/shield/txs/r/alpha/board.svg?network=alpha", ">txs<", ">40<"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", tt.target, nil))
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("X-Badge-Error") != "" {
				t.Fatalf("badge error %q", rec.Header().Get("X-Badge-Error"))
			}
			body := rec.Body.String()
			for _, want := range []string{tt.wantLabel, tt.wantMessage} {
				if !strings.Contains(body, want) {
					t.Errorf("no %q in:\n%s", want, body)
				}
			}
		})
	}
}

// A package with no submission rows of its own is a genesis package, and "r0"
// would be a number rather than that fact.
func TestShieldVersionSaysGenesisWithNoSubmissions(t *testing.T) {
	mux, api := badgeMux(t)
	if err := api.db.UpsertPackage("alpha", "gno.land/r/alpha/old", "old", "g1creator", "TXG", 1,
		time.Now().UTC().Format(time.RFC3339Nano), true, 1); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/shield/version/r/alpha/old?network=alpha", nil))
	if !strings.Contains(rec.Body.String(), ">genesis<") {
		t.Errorf("want genesis, got:\n%s", rec.Body.String())
	}
}

// The shape is the other half of the feature: a badge sits in a row with
// somebody else's, so it has to be the height they all are.
func TestShieldIsTheShapeAReadmeExpects(t *testing.T) {
	mux, api := badgeMux(t)
	seedShieldRealm(t, api, "gno.land/r/alpha/board", 3)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/shield/txs/r/alpha/board?network=alpha", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `height="20"`) {
		t.Errorf("not 20 pixels high, so it does not line up with any other badge:\n%s", body)
	}
	h := rec.Header()
	for _, c := range []struct{ key, want string }{
		{"Content-Type", "image/svg+xml"},
		{"Cache-Control", "max-age=300"},
		{"Access-Control-Allow-Origin", "*"},
		{"Content-Security-Policy", "default-src 'none'"},
	} {
		if !strings.Contains(h.Get(c.key), c.want) {
			t.Errorf("%s = %q, want it to contain %q", c.key, h.Get(c.key), c.want)
		}
	}
	if h.Get("ETag") == "" {
		t.Error("no ETag, so every page view redraws a badge the reader already holds")
	}
}

// A README's author writes these by hand and gets them wrong, and an <img> has
// no error channel. Every failure is therefore a drawn answer plus a header
// for everything that is not a browser.
func TestShieldFailuresAreDrawnNotReturned(t *testing.T) {
	mux, api := badgeMux(t)
	seedShieldRealm(t, api, "gno.land/r/alpha/board", 2)

	tests := []struct {
		name, target, wantErr string
	}{
		{"a typo in the path", "/_badges/shield/txs/r/alpha/bord?network=alpha", "no such package"},
		{"a kind that does not exist", "/_badges/shield/stars/r/alpha/board?network=alpha", "unknown badge: stars"},
		{"a network nobody configured", "/_badges/shield/txs/r/alpha/board?network=zeta", "unknown network"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", tt.target, nil))
			if rec.Code != 200 {
				t.Errorf("status %d: a browser handed a 404 paints the broken-image glyph and throws the message away", rec.Code)
			}
			if got := rec.Header().Get("X-Badge-Error"); got != tt.wantErr {
				t.Errorf("X-Badge-Error = %q, want %q", got, tt.wantErr)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store: nothing should pin a typo's answer", got)
			}
			if !strings.Contains(rec.Body.String(), `height="20"`) {
				t.Errorf("the failure is not shield-shaped, so it wrecks the row it is in:\n%s", rec.Body.String())
			}
		})
	}
}

// The badge is in somebody else's document, so what it is called there is
// theirs to decide.
func TestShieldHonoursLabelColorAndStyle(t *testing.T) {
	mux, api := badgeMux(t)
	seedShieldRealm(t, api, "gno.land/r/alpha/board", 2)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET",
		"/_badges/shield/txs/r/alpha/board?network=alpha&label=calls&color=brightgreen&style=flat-square", nil))
	body := rec.Body.String()
	if !strings.Contains(body, ">calls<") {
		t.Errorf("?label= was ignored:\n%s", body)
	}
	if !strings.Contains(body, "#4c1") {
		t.Errorf("?color= was ignored:\n%s", body)
	}
	if strings.Contains(body, "mask") {
		t.Errorf("?style=flat-square was ignored:\n%s", body)
	}
}

// Status is the one kind that asks the chain rather than the index, because
// "absent" has to be answerable for a path the index has never heard of.
func TestShieldStatusReadsTheChain(t *testing.T) {
	tests := []struct {
		name, status, wantMsg, wantColor string
	}{
		{"live", "live", ">live<", "#4c1"},
		{"parked", "inert", ">parked<", "#dfb317"},
		{"absent", "absent", ">absent<", "#9f9f9f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Params struct{ Path, Data string } `json:"params"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("decode: %v", err)
				}
				data := fmt.Sprintf(`{"path":"gno.land/r/alpha/board","status":%q}`, tt.status)
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"response": map[string]any{
					"ResponseBase": map[string]any{"Data": base64.StdEncoding.EncodeToString([]byte(data))},
				}}})
			}))
			defer srv.Close()

			api, _ := newTestAPI(t)
			api.networks = []config.NetworkConfig{{ID: "alpha"}}
			api.rpcPick = map[string]string{"alpha": srv.URL}
			mux := http.NewServeMux()
			api.RegisterRoutes(mux)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/shield/status/r/alpha/board?network=alpha", nil))
			body := rec.Body.String()
			if !strings.Contains(body, tt.wantMsg) {
				t.Errorf("want %s in:\n%s", tt.wantMsg, body)
			}
			if !strings.Contains(body, tt.wantColor) {
				t.Errorf("want colour %s in:\n%s", tt.wantColor, body)
			}
			if !strings.Contains(body, ">realm<") {
				t.Errorf("an r/ path is a realm:\n%s", body)
			}
		})
	}
}

// An RPC that did not answer is not an absent package: saying "absent" would
// tell a reader their realm had been removed because a node was restarting.
func TestShieldStatusWithNoChainSaysUnknown(t *testing.T) {
	mux, _ := badgeMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/shield/status/r/alpha/board?network=alpha", nil))
	body := rec.Body.String()
	if !strings.Contains(body, ">unknown<") {
		t.Errorf("want unknown, got:\n%s", body)
	}
	if strings.Contains(body, ">absent<") {
		t.Errorf("an unreachable chain was reported as an absent package:\n%s", body)
	}
}

// A badge written into a README before the realm is deployed is a normal
// thing to do, and the index cannot tell that case apart from a typo. The
// chain can.
func TestShieldCountsSayWhyThePathIsMissing(t *testing.T) {
	tests := []struct {
		name, status, want string
	}{
		{"never deployed", "absent", ">not deployed<"},
		{"submitted, awaiting an approver", "inert", ">parked<"},
		{"on the chain, not yet indexed", "live", ">0<"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data := fmt.Sprintf(`{"path":"gno.land/r/alpha/soon","status":%q}`, tt.status)
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"response": map[string]any{
					"ResponseBase": map[string]any{"Data": base64.StdEncoding.EncodeToString([]byte(data))},
				}}})
			}))
			defer srv.Close()

			api, _ := newTestAPI(t)
			api.networks = []config.NetworkConfig{{ID: "alpha"}}
			api.rpcPick = map[string]string{"alpha": srv.URL}
			mux := http.NewServeMux()
			api.RegisterRoutes(mux)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/shield/txs/r/alpha/soon?network=alpha", nil))
			body := rec.Body.String()
			if !strings.Contains(body, tt.want) {
				t.Errorf("want %s in:\n%s", tt.want, body)
			}
			if !strings.Contains(body, ">txs<") {
				t.Errorf("the badge stopped naming what it measures:\n%s", body)
			}
			if rec.Header().Get("X-Badge-Error") != "" {
				t.Errorf("answered as an error: %q", rec.Header().Get("X-Badge-Error"))
			}
		})
	}
}

// With no chain to ask, the index's own answer stands: a path it does not hold
// is a path nobody here can say anything about.
func TestShieldCountsStillFailWhenTheChainCannotBeAsked(t *testing.T) {
	mux, _ := badgeMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/shield/txs/r/alpha/nope?network=alpha", nil))
	if got := rec.Header().Get("X-Badge-Error"); got != "no such package" {
		t.Errorf("X-Badge-Error = %q, want \"no such package\"", got)
	}
}

// The public RPC goes away for minutes at a time (rpc.gno.land answered 403 to
// everything, `health` included, on 2026-09-28), and a README full of grey
// `unknown` badges for the duration reads as the badges being broken.
func TestShieldStatusSurvivesAnRPCOutage(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			// What the outage actually looks like: not a refusal, a 403 page.
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("<html><head><title>403 Forbidden</title></head></html>"))
			return
		}
		data := `{"path":"gno.land/r/alpha/board","status":"live"}`
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"response": map[string]any{
			"ResponseBase": map[string]any{"Data": base64.StdEncoding.EncodeToString([]byte(data))},
		}}})
	}))
	defer srv.Close()

	api, _ := newTestAPI(t)
	api.networks = []config.NetworkConfig{{ID: "alpha"}}
	api.rpcPick = map[string]string{"alpha": srv.URL}
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/shield/status/r/alpha/board?network=alpha", nil))
		m := regexp.MustCompile(`aria-label="([^"]*)"`).FindStringSubmatch(rec.Body.String())
		if m == nil {
			t.Fatalf("no aria-label in:\n%s", rec.Body.String())
		}
		return m[1]
	}

	if got := get(); got != "realm: live" {
		t.Fatalf("with the chain up: %q, want realm: live", got)
	}
	fail.Store(true)
	if got := get(); got != "realm: live" {
		t.Errorf("through the outage: %q, want the last answer the chain gave", got)
	}

	// A path nobody has ever asked about has no last answer, and inventing one
	// would be the whole point of this thing missed.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/_badges/shield/status/r/alpha/never?network=alpha", nil))
	if !strings.Contains(rec.Body.String(), ">unknown<") {
		t.Errorf("an unseen path through an outage should be unknown:\n%s", rec.Body.String())
	}
}

// A day is the limit. Past it, "we could not reach the chain" is the honest
// answer and the remembered one is a guess about a chain nobody has reached
// since yesterday.
func TestStatusMemoryForgets(t *testing.T) {
	m := newStatusMemory(time.Hour)
	m.remember("k", PackageStatusLive)
	if got := m.recall("k"); got != PackageStatusLive {
		t.Fatalf("recall = %q, want live", got)
	}

	m.mu.Lock()
	e := m.m["k"]
	e.at = time.Now().Add(-2 * time.Hour)
	m.m["k"] = e
	m.mu.Unlock()

	if got := m.recall("k"); got != "" {
		t.Errorf("recall of a stale entry = %q, want empty", got)
	}
}

// Every key is a path out of a URL anybody can write, so the map has to have
// a ceiling or the badge route is a memory leak with a public endpoint on it.
func TestStatusMemoryIsBounded(t *testing.T) {
	m := newStatusMemory(time.Hour)
	for i := 0; i < statusMemoryMax*2+10; i++ {
		m.remember("gno.land/r/x/"+strconv.Itoa(i), PackageStatusAbsent)
	}
	m.mu.Lock()
	n := len(m.m)
	m.mu.Unlock()
	if n > statusMemoryMax {
		t.Errorf("held %d entries, cap is %d", n, statusMemoryMax)
	}
	// And the thing just written is still there, or the drop took the answer
	// the caller is about to ask for.
	if got := m.recall("gno.land/r/x/" + strconv.Itoa(statusMemoryMax*2+9)); got != PackageStatusAbsent {
		t.Errorf("the most recent entry was dropped: %q", got)
	}
}

// A nil memory is the shape every tool and several tests build, and it has to
// behave exactly as the code did before the memory existed.
func TestStatusMemoryNilIsUsable(t *testing.T) {
	var m *statusMemory
	m.remember("k", PackageStatusLive)
	if got := m.recall("k"); got != "" {
		t.Errorf("nil memory recalled %q", got)
	}
}

// The endpoint route is the same numbers through shields.io's renderer, which
// is how anyone gets a style this one does not draw.
func TestShieldEndpointAnswersTheSchemaShieldsExpects(t *testing.T) {
	mux, api := badgeMux(t)
	seedShieldRealm(t, api, "gno.land/r/alpha/board", 7)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/shield/users/r/alpha/board?network=alpha", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		SchemaVersion int    `json:"schemaVersion"`
		Label         string `json:"label"`
		Message       string `json:"message"`
		Color         string `json:"color"`
		CacheSeconds  int    `json:"cacheSeconds"`
		IsError       bool   `json:"isError"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if got.SchemaVersion != 1 {
		t.Errorf("schemaVersion = %d, want 1: shields rejects anything else", got.SchemaVersion)
	}
	if got.Label != "users" || got.Message != "3" {
		t.Errorf("label/message = %q/%q, want users/3", got.Label, got.Message)
	}
	if got.CacheSeconds == 0 {
		t.Error("no cacheSeconds, so shields caches on its own floor and nothing here decides freshness")
	}
	if got.IsError {
		t.Error("a served number was marked as an error")
	}
}

// Shields draws its own generic error for a non-200 and throws the body away,
// so a failure here has to arrive as a successful response that says so.
func TestShieldEndpointReportsFailureInBand(t *testing.T) {
	mux, api := badgeMux(t)
	seedShieldRealm(t, api, "gno.land/r/alpha/board", 2)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/shield/txs/r/alpha/bord?network=alpha", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200 with isError", rec.Code)
	}
	var got struct {
		Message string `json:"message"`
		IsError bool   `json:"isError"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.IsError || got.Message != "no such package" {
		t.Errorf("got %+v, want isError with the reason", got)
	}
}
