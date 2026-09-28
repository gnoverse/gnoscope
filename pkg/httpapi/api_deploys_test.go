package httpapi

import (
	"testing"

	"github.com/moul/mygnoscan/pkg/store"
)

type deploysResponse struct {
	Path      string                `json:"path"`
	Network   string                `json:"network"`
	Deploys   []store.PackageDeploy `json:"deploys"`
	Total     int                   `json:"total"`
	Truncated bool                  `json:"truncated"`
}

func seedDeploys(t *testing.T, db *store.DB) {
	t.Helper()
	const path = "gno.land/r/ns/one"
	rows := []struct {
		hash   string
		height int
		ok     bool
	}{{"d1", 100, false}, {"d2", 101, false}, {"d3", 102, true}}
	for _, r := range rows {
		if err := db.InsertPackageSubmission("alpha", r.hash, 0, path, "one", "g1ns",
			r.height, "2026-08-01T00:00:00Z", true, 1, r.ok); err != nil {
			t.Fatalf("InsertPackageSubmission(%s): %v", r.hash, err)
		}
	}
	if err := db.InsertPackageSubmission("beta", "b1", 0, path, "one", "g1other",
		7, "2026-08-02T00:00:00Z", true, 1, true); err != nil {
		t.Fatalf("InsertPackageSubmission(beta): %v", err)
	}
}

// The route has to beat /api/realm/{path...}, which would otherwise swallow it
// and answer with a package detail for a path named "deploys/r/ns/one" — a 404
// that looks like a missing realm rather than a routing mistake.
func TestHandleRealmDeploys(t *testing.T) {
	api, db := newTestAPI(t)
	seedDeploys(t, db)

	var got deploysResponse
	muxGET(t, api, "/api/realm/deploys/r/ns/one?network=alpha", &got)

	if got.Path != "gno.land/r/ns/one" {
		t.Errorf("path = %q, want gno.land/r/ns/one", got.Path)
	}
	if got.Total != 3 || len(got.Deploys) != 3 {
		t.Fatalf("total %d / %d rows, want 3 and 3: %+v", got.Total, len(got.Deploys), got.Deploys)
	}
	if got.Deploys[0].TxHash != "d3" {
		t.Errorf("first row is %q, want d3 (newest first)", got.Deploys[0].TxHash)
	}
	if got.Truncated {
		t.Error("truncated on a complete page")
	}
}

func TestHandleRealmDeploysIsPerNetwork(t *testing.T) {
	api, db := newTestAPI(t)
	seedDeploys(t, db)

	var beta deploysResponse
	muxGET(t, api, "/api/realm/deploys/r/ns/one?network=beta", &beta)
	if beta.Total != 1 || beta.Deploys[0].TxHash != "b1" {
		t.Fatalf("beta sees %+v, want only its own submission", beta.Deploys)
	}

	// No network parameter means every configured chain, and each row says
	// which one it came from.
	var all deploysResponse
	muxGET(t, api, "/api/realm/deploys/r/ns/one", &all)
	if all.Total != 4 {
		t.Fatalf("all-networks total = %d, want 4", all.Total)
	}
	seen := map[string]bool{}
	for _, d := range all.Deploys {
		seen[d.Network] = true
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Errorf("rows are tagged %v, want both chains named", seen)
	}
}

func TestHandleRealmDeploysTruncates(t *testing.T) {
	api, db := newTestAPI(t)
	seedDeploys(t, db)

	var got deploysResponse
	muxGET(t, api, "/api/realm/deploys/r/ns/one?network=alpha&limit=2", &got)
	if len(got.Deploys) != 2 || got.Total != 3 || !got.Truncated {
		t.Fatalf("limit=2 gave %d rows, total %d, truncated %v", len(got.Deploys), got.Total, got.Truncated)
	}
}

// A path with no submissions answers 200 with an empty list, not 404: the
// question "what deployed this" is answerable for a path that does not exist,
// and the answer is "nothing".
func TestHandleRealmDeploysUnknownPath(t *testing.T) {
	api, db := newTestAPI(t)
	seedDeploys(t, db)

	var got deploysResponse
	muxGET(t, api, "/api/realm/deploys/r/ns/nothing?network=alpha", &got)
	if got.Total != 0 || len(got.Deploys) != 0 {
		t.Fatalf("got %+v, want an empty history", got)
	}
}
