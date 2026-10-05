package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
	"github.com/gnoverse/gnoscope/pkg/tags"
)

// seedTags: on alpha a token realm with a page that emits events, a library
// it imports, and a realm with a page only; beta holds the token realm's path
// with code that earns nothing, so a reader that mixed the chains would tag it.
func seedTags(t *testing.T, db *store.DB) {
	t.Helper()
	f := func(body string) indexer.MemFile { return indexer.MemFile{Name: "a.gno", Body: "package a\n\n" + body} }
	tlDeploy(t, db, "alpha", "gno.land/p/x/kit", "tx-kit", 1, "2026-09-20T00:00:00Z", "g1alice", true, f("func K() {}"))
	tlDeploy(t, db, "alpha", "gno.land/r/x/coin", "tx-coin", 2, "2026-09-21T00:00:00Z", "g1alice", true,
		f("import (\n\t\"chain\"\n\t\"gno.land/p/nt/grc20/v0\"\n\t\"gno.land/p/x/kit\"\n)\n\nfunc Render(string) string { chain.Emit(\"x\"); return \"\" }"))
	tlDeploy(t, db, "alpha", "gno.land/r/x/page", "tx-page", 3, "2026-09-22T00:00:00Z", "g1bob", true,
		f("func Render(string) string { return \"\" }"))
	tlDeploy(t, db, "beta", "gno.land/r/x/coin", "tx-coin-b", 2, "2026-09-21T00:00:00Z", "g1alice", true, f("func F() {}"))
}

func tagNamesOf(ts []tags.Tag) string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Tag)
	}
	return strings.Join(out, ",")
}

func TestTagsOnTheTimeline(t *testing.T) {
	api, db := newTestAPI(t)
	seedTags(t, db)

	_, body := getTimeline(t, api, "network=alpha")
	by := map[string]timelineRow{}
	for _, r := range body.Rows {
		by[r.Path] = r
	}
	coin := by["gno.land/r/x/coin"]
	if tagNamesOf(coin.Tags) != "token,events,render" {
		t.Fatalf("coin tags: %+v", coin.Tags)
	}
	if !strings.Contains(coin.Tags[0].Why, "imports gno.land/p/nt/grc20/v0 (a.gno:5)") {
		t.Errorf("evidence: %q", coin.Tags[0].Why)
	}
	if tagNamesOf(by["gno.land/p/x/kit"].Tags) != "library" {
		t.Errorf("kit: %+v", by["gno.land/p/x/kit"].Tags)
	}

	_, body = getTimeline(t, api, "network=alpha&tag=render")
	if tlPaths(body.Rows) != "gno.land/r/x/page gno.land/r/x/coin" || body.Total != 2 {
		t.Errorf("tag=render: %s (total %d)", tlPaths(body.Rows), body.Total)
	}
	_, body = getTimeline(t, api, "network=beta&tag=token")
	if len(body.Rows) != 0 {
		t.Errorf("beta took alpha's tags: %s", tlPaths(body.Rows))
	}
	if rec := sourceGET(t, api, "/api/code/timeline?network=alpha&tag=defii"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown tag: %d", rec.Code)
	}
}

func tlPaths(rows []timelineRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Path)
	}
	return strings.Join(out, " ")
}

