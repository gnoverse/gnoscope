package analyzer

import (
	"sort"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// storedFileNames lists one package's files as package_files holds them.
func storedFileNames(t *testing.T, db *store.DB, network, path string) []string {
	t.Helper()
	files, err := db.StoredPackageFiles(network, path)
	if err != nil {
		t.Fatalf("StoredPackageFiles: %v", err)
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

// codeHits is how many files on one network the code index matches for q.
func codeHits(t *testing.T, db *store.DB, network, q string) int {
	t.Helper()
	hits, err := db.SearchCode(store.CodeSearchOpts{Network: network, Query: q})
	if err != nil {
		t.Fatalf("SearchCode(%q): %v", q, err)
	}
	return len(hits)
}

// A failed MsgAddPackage changed nothing on chain, so it must change nothing in
// the current-state tables either. It is still a submission, and the deploy
// history keeps it.
func TestProcessPackageIgnoresFailedSubmission(t *testing.T) {
	db := store.NewTestDB(t)
	a := NewAnalyzer(db)
	const net, path = "alpha", "gno.land/r/ns/app"

	good := &indexer.MemPackage{Name: "app", Path: path, Files: []indexer.MemFile{
		{Name: "app.gno", Body: "package app\n\nfunc Good() {} // goodmarker\n"},
	}}
	if err := a.ProcessPackage(net, good, "g1good", "tx-good", 10, 0, "2026-09-01T00:00:00Z", "", true); err != nil {
		t.Fatalf("ProcessPackage(good): %v", err)
	}

	bad := &indexer.MemPackage{Name: "app", Path: path, Files: []indexer.MemFile{
		{Name: "app.gno", Body: "package app\n\nimport \"gno.land/p/ns/evil\"\n\nfunc Bad() {} // badmarker\n"},
		{Name: "extra.gno", Body: "package app // badmarker\n"},
	}}
	if err := a.ProcessPackage(net, bad, "g1bad", "tx-bad", 11, 0, "2026-09-02T00:00:00Z", "", false); err != nil {
		t.Fatalf("ProcessPackage(bad): %v", err)
	}

	info, err := db.GetPackageDetail(net, path)
	if err != nil {
		t.Fatalf("GetPackageDetail: %v", err)
	}
	if info.TxHash != "tx-good" || info.BlockHeight != 10 || info.Creator != "g1good" {
		t.Errorf("package row moved to the failed submission: tx %q height %d creator %q",
			info.TxHash, info.BlockHeight, info.Creator)
	}
	if got := storedFileNames(t, db, net, path); len(got) != 1 || got[0] != "app.gno" {
		t.Errorf("files = %v, want [app.gno]", got)
	}
	if len(info.Files) == 1 && info.Files[0].Body != good.Files[0].Body {
		t.Errorf("app.gno body is the failed submission's: %q", info.Files[0].Body)
	}
	if n := codeHits(t, db, net, "badmarker"); n != 0 {
		t.Errorf("code index matches the failed submission's source in %d files", n)
	}
	if n := codeHits(t, db, net, "goodmarker"); n != 1 {
		t.Errorf("code index lost the live source: %d hits for goodmarker, want 1", n)
	}
	if len(info.Imports) != 0 {
		t.Errorf("imports = %v, want none: the failed submission's edges were stored", info.Imports)
	}

	// The submission itself is history and is kept.
	deploys, err := db.PackageDeploys(net, path, 10)
	if err != nil {
		t.Fatalf("PackageDeploys: %v", err)
	}
	if len(deploys) != 2 || deploys[0].TxHash != "tx-bad" || deploys[0].Success {
		t.Errorf("deploy history = %+v, want the failed submission first and marked failed", deploys)
	}
}

// A failed submission at a path that never deployed successfully leaves no
// package behind: nothing exists there on chain.
func TestProcessPackageFailedFirstSubmissionCreatesNoPackage(t *testing.T) {
	db := store.NewTestDB(t)
	a := NewAnalyzer(db)
	const net, path = "alpha", "gno.land/r/ns/never"

	pkg := &indexer.MemPackage{Name: "never", Path: path, Files: []indexer.MemFile{
		{Name: "never.gno", Body: "package never\n"},
	}}
	if err := a.ProcessPackage(net, pkg, "g1x", "tx-1", 5, 0, "", "", false); err != nil {
		t.Fatalf("ProcessPackage: %v", err)
	}
	if _, err := db.GetPackageDetail(net, path); err == nil {
		t.Error("a package row exists for a path whose only submission failed")
	}
	if got := storedFileNames(t, db, net, path); len(got) != 0 {
		t.Errorf("files = %v, want none", got)
	}
}

// A successful redeploy replaces the package's source, so a file the new
// submission does not carry is gone from chain and must be gone here: from
// package_files and from the code index. Only on that network.
func TestProcessPackageRedeployDropsRemovedFiles(t *testing.T) {
	db := store.NewTestDB(t)
	a := NewAnalyzer(db)
	const path = "gno.land/r/ns/app"

	v1 := &indexer.MemPackage{Name: "app", Path: path, Files: []indexer.MemFile{
		{Name: "app.gno", Body: "package app\n"},
		{Name: "old.gno", Body: "package app // oldmarker\n"},
	}}
	for _, net := range []string{"alpha", "beta"} {
		if err := a.ProcessPackage(net, v1, "g1x", "tx-v1", 10, 0, "", "", true); err != nil {
			t.Fatalf("ProcessPackage(%s, v1): %v", net, err)
		}
	}

	v2 := &indexer.MemPackage{Name: "app", Path: path, Files: []indexer.MemFile{
		{Name: "app.gno", Body: "package app // v2\n"},
		{Name: "new.gno", Body: "package app // newmarker\n"},
	}}
	if err := a.ProcessPackage("alpha", v2, "g1x", "tx-v2", 20, 0, "", "", true); err != nil {
		t.Fatalf("ProcessPackage(alpha, v2): %v", err)
	}

	if got := storedFileNames(t, db, "alpha", path); len(got) != 2 || got[0] != "app.gno" || got[1] != "new.gno" {
		t.Errorf("alpha files = %v, want [app.gno new.gno]", got)
	}
	if n := codeHits(t, db, "alpha", "oldmarker"); n != 0 {
		t.Errorf("alpha code index still matches a removed file (%d hits)", n)
	}
	if n := codeHits(t, db, "alpha", "newmarker"); n != 1 {
		t.Errorf("alpha code index: %d hits for newmarker, want 1", n)
	}

	// beta never saw v2.
	if got := storedFileNames(t, db, "beta", path); len(got) != 2 || got[0] != "app.gno" || got[1] != "old.gno" {
		t.Errorf("beta files = %v, want [app.gno old.gno] untouched", got)
	}
	if n := codeHits(t, db, "beta", "oldmarker"); n != 1 {
		t.Errorf("beta code index: %d hits for oldmarker, want 1", n)
	}
}
