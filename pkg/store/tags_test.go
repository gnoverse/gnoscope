package store

import (
	"strings"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/tags"
)

func tagNames(ts []tags.Tag) string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Tag)
	}
	return strings.Join(out, ",")
}

const (
	tgLib   = "gno.land/p/x/kit"
	tgToken = "gno.land/r/x/coin"
)

func tgFiles(body string) []indexer.MemFile {
	return []indexer.MemFile{{Name: "a.gno", Body: "package a\n\n" + body}}
}

func tgStore(t *testing.T, db *DB, network, path, tx string, height int, body string, imports ...string) {
	t.Helper()
	if err := db.ReplacePackage(network, path, "a", "g1creator", tx, height, "", strings.HasPrefix(path, "gno.land/r/"), tgFiles(body)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDependencies(network, path, imports); err != nil {
		t.Fatal(err)
	}
}

func tagsOf(t *testing.T, db *DB, network, path string) string {
	t.Helper()
	m, err := db.PackageTags(network, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	return tagNames(m[path])
}

// Storing a package tags it in the same write, and its importers make a
// pure package a library, on its own network only.
func TestReplacePackageTagsItAndScopesByNetwork(t *testing.T) {
	db := NewTestDB(t)
	tgStore(t, db, "alpha", tgLib, "tx-lib", 1, "func F() {}")
	if got := tagsOf(t, db, "alpha", tgLib); got != "" {
		t.Errorf("a library nobody imports yet: %q", got)
	}
	tgStore(t, db, "alpha", tgToken, "tx-coin", 2,
		"import (\n\"chain\"\n\"gno.land/p/nt/grc20/v0\"\n\"gno.land/p/x/kit\"\n)\nfunc Render(string) string { chain.Emit(\"x\"); return \"\" }",
		"gno.land/p/nt/grc20/v0", tgLib)
	if got := tagsOf(t, db, "alpha", tgToken); got != "token,events,render" {
		t.Errorf("realm tags: %q", got)
	}
	if got := tagsOf(t, db, "alpha", tgLib); got != "library" {
		t.Errorf("imported now, so a library: %q", got)
	}
	m, _ := db.PackageTags("alpha", []string{tgLib})
	if w := m[tgLib][0].Why; w != "imported by gno.land/r/x/coin" {
		t.Errorf("library evidence %q", w)
	}

	// Same paths on another chain, nothing importing the library there.
	tgStore(t, db, "beta", tgLib, "tx-lib-b", 1, "func F() {}")
	tgStore(t, db, "beta", tgToken, "tx-coin-b", 2, "func F() {}")
	if got := tagsOf(t, db, "beta", tgToken); got != "" {
		t.Errorf("beta's realm took alpha's tags: %q", got)
	}
	if got := tagsOf(t, db, "beta", tgLib); got != "" {
		t.Errorf("beta's library counted alpha's importer: %q", got)
	}
	if got := tagsOf(t, db, "alpha", tgToken); got != "token,events,render" {
		t.Errorf("writing beta moved alpha: %q", got)
	}

	// The importer drops the import: no longer a library.
	if err := db.SetDependencies("alpha", tgToken, []string{"gno.land/p/nt/grc20/v0"}); err != nil {
		t.Fatal(err)
	}
	if got := tagsOf(t, db, "alpha", tgLib); got != "" {
		t.Errorf("still a library with no importer: %q", got)
	}

	// A redeploy replaces the tags with what the new source earns.
	tgStore(t, db, "alpha", tgToken, "tx-coin-2", 3, "func F() {}")
	if got := tagsOf(t, db, "alpha", tgToken); got != "" {
		t.Errorf("redeploy kept the old tags: %q", got)
	}
}

// The refresh pass fills what was written another way, does nothing the
// second time, recomputes everything when the rule set changes, and drops the
// tags of a package that is gone.
func TestRefreshPackageTags(t *testing.T) {
	db := NewTestDB(t)
	// Written behind the store's back, the way a database from before the
	// table, or the e2e fixture, holds its packages.
	for _, q := range []string{
		`INSERT INTO packages (network, path, name, creator, tx_hash, block_height, is_realm, num_files) VALUES
			('alpha', 'gno.land/r/x/coin', 'coin', 'g1', 'tx-1', 1, 1, 1),
			('alpha', 'gno.land/p/x/kit', 'kit', 'g1', 'tx-2', 1, 0, 1),
			('beta', 'gno.land/r/x/coin', 'coin', 'g1', 'tx-3', 1, 1, 1)`,
		`INSERT INTO package_files (network, package_path, file_name, body) VALUES
			('alpha', 'gno.land/r/x/coin', 'a.gno', 'package a
import "chain"
func F() { chain.Emit("x") }'),
			('alpha', 'gno.land/p/x/kit', 'a.gno', 'package a'),
			('beta', 'gno.land/r/x/coin', 'a.gno', 'package a')`,
		`INSERT INTO dependencies (network, package_path, import_path) VALUES ('alpha', 'gno.land/r/x/coin', 'gno.land/p/x/kit')`,
	} {
		if _, err := db.SQL().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	res, err := db.RefreshPackageTags()
	if err != nil {
		t.Fatal(err)
	}
	if res.Packages != 3 {
		t.Errorf("first pass recomputed %d, want 3", res.Packages)
	}
	if got := tagsOf(t, db, "alpha", tgToken); got != "events" {
		t.Errorf("alpha coin: %q", got)
	}
	if got := tagsOf(t, db, "alpha", tgLib); got != "library" {
		t.Errorf("alpha kit: %q", got)
	}
	if got := tagsOf(t, db, "beta", tgToken); got != "" {
		t.Errorf("beta coin: %q", got)
	}
	if v, _ := db.GetSyncState(TagsStateKey); v != tags.Version {
		t.Errorf("state %q", v)
	}

	before := tgDump(t, db)
	res, err = db.RefreshPackageTags()
	if err != nil {
		t.Fatal(err)
	}
	if res.Packages != 0 || tgDump(t, db) != before {
		t.Errorf("second pass was not a no-op: %+v\n%s\nvs\n%s", res, tgDump(t, db), before)
	}

	// A different rule set: every row was written under another version.
	if _, err := db.SQL().Exec(`UPDATE package_tags_state SET rules = 'old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`DELETE FROM package_tags`); err != nil {
		t.Fatal(err)
	}
	res, err = db.RefreshPackageTags()
	if err != nil {
		t.Fatal(err)
	}
	if res.Packages != 3 || tgDump(t, db) != before {
		t.Errorf("version bump: %+v\n%s", res, tgDump(t, db))
	}

	// A package that is gone loses its tags.
	if _, err := db.SQL().Exec(`DELETE FROM packages WHERE network = 'alpha' AND path = ?`, tgToken); err != nil {
		t.Fatal(err)
	}
	res, err = db.RefreshPackageTags()
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 || tagsOf(t, db, "alpha", tgToken) != "" {
		t.Errorf("orphan kept: %+v", res)
	}
}

func tgDump(t *testing.T, db *DB) string {
	t.Helper()
	rows, err := db.SQL().Query(`SELECT network, path, tag, evidence FROM package_tags ORDER BY 1, 2, 3`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var n, p, tg, e string
		if err := rows.Scan(&n, &p, &tg, &e); err != nil {
			t.Fatal(err)
		}
		b.WriteString(n + " " + p + " " + tg + " " + e + "\n")
	}
	return b.String()
}

func TestTagFilterAndCounts(t *testing.T) {
	db := NewTestDB(t)
	tgStore(t, db, "alpha", tgLib, "tx-lib", 1, "func F() {}")
	tgStore(t, db, "alpha", tgToken, "tx-coin", 2, "import \"gno.land/p/nt/grc20/v0\"\nfunc Render(string) string { return \"\" }", tgLib)
	tgStore(t, db, "alpha", "gno.land/r/x/page", "tx-page", 3, "func Render(string) string { return \"\" }")
	tgStore(t, db, "beta", "gno.land/r/x/page", "tx-page-b", 3, "func Render(string) string { return \"\" }")

	got, err := db.ListPackages("alpha", PackageFilter{Kind: KindAll, Tag: "render"}, 50, 0, "name")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, p := range got {
		paths = append(paths, p.Path)
	}
	if strings.Join(paths, " ") != "gno.land/r/x/coin gno.land/r/x/page" {
		t.Errorf("render filter: %v", paths)
	}
	if n, _ := db.CountPackages("alpha", PackageFilter{Kind: KindAll, Tag: "token"}); n != 1 {
		t.Errorf("token count %d", n)
	}
	counts, err := db.TagCounts("alpha", PackageFilter{Kind: KindAll, Tag: "token"})
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, c := range counts {
		s = append(s, c.Tag+"="+itoa(c.Packages))
	}
	// The tag filter is ignored for the counts, the network is not.
	if strings.Join(s, " ") != "token=1 render=2 library=1" {
		t.Errorf("counts: %v", s)
	}
}
