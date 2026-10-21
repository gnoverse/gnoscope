package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/syncer"
)

// alive is the shape of a network row with nothing wrong with it, so each case
// below can state only what it changes.
func cockpitHealthy(id string) CockpitNetwork {
	return CockpitNetwork{
		Network:       id,
		Diagnosis:     ChainDiagnosis{State: DiagAlive, Healthy: true, Detail: "producing blocks"},
		ChainHeight:   1000,
		IndexedHeight: 1000,
		Indexed:       true,
		Reachable:     true,
	}
}

// The grading is the whole reason this endpoint exists rather than the page
// joining six others in the browser, so it is the thing worth pinning: a
// verdict that said "degraded" on a wedged chain, or "all clear" on an index a
// day behind, would be worse than no page at all.
func TestCockpitFindings(t *testing.T) {
	// An hour of uptime, so the cold-start grace never masks a case that is
	// not about it.
	up := cockpitResponse{Service: CockpitService{UptimeSeconds: 3600}}

	tests := []struct {
		name     string
		resp     cockpitResponse
		want     []string // finding IDs, in the order they must be reported
		severity string   // the verdict state
	}{
		{
			name:     "a healthy instance reports nothing",
			resp:     cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{cockpitHealthy("alpha")}},
			want:     nil,
			severity: CockpitOK,
		},
		{
			name: "a wedged chain is down",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.Diagnosis = ChainDiagnosis{State: DiagWedged, Detail: "consensus has not advanced for 71d"}
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     []string{"chain"},
			severity: CockpitDown,
		},
		{
			name: "a node catching up is a warning, not an outage",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.Diagnosis = ChainDiagnosis{State: DiagSyncing, Detail: "the node is catching up"}
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     []string{"chain"},
			severity: CockpitDegraded,
		},
		{
			// A node we cannot ask is not a service that has stopped: the
			// indexer is still answering and the chain may be fine. Graded an
			// outage, every instance run without -rpc would be permanently red.
			name: "a node we cannot reach is a warning",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.Diagnosis = ChainDiagnosis{State: DiagNodeUnreachable, Detail: "no RPC endpoint is configured"}
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     []string{"chain"},
			severity: CockpitDegraded,
		},
		{
			name: "an indexer we cannot reach is an outage: it is our own data source",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.Diagnosis = ChainDiagnosis{State: DiagIndexerDown, Detail: "the chain is producing blocks"}
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     []string{"chain"},
			severity: CockpitDown,
		},
		{
			name: "an index a little behind is a warning",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.IndexedHeight, n.LagBlocks, n.LagSeconds = 900, 100, 300
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     []string{"lag"},
			severity: CockpitDegraded,
		},
		{
			name: "an index a long way behind is an outage",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.IndexedHeight, n.LagBlocks, n.LagSeconds = 0, 5000, 90000
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     []string{"lag"},
			severity: CockpitDown,
		},
		{
			name: "a lag inside the threshold is not a finding",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.IndexedHeight, n.LagBlocks = 1000-cockpitLagWarn+1, cockpitLagWarn-1
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     nil,
			severity: CockpitOK,
		},
		{
			name: "nothing indexed is not held against a process that just started",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.Indexed, n.IndexedHeight = false, 0
				return cockpitResponse{
					Service:  CockpitService{UptimeSeconds: 30},
					Networks: []CockpitNetwork{n},
				}
			}(),
			want:     nil,
			severity: CockpitOK,
		},
		{
			// A failed local read and an empty table look identical in the
			// fields above, and grading a database problem as "nothing has
			// been synced yet" would send somebody to wait for a backfill.
			name: "an unreadable index is an outage, not an empty one",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.Indexed, n.Error = false, "database is locked"
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     []string{"index-unreadable"},
			severity: CockpitDown,
		},
		{
			name: "nothing indexed an hour in is a finding",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.Indexed, n.IndexedHeight = false, 0
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     []string{"not-indexed"},
			severity: CockpitDegraded,
		},
		{
			name: "repeated sync failures are an outage",
			resp: func() cockpitResponse {
				n := cockpitHealthy("alpha")
				n.Sync = &syncer.NetworkHealth{ConsecutiveFailures: cockpitSyncFailDown, LastError: "422"}
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{n}}
			}(),
			want:     []string{"sync"},
			severity: CockpitDown,
		},
		{
			name: "an error rate over the threshold fires, with enough traffic behind it",
			resp: cockpitResponse{
				Service:  up.Service,
				Networks: []CockpitNetwork{cockpitHealthy("alpha")},
				Requests: &CockpitRequests{Requests: 1000, Errors: 200, ErrorRate: 0.2},
			},
			want:     []string{"errors"},
			severity: CockpitDegraded,
		},
		{
			name: "the same rate over three requests does not",
			resp: cockpitResponse{
				Service:  up.Service,
				Networks: []CockpitNetwork{cockpitHealthy("alpha")},
				Requests: &CockpitRequests{Requests: 3, Errors: 1, ErrorRate: 0.33},
			},
			want:     nil,
			severity: CockpitOK,
		},
		{
			name: "a dropped request log says so",
			resp: cockpitResponse{
				Service:  up.Service,
				Networks: []CockpitNetwork{cockpitHealthy("alpha")},
				Requests: &CockpitRequests{Requests: 10, Dropped: 42},
			},
			want:     []string{"log-dropped"},
			severity: CockpitDegraded,
		},
		{
			// The ranking is the point of the list: a wall display shows the
			// first three, and a warning ahead of an outage is the one
			// ordering that makes the page useless.
			name: "an outage outranks a warning whatever order the rules ran in",
			resp: func() cockpitResponse {
				warn := cockpitHealthy("alpha")
				warn.IndexedHeight, warn.LagBlocks = 900, 100
				down := cockpitHealthy("beta")
				down.Sync = &syncer.NetworkHealth{ConsecutiveFailures: cockpitSyncFailDown}
				return cockpitResponse{Service: up.Service, Networks: []CockpitNetwork{warn, down}}
			}(),
			want:     []string{"sync", "lag"},
			severity: CockpitDown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cockpitFindings(tt.resp)
			var ids []string
			for _, f := range got {
				ids = append(ids, f.ID)
				if f.Title == "" {
					t.Errorf("finding %q has no title: a wall display shows nothing else", f.ID)
				}
				if f.Detail == "" {
					t.Errorf("finding %q has no detail: the threshold has to be readable", f.ID)
				}
			}
			if strings.Join(ids, ",") != strings.Join(tt.want, ",") {
				t.Errorf("findings = %v, want %v", ids, tt.want)
			}
			if v := cockpitVerdict(got); v.State != tt.severity {
				t.Errorf("verdict = %q, want %q (headline %q)", v.State, tt.severity, v.Headline)
			}
		})
	}
}

