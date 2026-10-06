package analyzer

import (
	"reflect"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// A failed submission is kept, files and all, and is still not current.
func TestProcessPackageStoresAFailedSubmissionsFiles(t *testing.T) {
	db := store.NewTestDB(t)
	a := NewAnalyzer(db)
	const net, path = "alpha", "gno.land/r/ns/app"
	good := &indexer.MemPackage{Name: "app", Path: path, Files: []indexer.MemFile{{Name: "app.gno", Body: "package app\n"}}}
	bad := &indexer.MemPackage{Name: "app", Path: path, Files: []indexer.MemFile{{Name: "app.gno", Body: "package app // rejected\n"}}}
	if err := a.ProcessPackage(net, good, "g1", "tx-good", 10, 0, "", "", true); err != nil {
		t.Fatal(err)
	}
	if err := a.ProcessPackage(net, bad, "g1", "tx-bad", 11, 0, "", "", false); err != nil {
		t.Fatal(err)
	}
	files, err := db.SubmissionFiles(net, "tx-bad", 0)
	if err != nil || !reflect.DeepEqual(files, bad.Files) {
		t.Errorf("failed submission's files = %+v (%v), want what it carried", files, err)
	}
	cur, err := db.StoredPackageFiles(net, path)
	if err != nil || !reflect.DeepEqual(cur, good.Files) {
		t.Errorf("current files = %+v (%v), want the successful submission's", cur, err)
	}
}

// damage reproduces what ProcessPackage did to a failed submission before it
// stopped writing current state: the row took the failed stamp and the files
// were upserted over the live ones, the ones it did not carry left in place.
func damage(t *testing.T, db *store.DB, net string, pkg *indexer.MemPackage, tx string, height int) {
	t.Helper()
	if err := db.UpsertPackage(net, pkg.Path, pkg.Name, "g1bad", tx, height, "", true, len(pkg.Files)); err != nil {
		t.Fatal(err)
	}
	for _, f := range pkg.Files {
		if err := db.UpsertPackageFile(net, pkg.Path, f.Name, f.Body); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetDependencies(net, pkg.Path, NewAnalyzer(db).ExtractImports(pkg.Path, pkg.Files)); err != nil {
		t.Fatal(err)
	}
}

func TestRepairCurrentSource(t *testing.T) {
	db := store.NewTestDB(t)
	a := NewAnalyzer(db)
	const net = "alpha"
	mk := func(path string, files ...indexer.MemFile) *indexer.MemPackage {
		return &indexer.MemPackage{Name: "app", Path: path, Files: files}
	}
	f := func(name, body string) indexer.MemFile { return indexer.MemFile{Name: name, Body: body} }
	process := func(pkg *indexer.MemPackage, tx string, h int, ok bool) {
		t.Helper()
		if err := a.ProcessPackage(net, pkg, "g1", tx, h, 0, "2026-09-01T00:00:00Z", "", ok); err != nil {
			t.Fatal(err)
		}
	}

	// broken: a good publish, then a failed one that overwrote it the old way.
	const broken = "gno.land/r/ns/broken"
	goodPkg := mk(broken, f("app.gno", "package app\n\nfunc Good() {} // goodmarker\n"))
	badPkg := mk(broken,
		f("app.gno", "package app\n\nimport \"gno.land/p/ns/evil\"\n\nfunc Bad() {} // badmarker\n"),
		f("extra.gno", "package app // badmarker\n"))
	process(goodPkg, "tx-good", 10, true)
	process(badPkg, "tx-bad", 11, false)
	damage(t, db, net, badPkg, "tx-bad", 11)

	// leftover: a redeploy dropped a file, which the old upsert left behind.
	const leftover = "gno.land/r/ns/leftover"
	process(mk(leftover, f("a.gno", "package app\n"), f("old.gno", "package app // oldmarker\n")), "tx-l1", 12, true)
	process(mk(leftover, f("a.gno", "package app // v2\n")), "tx-l2", 13, true)
	if err := db.UpsertPackageFile(net, leftover, "old.gno", "package app // oldmarker\n"); err != nil {
		t.Fatal(err)
	}

	// fine: correct, and must be left exactly as it is.
	const fine = "gno.land/r/ns/fine"
	process(mk(fine, f("a.gno", "package app // fine\n")), "tx-f", 14, true)

	// rejected: every submission failed, and the old code wrote a row anyway.
	const rejected = "gno.land/r/ns/rejected"
	rejPkg := mk(rejected, f("a.gno", "package app // rejectedmarker\n"))
	process(rejPkg, "tx-r", 15, false)
	damage(t, db, net, rejPkg, "tx-r", 15)

	// other network, same broken shape: not this pass's business.
	if err := a.ProcessPackage("beta", goodPkg, "g1", "tx-good", 10, 0, "", "", true); err != nil {
		t.Fatal(err)
	}
	damage(t, db, "beta", badPkg, "tx-bad", 11)

	if n := codeHits(t, db, net, "badmarker"); n == 0 {
		t.Fatal("the seeded damage is not visible; the test would pass vacuously")
	}

	res, err := a.RepairCurrentSource(net)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Repaired, []string{broken, leftover}) || res.StampWrong != 1 || res.FilesWrong != 2 {
		t.Errorf("repaired = %v stamp %d files %d, want [broken leftover], 1, 2", res.Repaired, res.StampWrong, res.FilesWrong)
	}
	if !reflect.DeepEqual(res.Removed, []string{rejected}) {
		t.Errorf("removed = %v, want [%s]", res.Removed, rejected)
	}
	if res.Checked != 3 {
		t.Errorf("checked = %d, want 3", res.Checked)
	}

	src, err := db.PackageSource(net, broken, true)
	if err != nil {
		t.Fatal(err)
	}
	if src.Stamp.Height != 10 || src.Stamp.TxHash != "tx-good" || len(src.Files) != 1 ||
		*src.Files[0].Body != goodPkg.Files[0].Body {
		t.Errorf("broken after repair = %+v", src)
	}
	if got := storedFileNames(t, db, net, leftover); !reflect.DeepEqual(got, []string{"a.gno"}) {
		t.Errorf("leftover files = %v, want [a.gno]", got)
	}
	for _, q := range []string{"badmarker", "oldmarker", "rejectedmarker"} {
		if n := codeHits(t, db, net, q); n != 0 {
			t.Errorf("code search still finds %q %d times", q, n)
		}
	}
	if n := codeHits(t, db, net, "goodmarker"); n != 1 {
		t.Errorf("goodmarker hits = %d, want 1", n)
	}
	var evil int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM dependencies WHERE network = ? AND import_path = 'gno.land/p/ns/evil'`, net).Scan(&evil); err != nil || evil != 0 {
		t.Errorf("evil import edges = %d (%v), want 0", evil, err)
	}
	if _, err := db.PackageSource(net, rejected, false); err != store.ErrNoSource {
		t.Errorf("rejected path: %v, want ErrNoSource", err)
	}
	if src, _ := db.PackageSource(net, fine, true); src == nil || src.Stamp.TxHash != "tx-f" {
		t.Errorf("fine path = %+v", src)
	}
	// beta untouched.
	if n := codeHits(t, db, "beta", "badmarker"); n == 0 {
		t.Error("the repair of alpha touched beta")
	}

	// A second pass finds nothing.
	res, err = a.RepairCurrentSource(net)
	if err != nil || len(res.Repaired) != 0 || len(res.Removed) != 0 {
		t.Errorf("second pass = %+v (%v), want nothing to do", res, err)
	}
}

// A path whose newest successful submission has no stored files cannot be
// judged, and is skipped rather than guessed at.
func TestRepairSkipsWithoutStoredFiles(t *testing.T) {
	db := store.NewTestDB(t)
	a := NewAnalyzer(db)
	pkg := &indexer.MemPackage{Name: "app", Path: "gno.land/r/ns/app", Files: []indexer.MemFile{{Name: "a.gno", Body: "package app\n"}}}
	if err := a.ProcessPackage("alpha", pkg, "g1", "tx1", 10, 0, "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`DELETE FROM submission_files`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPackageFile("alpha", pkg.Path, "junk.gno", "package app\n"); err != nil {
		t.Fatal(err)
	}
	res, err := a.RepairCurrentSource("alpha")
	if err != nil || res.Skipped != 1 || len(res.Repaired) != 0 {
		t.Errorf("res = %+v (%v), want one skipped and nothing repaired", res, err)
	}
}
