package store

import (
	"testing"

	"github.com/gnoverse/gnoscope/pkg/config"
)

// The history a realm has is its submissions, and a resubmission is a routine
// event rather than an anomaly (see the schema comment on package_submissions).
// Reading it out of `packages` returns one row per path and calls that the
// whole story, which is how the gnohub commit list would have shown a single
// "commit" for a realm deployed four times.
func TestPackageDeploysReturnsEverySubmissionNewestFirst(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: "live"}, {ID: "other"}})

	const path = "gno.land/r/demo/retried"
	rows := []struct {
		hash    string
		height  int
		when    string
		success bool
		files   int
	}{
		{"try1", 100, "2026-08-01T00:00:00Z", false, 1},
		{"try2", 101, "2026-08-01T00:00:10Z", false, 1},
		{"try3", 102, "2026-08-01T00:00:20Z", true, 2},
	}
	for _, r := range rows {
		if err := db.InsertPackageSubmission("live", r.hash, 0, path, "retried",
			"g1deployer", r.height, r.when, true, r.files, r.success); err != nil {
			t.Fatalf("InsertPackageSubmission(%s): %v", r.hash, err)
		}
		if err := db.UpsertPackage("live", path, "retried", "g1deployer", r.hash,
			r.height, r.when, true, r.files); err != nil {
			t.Fatalf("UpsertPackage(%s): %v", r.hash, err)
		}
	}
	// Same path on another chain: network scoping is the invariant every query
	// here has to keep, and a path is the one key two chains genuinely share.
	if err := db.InsertPackageSubmission("other", "elsewhere", 0, path, "retried",
		"g1someone", 5, "2026-08-02T00:00:00Z", true, 1, true); err != nil {
		t.Fatalf("InsertPackageSubmission(other): %v", err)
	}

	got, err := db.PackageDeploys("live", path, 0)
	if err != nil {
		t.Fatalf("PackageDeploys: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d deploys, want 3: %+v", len(got), got)
	}
	wantOrder := []string{"try3", "try2", "try1"}
	for i, want := range wantOrder {
		if got[i].TxHash != want {
			t.Errorf("deploy %d is %q, want %q (newest first)", i, got[i].TxHash, want)
		}
	}
	if !got[0].Success || got[1].Success {
		t.Errorf("success did not survive the round trip: %+v", got[:2])
	}
	if got[0].NumFiles != 2 {
		t.Errorf("num_files is %d, want 2", got[0].NumFiles)
	}
	if got[0].BlockTime != "2026-08-01T00:00:20Z" {
		t.Errorf("block_time is %q, want the row's", got[0].BlockTime)
	}

	n, err := db.CountPackageDeploys("live", path)
	if err != nil {
		t.Fatalf("CountPackageDeploys: %v", err)
	}
	if n != 3 {
		t.Errorf("CountPackageDeploys = %d, want 3", n)
	}

	// Every-network mode spans the configured set and no further.
	all, err := db.PackageDeploys("", path, 0)
	if err != nil {
		t.Fatalf("PackageDeploys(all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d deploys across networks, want 4: %+v", len(all), all)
	}

	// And the other chain's row is reachable on its own, tagged as its own.
	only, err := db.PackageDeploys("other", path, 0)
	if err != nil {
		t.Fatalf("PackageDeploys(other): %v", err)
	}
	if len(only) != 1 || only[0].Network != "other" {
		t.Fatalf("scoping to `other` gave %+v", only)
	}
}

func TestPackageDeploysLimits(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: "live"}})

	const path = "gno.land/r/demo/busy"
	for i := 0; i < 5; i++ {
		if err := db.InsertPackageSubmission("live", "h"+string(rune('a'+i)), 0, path,
			"busy", "g1deployer", 100+i, "2026-08-01T00:00:00Z", true, 1, true); err != nil {
			t.Fatalf("InsertPackageSubmission: %v", err)
		}
	}
	got, err := db.PackageDeploys("live", path, 2)
	if err != nil {
		t.Fatalf("PackageDeploys: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit 2 returned %d rows", len(got))
	}
	if got[0].BlockHeight != 104 {
		t.Errorf("a limited page starts at height %d, want the newest (104)", got[0].BlockHeight)
	}
}

// A path nobody ever deployed is an empty list, not a nil one: the handler
// serializes this straight to JSON and `null` is a different shape from `[]` to
// every consumer that iterates it.
func TestPackageDeploysUnknownPathIsEmptyNotNil(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: "live"}})

	got, err := db.PackageDeploys("live", "gno.land/r/demo/nothing", 0)
	if err != nil {
		t.Fatalf("PackageDeploys: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want an empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("got %d rows for a path with no submissions", len(got))
	}
}
