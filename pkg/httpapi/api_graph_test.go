package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/store"
)

type activeResponse struct {
	Network string   `json:"network"`
	Window  string   `json:"window"`
	Paths   []string `json:"paths"`
}

func TestHandleGraphActive(t *testing.T) {
	api, db := newTestAPI(t)
	seedContractsAPI(t, db)

	tests := []struct {
		name    string
		url     string
		window  string
		network string
		want    int
	}{
		// The fixture's packages and calls are all 48h old, so a 24h window
		// keeps nothing and an all-time one keeps the two called realms.
		{"all time names every called path", "/api/graph/active?network=alpha", "all", "alpha", 2},
		{"an explicit all is the same", "/api/graph/active?network=alpha&window=all", "all", "alpha", 2},
		{"a short window excludes everything older", "/api/graph/active?network=alpha&window=24h", "24h", "alpha", 0},
		{"90d reaches back past the fixture", "/api/graph/active?network=alpha&window=90d", "90d", "alpha", 3},
		// Unlike the contracts map, an absent network is the union rather than
		// a silent fallback to the first chain: this returns paths, which
		// carry no per-chain quantity to be mixed up. Still 2, because beta's
		// only called path is one alpha already has, and a union dedups it.
		{"no network is the union over every chain", "/api/graph/active", "all", "", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got activeResponse
			getJSON(t, api.HandleGraphActive, tt.url, &got)
			if got.Window != tt.window {
				t.Errorf("window = %q, want %q", got.Window, tt.window)
			}
			if got.Network != tt.network {
				t.Errorf("network = %q, want %q", got.Network, tt.network)
			}
			if len(got.Paths) != tt.want {
				t.Errorf("paths = %v, want %d of them", got.Paths, tt.want)
			}
		})
	}
}

// A window name the UI never offers is a typo, and answering it as all-time
// would produce a plausible set for the wrong period.
func TestHandleGraphActiveRejectsUnknownWindow(t *testing.T) {
	api, db := newTestAPI(t)
	seedContractsAPI(t, db)

	rec := httptest.NewRecorder()
	api.HandleGraphActive(rec, httptest.NewRequest(http.MethodGet, "/api/graph/active?window=1w", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// The reverse dependency graph's depth cap, at the layer the frontend talks to.
//
// The funnel on the deps tab is drawn from one hop, so the handler's default
// has to be one hop: a default that walked the closure would draw somebody
// else's dependents above the subject and no control on the page would say so.
// ?depth=0 is the escape hatch for a caller that wants the whole closure, and
// it has to keep working because that is what this endpoint always returned.
func TestHandleDepsReverseDepth(t *testing.T) {
	api, db := newTestAPI(t)

	// util <- lib <- app: two hops from util to app, on one network.
	for i, p := range []string{"gno.land/p/demo/util", "gno.land/p/demo/lib", "gno.land/r/demo/app"} {
		if err := db.UpsertPackage("alpha", p, "n", "g1creator", "TX", 10+i,
			"2026-08-01T00:00:00Z", i == 2, 1); err != nil {
			t.Fatalf("upsert %s: %v", p, err)
		}
	}
	for _, e := range [][2]string{
		{"gno.land/p/demo/lib", "gno.land/p/demo/util"},
		{"gno.land/r/demo/app", "gno.land/p/demo/lib"},
	} {
		if err := db.SetDependencies("alpha", e[0], []string{e[1]}); err != nil {
			t.Fatalf("deps %v: %v", e, err)
		}
	}

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	tests := []struct {
		name      string
		url       string
		wantApp   bool
		wantCode  int
		wantLibAt bool // lib carries its own dependents rather than an empty list
	}{
		{
			name: "the default stops at one hop",
			url:  "/api/deps/p/demo/util?network=alpha&dir=dependents",
		},
		{
			name: "an explicit depth of one is the same",
			url:  "/api/deps/p/demo/util?network=alpha&dir=dependents&depth=1",
		},
		{
			name:      "depth zero still walks the whole closure",
			url:       "/api/deps/p/demo/util?network=alpha&dir=dependents&depth=0",
			wantApp:   true,
			wantLibAt: true,
		},
		{
			name:      "depth two reaches app",
			url:       "/api/deps/p/demo/util?network=alpha&dir=dependents&depth=2",
			wantApp:   true,
			wantLibAt: true,
		},
		{
			name:     "a negative depth is a typo, not unbounded",
			url:      "/api/deps/p/demo/util?network=alpha&dir=dependents&depth=-1",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "a non-numeric depth is rejected rather than ignored",
			url:      "/api/deps/p/demo/util?network=alpha&dir=dependents&depth=deep",
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.url, nil))

			want := tt.wantCode
			if want == 0 {
				want = http.StatusOK
			}
			if rec.Code != want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, want, rec.Body.String())
			}
			if want != http.StatusOK {
				return
			}

			var graph map[string][]string
			if err := json.Unmarshal(rec.Body.Bytes(), &graph); err != nil {
				t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
			}
			if got := graph["gno.land/p/demo/util"]; len(got) != 1 || got[0] != "gno.land/p/demo/lib" {
				t.Errorf("util dependents = %v, want [gno.land/p/demo/lib]", got)
			}
			// lib is always present: it is what util's one edge points at, and
			// a node an edge points at has to exist or the funnel cannot draw
			// the edge.
			if _, ok := graph["gno.land/p/demo/lib"]; !ok {
				t.Errorf("lib is missing from the graph: %v", graph)
			}
			if got := len(graph["gno.land/p/demo/lib"]) > 0; got != tt.wantLibAt {
				t.Errorf("lib expanded = %v, want %v: %v", got, tt.wantLibAt, graph)
			}
			if _, ok := graph["gno.land/r/demo/app"]; ok != tt.wantApp {
				t.Errorf("app present = %v, want %v: %v", ok, tt.wantApp, graph)
			}
		})
	}
}

