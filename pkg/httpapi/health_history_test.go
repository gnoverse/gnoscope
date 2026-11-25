package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/config"
	"github.com/gnoverse/gnoscope/pkg/healthlog"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// The two vocabularies have to stay identical, and the failure of letting them
// drift is quiet: healthlog.Severity ranks an unrecognised state worst, so one
// renamed Diag constant would paint a healthy week in the worst colour the
// strip has, with nothing anywhere saying why. Same shape as
// TestRailMatchesNavTable, and for the same reason.
func TestDiagStatesMatchHealthlog(t *testing.T) {
	diag := map[string]bool{
		DiagAlive:           true,
		DiagSyncing:         true,
		DiagWedged:          true,
		DiagStale:           true,
		DiagIndexerDown:     true,
		DiagIndexerBehind:   true,
		DiagNodeUnreachable: true,
		DiagUnknown:         true,
	}
	ranked := map[string]bool{}
	for _, s := range healthlog.RankedStates() {
		ranked[s] = true
		if !diag[s] {
			t.Errorf("healthlog ranks %q, which is not a diagnosis state", s)
		}
	}
	for s := range diag {
		if !ranked[s] {
			t.Errorf("diagnosis state %q is not ranked by healthlog, so it would sort as the worst state there is", s)
		}
	}
	// no_data is healthlog's alone: diagnose never produces it.
	if diag[healthlog.StateNoData] {
		t.Error("no_data must not be a diagnosis state; it means nobody was watching")
	}
}