// The headline is the one line a passer-by reads, so it has to agree with the
// list under it rather than hedge.
func TestCockpitVerdictHeadline(t *testing.T) {
	tests := []struct {
		name     string
		findings []CockpitFinding
		want     string
	}{
		{"nothing firing", nil, "all clear"},
		{"one warning", []CockpitFinding{{Severity: CockpitWarnSeverity}}, "one thing needs a look"},
		{"two warnings", []CockpitFinding{{Severity: CockpitWarnSeverity}, {Severity: CockpitWarnSeverity}},
			"2 things need a look"},
		{"one outage", []CockpitFinding{{Severity: CockpitDown}}, "one thing is down"},
		{"an outage beside a warning counts only the outages",
			[]CockpitFinding{{Severity: CockpitDown}, {Severity: CockpitWarnSeverity}}, "one thing is down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cockpitVerdict(tt.findings).Headline; got != tt.want {
				t.Errorf("headline = %q, want %q", got, tt.want)
			}
		})
	}
}

// The endpoint answers without an indexer, without a request log and without a
// sync loop, which is every deployment that is not production and is also what
// the first minute of production looks like.
func TestCockpitAnswersOnABareInstance(t *testing.T) {
	api, _ := newTestAPI(t)
	api.SetBuildInfo(BuildInfo{GitHash: "abc1234", Version: "v1.2.3", StartedAt: time.Now().Add(-time.Hour)})

	var got struct {
		Verdict  CockpitVerdict   `json:"verdict"`
		Findings []CockpitFinding `json:"findings"`
		Service  CockpitService   `json:"service"`
		Networks []CockpitNetwork `json:"networks"`
		Chain    CockpitChain     `json:"chain"`
		Requests *CockpitRequests `json:"requests"`
	}
	getJSON(t, api.HandleCockpit, "/api/cockpit", &got)

	if got.Service.GitHash != "abc1234" {
		t.Errorf("git hash = %q, want the one that was set", got.Service.GitHash)
	}
	if got.Service.UptimeSeconds < 3500 {
		t.Errorf("uptime = %ds, want about an hour", got.Service.UptimeSeconds)
	}
	if len(got.Networks) != 2 {
		t.Fatalf("got %d networks, want both configured ones", len(got.Networks))
	}
	if got.Chain.Network != "alpha" {
		t.Errorf("chain = %q, want the first configured network", got.Chain.Network)
	}
	if got.Requests != nil {
		t.Error("a request section without a request log reads as 'nobody came'; it should be absent")
	}
	// No indexer and nothing stored, an hour in: that is two findings per
	// network and not a crash, and the verdict has to say so.
	if got.Verdict.State == CockpitOK {
		t.Errorf("verdict = ok with no indexer and nothing indexed, findings %+v", got.Findings)
	}
}