// seedGraphAPI writes one transfer edge and one caller edge on alpha, and
// stores alpha's newest block, so every graph endpoint has something to stamp.
func seedGraphAPI(t *testing.T, db *store.DB) {
	t.Helper()
	day := time.Now().UTC().Format("2006-01-02")
	if err := db.UpsertTransferEdges("alpha", []store.TransferEdgeRow{
		{FromAddress: "g1ego", ToAddress: "g1a", Day: day, TotalValue: 100, TxCount: 1, LastHeight: 41},
		{FromAddress: "g1a", ToAddress: "g1b", Day: day, TotalValue: 30, TxCount: 1, LastHeight: 41},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCallerEdges("alpha", []store.CallerEdgeRow{
		{Caller: "g1ego", PkgPath: "gno.land/r/demo/x", Day: day, Calls: 3, LastHeight: 41},
		{Caller: "g1a", PkgPath: "gno.land/r/demo/x", Day: day, Calls: 2, LastHeight: 41},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertBlock("alpha", 42, "2026-10-26T10:00:00Z", 0, 1); err != nil {
		t.Fatal(err)
	}
}

// withLiveTip stands in for a live feed that has polled alpha, and puts the
// map back afterwards: liveFeeds is a package global.
func withLiveTip(t *testing.T, network string, height int, at time.Time) {
	t.Helper()
	prev, had := liveFeeds[network]
	liveFeeds[network] = &liveFeed{networkID: network, lastBlock: height, checkedAt: at}
	t.Cleanup(func() {
		if had {
			liveFeeds[network] = prev
		} else {
			delete(liveFeeds, network)
		}
	})
}

type stampedResponse struct {
	Network       string `json:"network"`
	ChainID       string `json:"chain_id"`
	IndexedHeight int    `json:"indexed_height"`
	IndexedTime   string `json:"indexed_time"`
	LiveTipHeight int    `json:"live_tip_height"`
	LiveTipAt     string `json:"live_tip_checked_at"`

	Days        int    `json:"days"`
	Window      string `json:"window"`
	Granularity string `json:"granularity"`
	TopN        int    `json:"top_n"`
	Ego         string `json:"ego"`
	Hops        int    `json:"hops"`
	Dropped     *int   `json:"dropped"`
	StoppedAt   int    `json:"stopped_at_hop"`
	Until       string `json:"until"`
	MinCalls    *int   `json:"min_calls"`
	MinValue    *int64 `json:"min_value"`

	Nodes []json.RawMessage `json:"nodes"`
	Edges []json.RawMessage `json:"edges"`
}

// Every graph payload says what it is and when it is true: the chain, the
// height it was computed at, the live tip as the header's feed last saw it,
// and every parameter the handler read, defaults included.
func TestGraphPayloadsDeclareTheirProvenance(t *testing.T) {
	api, db := newTestAPI(t)
	seedGraphAPI(t, db)
	seedContractsAPI(t, db)
	api.setChainID("alpha", "alpha-1")
	checked := time.Date(2026, 10, 26, 10, 0, 5, 0, time.UTC)
	withLiveTip(t, "alpha", 45, checked)

	endpoints := []struct {
		name string
		h    http.HandlerFunc
		url  string
	}{
		{"transfers", api.HandleGraphTransfers, "/api/graph/transfers?network=alpha"},
		{"callers", api.HandleGraphCallers, "/api/graph/callers?network=alpha"},
		{"contracts map", api.HandleContractsMap, "/api/contracts/map?network=alpha"},
		{"contracts edges", api.HandleContractsEdges, "/api/contracts/edges?network=alpha"},
	}
	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			var got stampedResponse
			getJSON(t, ep.h, ep.url, &got)
			if got.Network != "alpha" || got.ChainID != "alpha-1" {
				t.Errorf("network, chain_id = %q, %q; want alpha, alpha-1", got.Network, got.ChainID)
			}
			if got.IndexedHeight != 42 || got.IndexedTime != "2026-10-26T10:00:00Z" {
				t.Errorf("indexed = %d at %q, want 42 at the stored block's time", got.IndexedHeight, got.IndexedTime)
			}
			if got.LiveTipHeight != 45 || got.LiveTipAt != checked.Format(time.RFC3339) {
				t.Errorf("live tip = %d checked %q, want the feed's 45 checked %s", got.LiveTipHeight, got.LiveTipAt, checked.Format(time.RFC3339))
			}
		})
	}

	t.Run("transfers echo their defaults", func(t *testing.T) {
		var got stampedResponse
		getJSON(t, api.HandleGraphTransfers, "/api/graph/transfers?network=alpha", &got)
		if got.Days != 30 || got.Granularity != "daily" || got.TopN != 100 || got.MinValue == nil || *got.MinValue != 0 {
			t.Errorf("echo = days %d, granularity %q, top_n %d, min_value %v; want 30, daily, 100, 0", got.Days, got.Granularity, got.TopN, got.MinValue)
		}
		// Without an ego there is no radius and no frontier to report.
		if got.Hops != 0 || got.Dropped != nil {
			t.Errorf("hops %d, dropped %v without an ego; want both absent", got.Hops, got.Dropped)
		}
	})

	t.Run("an ego echoes its radius and what it dropped", func(t *testing.T) {
		var got stampedResponse
		getJSON(t, api.HandleGraphTransfers, "/api/graph/transfers?network=alpha&ego=g1ego&hops=2", &got)
		if got.Ego != "g1ego" || got.Hops != 2 || got.Dropped == nil || *got.Dropped != 0 {
			t.Errorf("ego %q hops %d dropped %v; want g1ego, 2, 0", got.Ego, got.Hops, got.Dropped)
		}
		if len(got.Nodes) != 3 {
			t.Errorf("two hops from g1ego = %d nodes, want 3", len(got.Nodes))
		}
	})

	// A budget of one is spent by hop 1, so a two-hop walk stops there and
	// the payload says so rather than looking like a complete neighbourhood.
	t.Run("a walk the budget stopped says where", func(t *testing.T) {
		var got stampedResponse
		getJSON(t, api.HandleGraphTransfers, "/api/graph/transfers?network=alpha&ego=g1ego&hops=2&topN=1", &got)
		if got.StoppedAt != 1 || len(got.Nodes) != 2 {
			t.Errorf("stopped_at_hop %d with %d nodes; want 1 and the ego with its one counterparty", got.StoppedAt, len(got.Nodes))
		}
	})

	t.Run("an ego with no hops is one hop", func(t *testing.T) {
		var got stampedResponse
		getJSON(t, api.HandleGraphTransfers, "/api/graph/transfers?network=alpha&ego=g1ego", &got)
		if got.Hops != 1 || len(got.Nodes) != 2 {
			t.Errorf("hops %d with %d nodes; want 1 and 2", got.Hops, len(got.Nodes))
		}
	})

	t.Run("callers echo until as the day the window ended", func(t *testing.T) {
		var got stampedResponse
		getJSON(t, api.HandleGraphCallers, "/api/graph/callers?network=alpha&days=3650", &got)
		if got.Until != time.Now().UTC().Format("2006-01-02") {
			t.Errorf("until = %q, want today", got.Until)
		}
		// 3650 days at the default daily granularity is capped, and the echo
		// is where a caller finds that out.
		if got.Days != 365 || got.TopN != 200 || got.MinCalls == nil {
			t.Errorf("echo = days %d top_n %d min_calls %v; want 365, 200, 0", got.Days, got.TopN, got.MinCalls)
		}
	})

	t.Run("a caller ego carries ego and dropped", func(t *testing.T) {
		var got stampedResponse
		getJSON(t, api.HandleGraphCallers, "/api/graph/callers?network=alpha&ego=gno.land/r/demo/x&topN=1", &got)
		if got.Ego != "gno.land/r/demo/x" || got.Dropped == nil || *got.Dropped != 1 || len(got.Nodes) != 2 {
			t.Errorf("ego %q dropped %v nodes %d; want the realm, 1 dropped, 2 nodes", got.Ego, got.Dropped, len(got.Nodes))
		}
	})
}

// Unknown is absent, not zero: a cold feed has no tip and a fresh database
// has no stored block, and a 0 in either field would read as block zero.
func TestGraphProvenanceOmitsWhatItDoesNotKnow(t *testing.T) {
	api, _ := newTestAPI(t)
	rec := getJSON(t, api.HandleGraphTransfers, "/api/graph/transfers?network=beta", nil)
	for _, key := range []string{"indexed_height", "live_tip_height", "live_tip_checked_at", "chain_id"} {
		if strings.Contains(rec.Body.String(), `"`+key+`"`) {
			t.Errorf("%s present with nothing to report: %s", key, rec.Body.String())
		}
	}
}

// hops is refused outside 1..5 rather than clamped, the way until refuses a
// malformed date: a neighbourhood quietly answered at another radius looks
// exactly like the one asked for.
func TestGraphTransfersRejectsHopsOutOfRange(t *testing.T) {
	api, db := newTestAPI(t)
	seedGraphAPI(t, db)

	tests := []struct {
		hops string
		want int
	}{
		{"1", http.StatusOK},
		{"5", http.StatusOK},
		{"0", http.StatusBadRequest},
		{"6", http.StatusBadRequest},
		{"-1", http.StatusBadRequest},
		{"two", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run("hops="+tt.hops, func(t *testing.T) {
			rec := httptest.NewRecorder()
			api.HandleGraphTransfers(rec, httptest.NewRequest(http.MethodGet,
				"/api/graph/transfers?network=alpha&ego=g1ego&hops="+tt.hops, nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "1 to 5") {
				t.Errorf("400 does not state the range: %s", rec.Body.String())
			}
		})
	}
}

// The graph keys every existing reader depends on come first and unchanged:
// a payload that gained fields still opens with nodes then edges.
func TestGraphTransfersEgoKeepsItsGraphBytes(t *testing.T) {
	api, db := newTestAPI(t)
	seedGraphAPI(t, db)

	plain := getJSON(t, api.HandleGraphTransfers, "/api/graph/transfers?network=alpha&ego=g1ego", nil).Body.String()
	one := getJSON(t, api.HandleGraphTransfers, "/api/graph/transfers?network=alpha&ego=g1ego&hops=1", nil).Body.String()
	if plain != one {
		t.Fatalf("ego and ego&hops=1 differ:\n%s\n%s", plain, one)
	}
	const want = `{"nodes":[{"id":"g1ego","volume":100},{"id":"g1a","volume":100}],"edges":[{"from":"g1ego","to":"g1a","value":100,"tx_count":1}],`
	if !strings.HasPrefix(plain, want) {
		t.Fatalf("payload does not open with the pre-hops graph:\n got %s\nwant %s...", plain, want)
	}
}
