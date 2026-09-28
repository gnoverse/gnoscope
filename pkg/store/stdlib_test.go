package store

import (
	"testing"
)

func TestIsStdlibPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Real stdlib paths, from `vm/qpaths` on mainnet 2026-09-28.
		{"strings", true},
		{"bufio", true},
		{"chain/banker", true},
		{"crypto/sha256", true},
		{"chain/runtime/unsafe", true},
		// On-chain paths always carry the chain's domain.
		{"gno.land/p/nt/avl/v0", false},
		{"gno.land/r/gnoland/blog", false},
		{"gno.land/r/g1abc/thing", false},
	}
	for _, tt := range tests {
		if got := IsStdlibPath(tt.path); got != tt.want {
			t.Errorf("IsStdlibPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestStdlibRoundTrip(t *testing.T) {
	d := NewTestDB(t)
	if err := d.UpsertStdlibFile("alpha", "strings", "compare.gno", "package strings\nfunc Compare() {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertStdlibFile("alpha", "strings", "builder.gno", "package strings\ntype Builder struct{}\n"); err != nil {
		t.Fatal(err)
	}

	pkgs, err := d.StdlibPackages("alpha")
	if err != nil || len(pkgs) != 1 || pkgs[0] != "strings" {
		t.Fatalf("packages = %v, err = %v", pkgs, err)
	}
	files, err := d.StdlibPackage("alpha", "strings")
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %v, err = %v", files, err)
	}
	// Ordered by name, so a page does not reshuffle between reads.
	if files[0].Name != "builder.gno" {
		t.Errorf("files[0] = %q, want builder.gno", files[0].Name)
	}
	if n, _ := d.StdlibFileCount("alpha"); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	// Another network must not see it.
	if other, _ := d.StdlibPackages("beta"); len(other) != 0 {
		t.Errorf("beta sees alpha's stdlib: %v", other)
	}
}

// Stdlib is searchable: that is the entire reason for crawling it.
func TestStdlibIsSearchable(t *testing.T) {
	d := NewTestDB(t)
	if err := d.UpsertStdlibFile("alpha", "strings", "compare.gno",
		"package strings\nfunc Compare(a, b string) int { return 0 }\n"); err != nil {
		t.Fatal(err)
	}
	hits, err := d.SearchCode(CodeSearchOpts{Network: "alpha", Query: "Compare"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "strings" {
		t.Fatalf("hits = %+v, want one in `strings`", hits)
	}
	if hits[0].IsRealm {
		t.Error("a stdlib package was classified as a realm")
	}
}

// The safety argument for a separate table, asserted rather than assumed.
//
// Stdlib is the node's own source, not something anybody deployed. If it
// reached the on-chain aggregates it would inflate the source-bytes total and
// the package counts with code no transaction ever carried, and it would do it
// silently.
func TestStdlibNeverReachesOnChainAggregates(t *testing.T) {
	d := NewTestDB(t)

	if err := d.UpsertPackageFile("alpha", "gno.land/r/x/y", "a.gno", "package y\n"); err != nil {
		t.Fatal(err)
	}
	beforeKB := sourceKB(t, d, "alpha")
	beforeFiles := countRows(t, d, "SELECT count(*) FROM package_files")

	// A big stdlib package, so any leak would be obvious rather than marginal.
	big := "package strings\n" + string(make([]byte, 50_000))
	if err := d.UpsertStdlibFile("alpha", "strings", "strings.gno", big); err != nil {
		t.Fatal(err)
	}

	if got := sourceKB(t, d, "alpha"); got != beforeKB {
		t.Errorf("total source KB moved from %d to %d: stdlib reached the analytics total", beforeKB, got)
	}
	if got := countRows(t, d, "SELECT count(*) FROM package_files"); got != beforeFiles {
		t.Errorf("package_files grew from %d to %d: stdlib reached the on-chain source table", beforeFiles, got)
	}
	// And it must not appear as a package anybody deployed.
	if got := countRows(t, d, "SELECT count(*) FROM packages"); got != 0 {
		t.Errorf("packages has %d rows; stdlib was counted as a deployment", got)
	}
}

// The on-chain backfill must keep working once the index also holds stdlib.
// A bare count(code_index) would exceed count(package_files) and the
// completeness guard would conclude the on-chain half was done.
func TestOnChainBackfillIgnoresStdlibRows(t *testing.T) {
	d := NewTestDB(t)
	for i, p := range []string{"gno.land/r/a/one", "gno.land/r/a/two", "gno.land/r/a/three"} {
		if err := d.UpsertPackageFile("alpha", p, "f.gno", "package a\n"); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	// Enough stdlib rows to outnumber the on-chain corpus on their own.
	for _, p := range []string{"strings", "bytes", "bufio", "chain", "crypto/sha256"} {
		if err := d.UpsertStdlibFile("alpha", p, "f.gno", "package x\n"); err != nil {
			t.Fatal(err)
		}
	}
	// Now empty the on-chain half of the index, the state a deployment that
	// predates code search is in.
	if _, err := d.db.Exec(`DELETE FROM code_index WHERE package_path LIKE 'gno.land/%'`); err != nil {
		t.Fatal(err)
	}

	n, err := d.BackfillCodeIndex()
	if err != nil {
		t.Fatalf("BackfillCodeIndex: %v", err)
	}
	if n != 3 {
		t.Errorf("backfilled %d files, want 3: stdlib rows were counted as on-chain coverage", n)
	}
	hits, _ := d.SearchCode(CodeSearchOpts{Network: "alpha", Query: "package"})
	if len(hits) != 8 {
		t.Errorf("%d hits after backfill, want 8 (3 on-chain + 5 stdlib)", len(hits))
	}
}

func TestBackfillStdlibIndex(t *testing.T) {
	d := NewTestDB(t)
	if err := d.UpsertStdlibFile("alpha", "strings", "a.gno", "package strings\nfunc Marker() {}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`DELETE FROM code_index`); err != nil {
		t.Fatal(err)
	}
	n, err := d.BackfillStdlibIndex("alpha")
	if err != nil || n != 1 {
		t.Fatalf("backfill = %d, %v; want 1, nil", n, err)
	}
	hits, _ := d.SearchCode(CodeSearchOpts{Network: "alpha", Query: "Marker"})
	if len(hits) != 1 {
		t.Errorf("%d hits after stdlib backfill, want 1", len(hits))
	}
	// Complete index is left alone.
	if n2, err := d.BackfillStdlibIndex("alpha"); err != nil || n2 != 0 {
		t.Errorf("second backfill = %d, %v; want 0, nil", n2, err)
	}
}

func sourceKB(t *testing.T, d *DB, network string) int {
	t.Helper()
	var kb int
	if err := d.db.QueryRow(
		`SELECT COALESCE(SUM(LENGTH(body)), 0) / 1024 FROM package_files WHERE network = ?`,
		network).Scan(&kb); err != nil {
		t.Fatal(err)
	}
	return kb
}

func countRows(t *testing.T, d *DB, q string) int {
	t.Helper()
	var n int
	if err := d.db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