// Sync health reaches the roll-up, because "the chain is fine and our passes
// against it are failing" is the case no chain-side figure can show.
func TestCockpitReportsSyncFailures(t *testing.T) {
	api, _ := newTestAPI(t)
	reg := syncer.NewRegistry()
	for i := 0; i < cockpitSyncFailDown; i++ {
		reg.Record("alpha", errors.New(`indexer returned 422`))
	}
	reg.Record("beta", nil)
	api.SetSyncHealth(reg)

	var got struct {
		Findings []CockpitFinding `json:"findings"`
		Networks []CockpitNetwork `json:"networks"`
	}
	getJSON(t, api.HandleCockpit, "/api/cockpit", &got)

	var found *CockpitFinding
	for i := range got.Findings {
		if got.Findings[i].ID == "sync" && got.Findings[i].Network == "alpha" {
			found = &got.Findings[i]
		}
		if got.Findings[i].ID == "sync" && got.Findings[i].Network == "beta" {
			t.Error("beta's passes are succeeding and it is reported as failing")
		}
	}
	if found == nil {
		t.Fatalf("no sync finding for alpha: %+v", got.Findings)
	}
	if found.Severity != CockpitDown {
		t.Errorf("severity = %q after %d failures in a row, want %q",
			found.Severity, cockpitSyncFailDown, CockpitDown)
	}
	if !strings.Contains(found.Detail, "422") {
		t.Errorf("detail %q drops the error, so nobody can act on it", found.Detail)
	}
}

// /api/version moved out of main.go so the cockpit could share its facts. It
// has to keep answering the same shape, and it has to be in the route table
// now that it is registered with the others.
func TestVersionEndpointKeepsItsShape(t *testing.T) {
	api, _ := newTestAPI(t)
	api.SetBuildInfo(BuildInfo{GitHash: "deadbee", BuildTime: "2026-10-21T00:00:00Z", Version: "v9"})

	var got map[string]any
	getJSON(t, api.HandleVersion, "/api/version", &got)
	for _, key := range []string{"git_hash", "build_time", "version", "go_version", "uptime_seconds", "shots"} {
		if _, ok := got[key]; !ok {
			t.Errorf("/api/version dropped %q, which the rail's about block reads", key)
		}
	}
	if got["git_hash"] != "deadbee" {
		t.Errorf("git_hash = %v, want the one that was set", got["git_hash"])
	}

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	api.HandleEndpoints(rec, httptest.NewRequest(http.MethodGet, "/api/endpoints", nil))
	for _, path := range []string{"/api/version", "/api/cockpit"} {
		if !strings.Contains(rec.Body.String(), `"`+path+`"`) {
			t.Errorf("%s is missing from /api/endpoints, which claims to be the whole surface", path)
		}
	}
}

// A build with nothing stamped in says so rather than shipping empty strings
// that read as a field the page forgot to fill.
func TestCockpitServiceWithoutLdflags(t *testing.T) {
	api, _ := newTestAPI(t)
	s := api.service()
	if s.GitHash != "unknown" || s.Version != "unknown" || s.BuildTime != "unknown" {
		t.Errorf("an unstamped build reports %+v, want every field saying unknown", s)
	}
	if s.GoVersion == "" {
		t.Error("go version is always knowable at runtime and should never be empty")
	}
}
