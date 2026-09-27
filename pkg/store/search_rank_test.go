package store

import "testing"

// Search ranking, pinned to the mainnet cases that motivated it. Each fixture
// below is a real shape measured on 2026-09-28, not an invented one.

// seedPkg writes a package and gives it a call history.
func seedPkg(t *testing.T, db *DB, network, path, name string, realm bool, height, calls int) {
	t.Helper()
	if err := db.UpsertPackage(network, path, name, "g1creator", "TX"+path, height, "", realm, 1); err != nil {
		t.Fatalf("upsert %s: %v", path, err)
	}
	for i := 0; i < calls; i++ {
		if err := db.InsertCall(network, path+"-tx", height, i, "", "g1caller", path, "F", true); err != nil {
			t.Fatalf("call %s: %v", path, err)
		}
	}
}

func searchPaths(t *testing.T, db *DB, network, q string) []string {
	t.Helper()
	rows, err := db.Search(network, q)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Path
	}
	return out
}

// The complaint this ranking exists for. On mainnet, "moul" answered with ten
// gno.land/r/moul/x/daily/* one-off demos, because they are the most recent
// deploys under that namespace, and gno.land/r/moul/home was not among them.
func TestSearchRanksTheEntryPointAboveTheDemos(t *testing.T) {
	db := NewTestDB(t)
	seedPkg(t, db, "alpha", "gno.land/r/moul/home", "home", true, 10, 8)
	seedPkg(t, db, "alpha", "gno.land/r/moul/blog", "blog", true, 11, 3)
	for i, demo := range []string{"kudos", "hangman", "heapdemo"} {
		seedPkg(t, db, "alpha", "gno.land/r/moul/x/daily/"+demo+"/v0", demo, true, 900+i, 0)
	}

	got := searchPaths(t, db, "alpha", "moul")
	if len(got) == 0 || got[0] != "gno.land/r/moul/home" {
		t.Fatalf("got %v, want r/moul/home first: a namespace query means the entry point", got)
	}
	if got[1] != "gno.land/r/moul/blog" {
		t.Errorf("second = %q, want r/moul/blog", got[1])
	}
}

// The other half of the same complaint: "blog" put gno.land/r/gnoland/blog,
// with 134 calls, sixth, below five demos with none.
func TestSearchRanksTheUsedRealmAboveTheIdleOnes(t *testing.T) {
	db := NewTestDB(t)
	seedPkg(t, db, "alpha", "gno.land/r/gnoland/blog", "blog", true, 5, 134)
	seedPkg(t, db, "alpha", "gno.land/r/moul/blog", "blog", true, 6, 3)
	for i, p := range []string{"x/daily/blog/v1", "x/daily/microblog/v1", "x/daily/blog/v0"} {
		seedPkg(t, db, "alpha", "gno.land/r/moul/"+p, "blog", true, 900+i, 0)
	}

	got := searchPaths(t, db, "alpha", "blog")
	if len(got) == 0 || got[0] != "gno.land/r/gnoland/blog" {
		t.Fatalf("got %v, want the 134-call realm first", got)
	}
}

// Depth and use are added, not applied one after the other. Depth alone buried
// r/sys/namereg/v0 (77 calls) below three r/sys/* realms with none, which is
// the mirror image of the bug above.
func TestSearchDoesNotBuryABusyDeepRealm(t *testing.T) {
	db := NewTestDB(t)
	seedPkg(t, db, "alpha", "gno.land/r/sys/namereg/v0", "namereg", true, 10, 77)
	seedPkg(t, db, "alpha", "gno.land/r/sys/names", "names", true, 11, 1)
	for i, p := range []string{"txfees", "rewards", "cla"} {
		seedPkg(t, db, "alpha", "gno.land/r/sys/"+p, p, true, 900+i, 0)
	}

	got := searchPaths(t, db, "alpha", "sys")
	if len(got) == 0 || got[0] != "gno.land/r/sys/namereg/v0" {
		t.Fatalf("got %v, want the 77-call realm first even though it is a level deeper", got)
	}
}

// A hit on the package's own name beats one that is only in the path, and
// `name` is the Go package name, so a /vN suffix does not hide it.
func TestSearchPrefersANameMatchOverAPathMatch(t *testing.T) {
	db := NewTestDB(t)
	// Same depth and no calls on either, so only the match position can decide.
	seedPkg(t, db, "alpha", "gno.land/r/other/faucet/v0", "faucet", true, 10, 0)
	seedPkg(t, db, "alpha", "gno.land/r/faucet/unrelated/v0", "unrelated", true, 900, 0)

	got := searchPaths(t, db, "alpha", "faucet")
	if len(got) != 2 || got[0] != "gno.land/r/other/faucet/v0" {
		t.Fatalf("got %v, want the package actually named faucet first", got)
	}
}

// Recency is still the last word, so two packages a search cannot otherwise
// tell apart come back newest first, which is what the listing pages do.
func TestSearchFallsBackToRecency(t *testing.T) {
	db := NewTestDB(t)
	seedPkg(t, db, "alpha", "gno.land/r/ns/twin/v0", "twin", true, 10, 0)
	seedPkg(t, db, "alpha", "gno.land/r/ns/twin/v1", "twin", true, 20, 0)

	got := searchPaths(t, db, "alpha", "twin")
	if len(got) != 2 || got[0] != "gno.land/r/ns/twin/v1" {
		t.Fatalf("got %v, want the newer deploy first", got)
	}
}

// Ranking must not change which rows survive the per-kind cap in a way the
// outer sort then contradicts: the window and the final ORDER BY share one
// expression precisely so the top of the list cannot go missing.
func TestSearchCapKeepsTheBestRowsOfEachKind(t *testing.T) {
	db := NewTestDB(t)
	// Twelve realms, the best of them deployed first and therefore last by
	// recency: under the old order it fell outside the ten-row cap entirely.
	seedPkg(t, db, "alpha", "gno.land/r/cap/home", "home", true, 1, 50)
	for i := 0; i < 12; i++ {
		seedPkg(t, db, "alpha", "gno.land/r/cap/x/demo"+string(rune('a'+i))+"/v0", "demo", true, 900+i, 0)
	}
	seedPkg(t, db, "alpha", "gno.land/p/cap/lib", "lib", false, 2, 0)

	got := searchPaths(t, db, "alpha", "cap")
	if len(got) == 0 || got[0] != "gno.land/r/cap/home" {
		t.Fatalf("got %v, want the busy shallow realm to survive the cap", got)
	}
	var pkgs int
	for _, p := range got {
		if p == "gno.land/p/cap/lib" {
			pkgs++
		}
	}
	if pkgs != 1 {
		t.Errorf("the pure package did not survive its own cap: %v", got)
	}
}