func TestTagsOnListingsRealmTreeAndIndex(t *testing.T) {
	api, db := newTestAPI(t)
	seedTags(t, db)

	type listing struct {
		Items []store.PackageInfo `json:"items"`
		Total int                 `json:"total"`
	}
	var l listing
	rec := sourceGET(t, api, "/api/realms?network=alpha&tag=token")
	decodeInto(t, rec, &l)
	if l.Total != 1 || len(l.Items) != 1 || l.Items[0].Path != "gno.land/r/x/coin" || tagNamesOf(l.Items[0].Tags) != "token,events,render" {
		t.Errorf("realms tag=token: %+v", l)
	}
	l = listing{}
	decodeInto(t, sourceGET(t, api, "/api/packages?network=alpha&tag=library"), &l)
	if l.Total != 1 || l.Items[0].Path != "gno.land/p/x/kit" {
		t.Errorf("packages tag=library: %+v", l)
	}
	l = listing{}
	decodeInto(t, sourceGET(t, api, "/api/packages?network=alpha&kind=all"), &l)
	if l.Total != 3 {
		t.Errorf("unfiltered: %d", l.Total)
	}
	if rec := sourceGET(t, api, "/api/realms?network=alpha&tag=nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown tag on realms: %d", rec.Code)
	}

	var facets struct {
		Tags []store.TagCount `json:"tags"`
	}
	decodeInto(t, sourceGET(t, api, "/api/packages/facets?network=alpha&kind=realm"), &facets)
	var fs []string
	for _, c := range facets.Tags {
		fs = append(fs, c.Tag+"="+strings.Repeat("|", c.Packages))
	}
	if strings.Join(fs, " ") != "token=| events=| render=||" {
		t.Errorf("facets: %v", fs)
	}

	var detail struct {
		Tags []tags.Tag `json:"tags"`
	}
	decodeInto(t, sourceGET(t, api, "/api/realm/r/x/coin?network=alpha"), &detail)
	if tagNamesOf(detail.Tags) != "token,events,render" {
		t.Errorf("realm: %+v", detail.Tags)
	}
	detail.Tags = nil
	decodeInto(t, sourceGET(t, api, "/api/realm/r/x/coin?network=beta"), &detail)
	if len(detail.Tags) != 0 {
		t.Errorf("beta realm: %+v", detail.Tags)
	}

	var tree struct {
		Count    int `json:"count"`
		Packages []struct {
			Path string   `json:"p"`
			Tags []string `json:"t"`
		} `json:"packages"`
	}
	decodeInto(t, sourceGET(t, api, "/api/code/tree?network=alpha"), &tree)
	got := map[string]string{}
	for _, p := range tree.Packages {
		got[p.Path] = strings.Join(p.Tags, ",")
	}
	if got["gno.land/r/x/coin"] != "token,events,render" || got["gno.land/p/x/kit"] != "library" {
		t.Errorf("tree: %v", got)
	}
	tree.Packages = nil
	decodeInto(t, sourceGET(t, api, "/api/code/tree?network=alpha&tag=events"), &tree)
	if tree.Count != 1 || len(tree.Packages) != 1 || tree.Packages[0].Path != "gno.land/r/x/coin" {
		t.Errorf("tree tag=events: %+v", tree)
	}

	var idx struct {
		Rules string `json:"rules"`
		Tags  []struct {
			Tag      string `json:"tag"`
			Means    string `json:"means"`
			Packages int    `json:"packages"`
		} `json:"tags"`
	}
	decodeInto(t, sourceGET(t, api, "/api/tags?network=alpha"), &idx)
	if idx.Rules != tags.Version || len(idx.Tags) != len(tags.Rules) {
		t.Fatalf("index: %+v", idx)
	}
	counts := map[string]int{}
	for _, x := range idx.Tags {
		counts[x.Tag] = x.Packages
		if x.Means == "" {
			t.Errorf("%s has no sentence", x.Tag)
		}
	}
	if counts["render"] != 2 || counts["token"] != 1 || counts["library"] != 1 || counts["nft"] != 0 {
		t.Errorf("counts: %v", counts)
	}
	var one struct {
		Tags []tags.Tag `json:"tags"`
	}
	decodeInto(t, sourceGET(t, api, "/api/tags?network=alpha&path=gno.land/p/x/kit"), &one)
	if len(one.Tags) != 1 || one.Tags[0].Why != "imported by gno.land/r/x/coin" {
		t.Errorf("one: %+v", one)
	}
	if rec := sourceGET(t, api, "/api/tags"); rec.Code != http.StatusBadRequest {
		t.Errorf("no network: %d", rec.Code)
	}
}
