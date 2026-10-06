package store

import (
	"testing"

	"github.com/gnoverse/gnoscope/pkg/indexer"
)

func subsrcRows(t *testing.T, db *DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.SQL().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// A body is stored once however many submissions, paths or chains carry it,
// and every submission still lists its own files.
func TestRecordSubmissionDedupsBodies(t *testing.T) {
	db := NewTestDB(t)
	shared := indexer.MemFile{Name: "lib.gno", Body: "package lib\n"}
	rec := func(network, tx string, height int, files ...indexer.MemFile) {
		t.Helper()
		if err := db.RecordSubmission(network, tx, 0, "gno.land/p/ns/lib", "lib", "g1x", height, "", false, "", true, files); err != nil {
			t.Fatalf("RecordSubmission(%s): %v", tx, err)
		}
	}
	rec("alpha", "tx1", 10, shared, indexer.MemFile{Name: "a.gno", Body: "package lib // a\n"})
	rec("alpha", "tx2", 11, shared, indexer.MemFile{Name: "a.gno", Body: "package lib // a\n"})
	rec("beta", "tx3", 5, shared)
	// The same message seen twice (the sync walk overlaps itself) adds nothing.
	rec("alpha", "tx1", 10, shared, indexer.MemFile{Name: "a.gno", Body: "package lib // a\n"})

	if n := subsrcRows(t, db, `SELECT COUNT(*) FROM blobs`); n != 2 {
		t.Errorf("blobs = %d, want 2 distinct bodies", n)
	}
	if n := subsrcRows(t, db, `SELECT COUNT(*) FROM submission_files`); n != 5 {
		t.Errorf("submission_files = %d, want 2 + 2 + 1", n)
	}
	if n := subsrcRows(t, db, `SELECT COUNT(*) FROM package_submissions`); n != 3 {
		t.Errorf("package_submissions = %d, want 3", n)
	}
	files, err := db.SubmissionFiles("beta", "tx3", 0)
	if err != nil || len(files) != 1 || files[0] != shared {
		t.Errorf("beta tx3 files = %+v (%v)", files, err)
	}
	st, err := db.SubmissionStorageStats("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if st.Submissions != 2 || st.WithFiles != 2 || st.FileRows != 4 || st.Blobs != 2 ||
		st.LogicalBytes != 2*int64(len(shared.Body)+len("package lib // a\n")) {
		t.Errorf("stats = %+v", st)
	}
}

// Two submissions at one path in one block: a success is preferred, tx=
// picks either, and a height with nothing stored is ErrNoSubmissionSource.
func TestSubmissionSourceChoosesWithinABlock(t *testing.T) {
	db := NewTestDB(t)
	const path = "gno.land/r/ns/app"
	for _, s := range []struct {
		tx   string
		ok   bool
		body string
	}{{"tx-b", false, "package app // failed\n"}, {"tx-a", true, "package app // ok\n"}} {
		if err := db.RecordSubmission("alpha", s.tx, 0, path, "app", "g1x", 30, "", true, "", s.ok,
			[]indexer.MemFile{{Name: "app.gno", Body: s.body}}); err != nil {
			t.Fatal(err)
		}
	}
	src, err := db.SubmissionSource("alpha", path, 30, "", true)
	if err != nil || src.Stamp.TxHash != "tx-a" || src.Failed {
		t.Fatalf("default pick = %+v (%v), want the successful tx-a", src, err)
	}
	src, err = db.SubmissionSource("alpha", path, 30, "tx-b", true)
	if err != nil || src.Stamp.TxHash != "tx-b" || !src.Failed || *src.Files[0].Body != "package app // failed\n" {
		t.Fatalf("tx=tx-b = %+v (%v)", src, err)
	}
	if src.Files[0].Size != len("package app // failed\n") || src.Files[0].Lines != 1 {
		t.Errorf("size/lines = %d/%d", src.Files[0].Size, src.Files[0].Lines)
	}
	if _, err := db.SubmissionSource("alpha", path, 31, "", true); err != ErrNoSubmissionSource {
		t.Errorf("height 31: %v, want ErrNoSubmissionSource", err)
	}
	if _, err := db.SubmissionSource("beta", path, 30, "", true); err != ErrNoSubmissionSource {
		t.Errorf("beta: %v, want ErrNoSubmissionSource (network-scoped)", err)
	}
}

// The backfill's write refuses a message this database never recorded, and
// its read lists only submissions without files, oldest first.
func TestAddSubmissionFilesAndMissing(t *testing.T) {
	db := NewTestDB(t)
	for i, tx := range []string{"tx1", "tx2", "tx3"} {
		if err := db.InsertPackageSubmission("alpha", tx, 0, "gno.land/r/ns/app", "app", "g1x", 10+i, "", true, 1, "", true); err != nil {
			t.Fatal(err)
		}
	}
	missing, err := db.SubmissionsMissingFiles("alpha", -1, 10)
	if err != nil || len(missing) != 3 || missing[0].TxHash != "tx1" {
		t.Fatalf("missing = %+v (%v)", missing, err)
	}
	f := []indexer.MemFile{{Name: "app.gno", Body: "package app\n"}}
	if ok, err := db.AddSubmissionFiles("alpha", "tx2", 0, "gno.land/r/ns/app", f); !ok || err != nil {
		t.Fatalf("AddSubmissionFiles(tx2) = %v %v", ok, err)
	}
	for _, c := range []struct{ net, tx, path string }{
		{"alpha", "nope", "gno.land/r/ns/app"},
		{"beta", "tx1", "gno.land/r/ns/app"},
		{"alpha", "tx1", "gno.land/r/ns/other"},
	} {
		if ok, err := db.AddSubmissionFiles(c.net, c.tx, 0, c.path, f); ok || err != nil {
			t.Errorf("AddSubmissionFiles(%v) = %v %v, want refused", c, ok, err)
		}
	}
	missing, _ = db.SubmissionsMissingFiles("alpha", -1, 10)
	if len(missing) != 2 || missing[0].TxHash != "tx1" || missing[1].TxHash != "tx3" {
		t.Errorf("missing after = %+v", missing)
	}
	missing, _ = db.SubmissionsMissingFiles("alpha", 10, 10)
	if len(missing) != 1 || missing[0].TxHash != "tx3" {
		t.Errorf("missing above 10 = %+v", missing)
	}
}
