package syncer

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

func addPackageTx(hash string, height int, ok bool, path string, files ...indexer.MemFile) indexer.Transaction {
	return indexer.Transaction{
		Hash: hash, BlockHeight: height, Success: ok,
		Messages: []indexer.TxMessage{{Value: indexer.MessageValue{
			Typename: "MsgAddPackage", Creator: "g1creator",
			Package: &indexer.MemPackage{Name: path[strings.LastIndex(path, "/")+1:], Path: path, Files: files},
		}}},
	}
}

// subsrcFixture syncs n package submissions, then forgets their files, which is
// what a database synced before per-submission source existed looks like.
func subsrcFixture(t *testing.T, n int) (*Syncer, *indexer.Fake, *store.DB) {
	t.Helper()
	s, fake, db := newTestSyncer(t, "alpha")
	s.subsrcPause = 0
	for i := 0; i < n; i++ {
		fake.Add(addPackageTx(fmt.Sprintf("tx%02d", i), i, true, fmt.Sprintf("gno.land/r/ns/p%d", i%3),
			indexer.MemFile{Name: "a.gno", Body: fmt.Sprintf("package p // %d\n", i)}))
	}
	if err := s.syncPackages(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DELETE FROM submission_files`, `DELETE FROM blobs`} {
		if _, err := db.SQL().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return s, fake, db
}

func fileRows(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM submission_files WHERE network = 'alpha'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The backfill fills every submission's files, a bounded number per pass,
// resumes from its own cursor, never re-asks for a range it finished, and
// moves no derived sync cursor.
func TestBackfillSubmissionSourceIsResumableAndIdempotent(t *testing.T) {
	s, fake, db := subsrcFixture(t, 7)
	s.subsrcBatch, s.subsrcBatchesPerPass = 2, 2
	ctx := context.Background()

	pkgCursor, err := s.getLastBlockHeight(ctx, "package_submissions")
	if err != nil || pkgCursor == nil || *pkgCursor != 6 {
		t.Fatalf("package cursor = %v (%v)", pkgCursor, err)
	}
	var pkgRows int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM packages`).Scan(&pkgRows); err != nil {
		t.Fatal(err)
	}

	// Pass 1: two batches of two.
	if done := s.backfillSubmissionSource(ctx); done {
		t.Fatal("pass 1 reported done with work left")
	}
	if n := fileRows(t, db); n != 4 {
		t.Fatalf("after pass 1: %d file rows, want 4", n)
	}
	if v, _ := db.GetSyncState(SubsrcCursorKey("alpha")); v != "3" {
		t.Errorf("cursor after pass 1 = %q, want 3", v)
	}

	// The indexer has a bad minute: nothing moves.
	fake.Status = http.StatusTooManyRequests
	if done := s.backfillSubmissionSource(ctx); done {
		t.Fatal("a failed pass reported done")
	}
	if v, _ := db.GetSyncState(SubsrcCursorKey("alpha")); v != "3" {
		t.Errorf("cursor after a failed pass = %q, want 3 still", v)
	}
	fake.Status = 0
	// Let the breaker the failure opened close again.
	s.client = indexer.NewSyncClient(fake.URL)

	before := len(fake.AskedQueries())
	if done := s.backfillSubmissionSource(ctx); done {
		t.Fatal("pass 2 reported done with one batch of work done")
	}
	if done := s.backfillSubmissionSource(ctx); !done {
		t.Fatal("pass 3 did not report done")
	}
	if n := fileRows(t, db); n != 7 {
		t.Fatalf("after the backfill: %d file rows, want 7", n)
	}
	// Resumed above 3, never re-asking for 0..3.
	for _, q := range fake.AskedQueries()[before:] {
		if strings.Contains(q, "MsgAddPackage") && !strings.Contains(q, "gt: 3") && !strings.Contains(q, "gt: 4") && !strings.Contains(q, "gt: 5") {
			t.Errorf("a resumed pass asked for history below its cursor: %s", oneLine(q))
		}
	}

	// Idempotent: two submissions lose their files and the walk starts over.
	// The range it fetches (2..4) also holds tx03, whose files are stored,
	// and writing those again changes nothing.
	if _, err := db.SQL().Exec(`DELETE FROM submission_files WHERE tx_hash IN ('tx02', 'tx04')`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSyncState(SubsrcCursorKey("alpha"), "-1"); err != nil {
		t.Fatal(err)
	}
	s.backfillSubmissionSource(ctx)
	if n := fileRows(t, db); n != 7 {
		t.Errorf("one pass over a range already partly stored left %d file rows, want 7", n)
	}
	if done := s.backfillSubmissionSource(ctx); !done {
		t.Fatal("a pass with nothing missing did not report done")
	}

	files, err := db.SubmissionFiles("alpha", "tx05", 0)
	if err != nil || len(files) != 1 || files[0].Body != "package p // 5\n" {
		t.Errorf("tx05 files = %+v (%v)", files, err)
	}
	after, err := s.getLastBlockHeight(ctx, "package_submissions")
	if err != nil || after == nil || *after != *pkgCursor {
		t.Errorf("package cursor moved: %v -> %v", *pkgCursor, after)
	}
	var pkgRowsAfter int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM packages`).Scan(&pkgRowsAfter); err != nil || pkgRowsAfter != pkgRows {
		t.Errorf("packages rows %d -> %d", pkgRows, pkgRowsAfter)
	}
}

func oneLine(q string) string { return strings.Join(strings.Fields(q), " ") }

// A submission the indexer does not return is passed over, not asked for on
// every pass forever.
func TestBackfillSubmissionSourcePassesOverWhatTheIndexerLacks(t *testing.T) {
	s, fake, db := subsrcFixture(t, 3)
	if err := db.InsertPackageSubmission("alpha", "ghost", 0, "gno.land/r/ns/ghost", "ghost", "g1", 1, "", true, 1, "", true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s.backfillSubmissionSource(ctx)
	if done := s.backfillSubmissionSource(ctx); !done {
		t.Fatal("did not finish with one submission the indexer lacks")
	}
	if ok, _ := db.HasSubmissionFiles("alpha", "ghost", 0); ok {
		t.Error("ghost has files")
	}
	// And the next pass does not ask the indexer for it again.
	before := len(fake.AskedQueries())
	if done := s.backfillSubmissionSource(ctx); !done {
		t.Fatal("a later pass is not done")
	}
	if asked := fake.AskedQueries()[before:]; len(asked) != 0 {
		t.Errorf("a later pass asked again: %s", oneLine(asked[0]))
	}
}

// End to end: a database the old code damaged, the backfill, then the repair,
// once, with the paths handed to the cache hook.
func TestSyncAllRepairsAfterTheBackfill(t *testing.T) {
	s, fake, db := newTestSyncer(t, "alpha")
	s.subsrcPause = 0
	const path = "gno.land/r/ns/app"
	good := indexer.MemFile{Name: "a.gno", Body: "package app // good\n"}
	bad := indexer.MemFile{Name: "a.gno", Body: "package app // bad\n"}
	fake.Add(addPackageTx("tx-good", 10, true, path, good), addPackageTx("tx-bad", 11, false, path, bad))
	ctx := context.Background()
	if err := s.syncPackages(ctx); err != nil {
		t.Fatal(err)
	}
	// What ProcessPackage used to do with the failed one, and no files kept.
	if err := db.UpsertPackage("alpha", path, "app", "g1", "tx-bad", 11, "", true, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPackageFile("alpha", path, "a.gno", bad.Body); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DELETE FROM submission_files`, `DELETE FROM blobs`} {
		if _, err := db.SQL().Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	var hooked []string
	s.SetOnSourceRepaired(func(network string, paths []string) { hooked = append(hooked, paths...) })
	if err := s.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}

	src, err := db.PackageSource("alpha", path, true)
	if err != nil || src.Stamp.TxHash != "tx-good" || *src.Files[0].Body != good.Body {
		t.Fatalf("after repair = %+v (%v)", src, err)
	}
	if !reflect.DeepEqual(hooked, []string{path}) {
		t.Errorf("hook got %v", hooked)
	}
	marker, _ := db.GetSyncState(SubsrcRepairKey("alpha"))
	if !strings.HasPrefix(marker, "v"+subsrcRepairVersion+" ") || !strings.Contains(marker, "repaired=1") {
		t.Errorf("marker = %q", marker)
	}

	// Once: damage it again and the repair does not run a second time.
	if err := db.UpsertPackageFile("alpha", path, "a.gno", bad.Body); err != nil {
		t.Fatal(err)
	}
	s.repairSubmissionSource()
	if len(hooked) != 1 {
		t.Errorf("the repair ran twice")
	}
}