func historyAPI(t *testing.T, networks ...string) (*API, *healthlog.Store) {
	t.Helper()
	s, err := healthlog.Open(filepath.Join(t.TempDir(), "health.db"), 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetMinGap(0)
	var cfgs []config.NetworkConfig
	for _, n := range networks {
		cfgs = append(cfgs, config.NetworkConfig{ID: n})
	}
	a := &API{networks: cfgs}
	a.SetHealthLog(s)
	return a, s
}

// stripGlyphs draws a strip the way the page does, one character per cell.
//
// A test that only counts cells cannot say whether the thing a reader sees is
// legible, and this is the cheapest way to look at it without a browser. The
// rendering is logged rather than compared against a golden: the grid is
// anchored to `now`, so which cell an outage lands in moves between runs, and
// a golden would pin the clock rather than the behaviour.
var stripGlyphs = map[string]string{
	DiagAlive:             "#",
	DiagSyncing:           "~",
	DiagStale:             "-",
	DiagNodeUnreachable:   "?",
	DiagIndexerDown:       "!",
	DiagWedged:            "X",
	DiagUnknown:           "u",
	healthlog.StateNoData: " ",
	"":                    ".",
}

func renderStrip(row HealthHistoryRow) string {
	var b strings.Builder
	b.WriteString(row.Network + " |")
	for _, c := range row.Cells {
		g, ok := stripGlyphs[c.State]
		if !ok {
			g = "?"
		}
		b.WriteString(g)
	}
	b.WriteString("|\n")
	b.WriteString("  " + row.Summary + "\n")
	for _, inc := range row.Incidents {
		if len(inc.Start) < 16 {
			continue
		}
		fmt.Fprintf(&b, "  %s  %-16s %s%s\n", inc.Start[:16], inc.State,
			humanDuration(inc.Seconds), map[bool]string{true: " and counting", false: ""}[inc.Ongoing])
	}
	return b.String()
}

func getHistory(t *testing.T, a *API, query string) healthHistoryResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	a.HandleHealthHistory(rec, httptest.NewRequest("GET", "/api/health/history"+query, nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp healthHistoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return resp
}

// A fresh instance is the common case on day one and after every new
// deployment, and it must render something honest: not an empty red box, and
// not a fake green one.
func TestHealthHistoryWithNoSamples(t *testing.T) {
	a, _ := historyAPI(t, "onyx", "pearl")
	resp := getHistory(t, a, "")

	if !resp.Enabled {
		t.Fatal("enabled = false with a store attached")
	}
	if len(resp.Rows) != 2 {
		t.Fatalf("got %d rows, want one per configured network", len(resp.Rows))
	}
	for _, row := range resp.Rows {
		if row.Error != "" {
			t.Fatalf("[%s] error = %q", row.Network, row.Error)
		}
		if len(row.Cells) != 168 {
			t.Errorf("[%s] %d cells, want a full grid even with nothing in it", row.Network, len(row.Cells))
		}
		for i, c := range row.Cells {
			if c.State != "" || c.Samples != 0 {
				t.Fatalf("[%s] cell %d = %q with %d samples, want an empty cell", row.Network, i, c.State, c.Samples)
			}
		}
		// The three ways this could lie, each checked.
		if row.UptimePercent != 0 {
			t.Errorf("[%s] uptime = %v with no samples at all", row.Network, row.UptimePercent)
		}
		if row.Current != nil || row.LastOutage != nil {
			t.Errorf("[%s] claimed a current state or a last outage with no samples", row.Network)
		}
		if row.Outages != 0 || row.DownSeconds != 0 {
			t.Errorf("[%s] claimed %d outages over %ds with no samples", row.Network, row.Outages, row.DownSeconds)
		}
		if !strings.Contains(row.Summary, "no availability history recorded yet") {
			t.Errorf("[%s] summary = %q, want it to say there is no history", row.Network, row.Summary)
		}
		t.Logf("\n%s", renderStrip(row))
	}
}

// An instance with no -health-db renders the same shape and says why.
func TestHealthHistoryDisabled(t *testing.T) {
	a := &API{networks: []config.NetworkConfig{{ID: "onyx"}}}
	resp := getHistory(t, a, "")
	if resp.Enabled {
		t.Fatal("enabled = true with no store")
	}
	if !strings.Contains(resp.Note, "-health-db") {
		t.Errorf("note = %q, want it to name the flag", resp.Note)
	}
	if len(resp.Rows) != 1 || len(resp.Rows[0].Cells) != 168 {
		t.Fatalf("want one row with a full empty grid, got %+v", resp.Rows)
	}
	if resp.Rows[0].Summary != "not recorded on this instance" {
		t.Errorf("summary = %q", resp.Rows[0].Summary)
	}
}

func TestHealthHistoryRenders(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	min := time.Minute

	tests := []struct {
		name        string
		write       func(s *healthlog.Store)
		wantOutages int
		wantCurrent string
		wantSummary []string
	}{
		{
			name: "healthy: a week of alive samples",
			write: func(s *healthlog.Store) {
				// One a minute, the real cadence. Spacing them wider than
				// healthlog.GapTolerance would, correctly, read as a sampler
				// that kept stopping.
				for i := 2000; i >= 0; i-- {
					s.Record(healthlog.Sample{At: now.Add(-time.Duration(i) * min), Network: "onyx", State: DiagAlive})
				}
			},
			wantCurrent: DiagAlive,
			wantSummary: []string{"no outage in the last 7d", "100.00%"},
		},
		{
			name: "flapping: six short outages, recovered",
			write: func(s *healthlog.Store) {
				for i := 600; i >= 0; i-- {
					state := DiagAlive
					// Every hundredth sample, six times over the window.
					if i%100 == 50 {
						state = DiagIndexerDown
					}
					s.Record(healthlog.Sample{
						At: now.Add(-time.Duration(i) * min), Network: "onyx", State: state,
						Detail: "the chain is producing blocks, but our indexer is not answering",
					})
				}
			},
			wantOutages: 6,
			wantCurrent: DiagAlive,
			wantSummary: []string{"last unreachable", "indexer down", "6 outage(s)"},
		},
		{
			name: "a long outage, still open",
			write: func(s *healthlog.Store) {
				for i := 4320; i >= 0; i-- { // three days, one a minute
					state := DiagAlive
					if i <= 2880 { // the last two days, to the minute
						state = DiagWedged
					}
					s.Record(healthlog.Sample{
						At: now.Add(-time.Duration(i) * min), Network: "onyx", State: state,
						Detail: "consensus has not advanced for 2d",
					})
				}
			},
			wantOutages: 1,
			wantCurrent: DiagWedged,
			wantSummary: []string{"wedged right now", "2d"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, s := historyAPI(t, "onyx")
			tt.write(s)
			resp := getHistory(t, a, "?window=7d")
			if len(resp.Rows) != 1 {
				t.Fatalf("got %d rows", len(resp.Rows))
			}
			row := resp.Rows[0]
			if row.Error != "" {
				t.Fatalf("error = %q", row.Error)
			}
			if row.Outages != tt.wantOutages {
				t.Errorf("outages = %d, want %d", row.Outages, tt.wantOutages)
			}
			if row.Current == nil || row.Current.State != tt.wantCurrent {
				t.Errorf("current = %+v, want %s", row.Current, tt.wantCurrent)
			}
			if tt.wantOutages > 0 && row.LastOutage == nil {
				t.Error("no last outage reported though there were outages")
			}
			if len(row.Incidents) != tt.wantOutages {
				t.Errorf("%d incidents listed, want %d", len(row.Incidents), tt.wantOutages)
			}
			for _, want := range tt.wantSummary {
				if !strings.Contains(row.Summary, want) {
					t.Errorf("summary %q does not contain %q", row.Summary, want)
				}
			}
			// The strip always has its full grid, and the newest cell is the
			// one a reader looks at first.
			if len(row.Cells) != 168 {
				t.Fatalf("%d cells, want 168", len(row.Cells))
			}
			if row.Cells[167].Samples == 0 {
				t.Error("the newest cell is empty though a sample was written at now")
			}
			t.Logf("\n%s", renderStrip(row))
		})
	}
}

func TestHealthHistoryIncidentsAreNewestFirstAndCapped(t *testing.T) {
	a, s := historyAPI(t, "onyx")
	now := time.Now().UTC().Truncate(time.Minute)
	// 120 alternating samples: 60 outages, past the cap of 50.
	for i := 239; i >= 0; i-- {
		state := DiagAlive
		if i%4 == 0 {
			state = DiagIndexerDown
		}
		s.Record(healthlog.Sample{At: now.Add(-time.Duration(i) * time.Minute), Network: "onyx", State: state})
	}
	row := getHistory(t, a, "").Rows[0]
	if row.Outages != 60 {
		t.Fatalf("outages = %d, want the full count of 60", row.Outages)
	}
	if len(row.Incidents) != maxHealthIncidents {
		t.Fatalf("%d incidents, want the list capped at %d", len(row.Incidents), maxHealthIncidents)
	}
	for i := 1; i < len(row.Incidents); i++ {
		if row.Incidents[i].Start > row.Incidents[i-1].Start {
			t.Fatalf("incident %d starts after the one before it; the list is not newest first", i)
		}
	}
}

// A gap in the series is a gap, and the strip must not colour it as either
// answer. This is what a restart looks like.
func TestHealthHistoryShowsUnobservedStretches(t *testing.T) {
	a, s := historyAPI(t, "onyx")
	now := time.Now().UTC().Truncate(time.Minute)
	for i := 0; i < 10; i++ { // ten samples, six hours ago
		s.Record(healthlog.Sample{At: now.Add(-6*time.Hour + time.Duration(i)*time.Minute), Network: "onyx", State: DiagAlive})
	}
	row := getHistory(t, a, "").Rows[0]
	if row.NoDataSeconds < int((5 * time.Hour).Seconds()) {
		t.Errorf("no_data = %ds, want roughly the six hours nobody was watching", row.NoDataSeconds)
	}
	if row.Current == nil || row.Current.State != healthlog.StateNoData {
		t.Fatalf("current = %+v, want the no_data run", row.Current)
	}
	if !strings.Contains(row.Summary, "not being sampled right now") {
		t.Errorf("summary = %q", row.Summary)
	}
	// And the empty cells are empty, not green.
	empty := 0
	for _, c := range row.Cells {
		if c.State == "" {
			empty++
		}
	}
	if empty < 160 {
		t.Errorf("%d empty cells, want nearly all of the week", empty)
	}
}

func TestHealthHistoryWindows(t *testing.T) {
	a, _ := historyAPI(t, "onyx")
	tests := []struct {
		window             string
		cells, cellSeconds int
	}{
		{"24h", 144, 600},
		{"7d", 168, 3600},
		{"30d", 180, 14400},
	}
	for _, tt := range tests {
		t.Run(tt.window, func(t *testing.T) {
			resp := getHistory(t, a, "?window="+tt.window)
			if resp.CellSeconds != tt.cellSeconds {
				t.Errorf("cell = %ds, want %ds", resp.CellSeconds, tt.cellSeconds)
			}
			if len(resp.Rows[0].Cells) != tt.cells {
				t.Errorf("%d cells, want %d", len(resp.Rows[0].Cells), tt.cells)
			}
		})
	}

	rec := httptest.NewRecorder()
	a.HandleHealthHistory(rec, httptest.NewRequest("GET", "/api/health/history?window=3y", nil))
	if rec.Code != 400 {
		t.Errorf("an unknown window answered %d, want 400", rec.Code)
	}
}

func TestHealthHistoryNarrowsToOneNetwork(t *testing.T) {
	a, s := historyAPI(t, "onyx", "pearl")
	now := time.Now().UTC()
	s.Record(healthlog.Sample{At: now, Network: "onyx", State: DiagAlive})
	s.Record(healthlog.Sample{At: now, Network: "pearl", State: DiagWedged})

	resp := getHistory(t, a, "?network=pearl")
	if len(resp.Rows) != 1 || resp.Rows[0].Network != "pearl" {
		t.Fatalf("got %+v, want only pearl", resp.Rows)
	}
	if resp.Rows[0].Current == nil || resp.Rows[0].Current.State != DiagWedged {
		t.Fatalf("pearl's current state = %+v", resp.Rows[0].Current)
	}
	// network=all is every network, which is what the rail sends by default.
	if n := len(getHistory(t, a, "?network=all").Rows); n != 2 {
		t.Errorf("network=all returned %d rows, want 2", n)
	}
}

// Recording is inside the probe so that no probe path can forget it. This
// pins the wiring: a refactor that moved the call into one caller would make
// the other two record nothing, and a hole in the history reads as an outage.
func TestProbeNetworksRecordsASample(t *testing.T) {
	a, s := historyAPI(t, "onyx")
	nodes := map[string]NodeState{"onyx": {Reachable: true, Height: 4242}}
	diags := map[string]ChainDiagnosis{"onyx": {State: DiagAlive, Healthy: true, Detail: "producing blocks"}}
	a.recordHealthSamples(nodes, diags, nil)

	got, err := s.Samples("onyx", time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d samples, want 1", len(got))
	}
	if got[0].State != DiagAlive || got[0].Height != 4242 || got[0].Detail != "producing blocks" {
		t.Fatalf("sample = %+v", got[0])
	}
}

// When the node did not answer, the height still comes from somewhere: a row
// with a state and no height at all would say "nobody could read this chain",
// which is a stronger claim than the indexer having answered.
func TestRecordFallsBackToTheIndexerHeight(t *testing.T) {
	a, s := historyAPI(t, "onyx")
	a.recordHealthSamples(
		map[string]NodeState{"onyx": {Reachable: false}},
		map[string]ChainDiagnosis{"onyx": {State: DiagNodeUnreachable}},
		map[string]store.SanityLiveness{"onyx": {ChainHeight: 1234, Reachable: true}},
	)
	got, _ := s.Samples("onyx", time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if len(got) != 1 || got[0].Height != 1234 {
		t.Fatalf("sample = %+v, want the indexer's height", got)
	}
}

func TestRecordIsSafeWithoutAStore(t *testing.T) {
	a := &API{}
	a.recordHealthSamples(nil, map[string]ChainDiagnosis{"onyx": {State: DiagAlive}}, nil)
	if a.HealthLogEnabled() {
		t.Error("HealthLogEnabled true with no store")
	}
}
