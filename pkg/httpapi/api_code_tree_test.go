package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// treeNow pins the activity window: it starts 2026-09-02T12:00:00Z.
var treeNow = time.Date(2026, 10, 2, 12, 34, 56, 0, time.UTC)

func pinTreeClock(t *testing.T) {
	t.Helper()
	prev := codeTreeNow
	codeTreeNow = func() time.Time { return treeNow }
	t.Cleanup(func() { codeTreeNow = prev })
}

// seedTree is seedSource plus what the tree reads beyond it: a library two
// packages import, calls inside and outside the window, and the same paths on
// beta with different numbers, so any mixing shows.
func seedTree(t *testing.T, db *store.DB) {
	t.Helper()
	seedSource(t, db)
	f := func(name, body string) indexer.MemFile { return indexer.MemFile{Name: name, Body: body} }
	lib := "gno.land/p/ns/lib"
	imp := "package x\n\nimport \"" + lib + "\"\n"
	deploySource(t, db, "alpha", lib, "tx-lib", 3, true, f("lib.gno", "package lib\n"))
	deploySource(t, db, "alpha", "gno.land/r/ns/user1", "tx-u1", 30, true, f("u.gno", imp))
	deploySource(t, db, "alpha", "gno.land/r/ns/user2", "tx-u2", 31, true, f("u.gno", imp))
	// beta: the same library with three importers of its own, and a bigger file.
	deploySource(t, db, "beta", lib, "tx-blib", 3, true, f("lib.gno", "package lib\n\n\n"))
	for i := 0; i < 3; i++ {
		deploySource(t, db, "beta", fmt.Sprintf("gno.land/r/b/u%d", i), fmt.Sprintf("tx-bu%d", i), 7+i, true, f("u.gno", imp))
	}

	// A path whose only submission failed, through the syncer's own path.
	deploySource(t, db, "alpha", "gno.land/r/ns/never", "tx-never", 40, false, f("n.gno", "package never\n"))
	// The same, the way a database synced before ProcessPackage stopped
	// writing failed submissions still holds it: a packages row and files
	// beside a failed submission.
	if err := db.UpsertPackage("alpha", "gno.land/r/ns/legacy", "legacy", "g1ns", "tx-leg", 41, "", true, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPackageFile("alpha", "gno.land/r/ns/legacy", "l.gno", "package legacy\n"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertPackageSubmission("alpha", "tx-leg", 0, "gno.land/r/ns/legacy", "legacy", "g1ns", 41, "", true, 1, "", false); err != nil {
		t.Fatal(err)
	}

	call := func(net, tx, when, caller, path string) {
		if err := db.InsertCall(net, tx, 100, 0, when, caller, path, "F", "", "", true); err != nil {
			t.Fatal(err)
		}
	}
	in, out := "2026-09-20T00:00:00Z", "2026-08-01T00:00:00Z"
	call("alpha", "c1", in, "g1a", "gno.land/r/ns/app/v2")
	call("alpha", "c2", in, "g1a", "gno.land/r/ns/app/v2")
	call("alpha", "c3", in, "g1b", "gno.land/r/ns/app/v2")
	call("alpha", "c4", out, "g1c", "gno.land/r/ns/app/v2") // before the window
	for i := 0; i < 5; i++ {
		call("beta", fmt.Sprintf("b%d", i), in, fmt.Sprintf("g1z%d", i), "gno.land/r/ns/app/v2")
	}
}

type treePkg struct {
	P   string            `json:"p"`
	NS  string            `json:"ns"`
	K   string            `json:"k"`
	H   int               `json:"h"`
	F   []json.RawMessage `json:"f"`
	L   int               `json:"l"`
	B   int               `json:"b"`
	Fam string            `json:"fam"`
	G   []int             `json:"g"`
	C   int               `json:"c"`
	U   int               `json:"u"`
	D   int               `json:"d"`
}

type treeBody struct {
	Network    string    `json:"network"`
	Height     int       `json:"height"`
	Count      int       `json:"count"`
	Files      int       `json:"files"`
	Lines      int       `json:"lines"`
	Bytes      int       `json:"bytes"`
	Since      string    `json:"since"`
	WindowDays int       `json:"window_days"`
	Packages   []treePkg `json:"packages"`
}

func getTree(t *testing.T, api *API, network string) (*httptest.ResponseRecorder, treeBody, map[string]treePkg) {
	t.Helper()
	rec := sourceGET(t, api, "/api/code/tree?network="+network)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body treeBody
	decodeInto(t, rec, &body)
	byPath := map[string]treePkg{}
	for _, p := range body.Packages {
		byPath[p.P] = p
	}
	return rec, body, byPath
}

func TestHandleCodeTreeRequiresOneNetwork(t *testing.T) {
	api, _ := newTestAPI(t)
	for _, url := range []string{"/api/code/tree", "/api/code/tree?network=all"} {
		if rec := sourceGET(t, api, url); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", url, rec.Code)
		}
	}
}

// AGENTS.md's first invariant: beta has the same paths with other files,
// other callers and other importers, and none of it may reach alpha's tree.
func TestHandleCodeTreeNetworkScoping(t *testing.T) {
	pinTreeClock(t)
	api, db := newTestAPI(t)
	seedTree(t, db)

	_, alpha, a := getTree(t, api, "alpha")
	_, beta, b := getTree(t, api, "beta")
	if alpha.Network != "alpha" || beta.Network != "beta" {
		t.Fatalf("network fields: %q, %q", alpha.Network, beta.Network)
	}
	if _, ok := a["gno.land/r/b/u0"]; ok {
		t.Error("beta's package is in alpha's tree")
	}
	if _, ok := b["gno.land/r/ns/user1"]; ok {
		t.Error("alpha's package is in beta's tree")
	}
	if got := a["gno.land/p/ns/lib"]; got.D != 2 || got.B != len("package lib\n") {
		t.Errorf("alpha lib: dependents %d bytes %d, want 2 and %d", got.D, got.B, len("package lib\n"))
	}
	if got := b["gno.land/p/ns/lib"]; got.D != 3 || got.L != 3 {
		t.Errorf("beta lib: dependents %d lines %d, want 3 and 3", got.D, got.L)
	}
	// alpha has three calls in the window from two accounts; beta has five
	// calls to the same path that alpha must not count.
	if got := a["gno.land/r/ns/app/v2"]; got.C != 3 || got.U != 2 {
		t.Errorf("alpha app/v2 activity: calls %d callers %d, want 3 and 2", got.C, got.U)
	}
	if _, ok := b["gno.land/r/ns/app/v2"]; ok {
		t.Error("beta never deployed app/v2, but calls to it put it in the tree")
	}
	if alpha.Count != len(alpha.Packages) || beta.Count != len(beta.Packages) {
		t.Errorf("count fields %d/%d, lists %d/%d", alpha.Count, beta.Count, len(alpha.Packages), len(beta.Packages))
	}
}

func TestHandleCodeTreeLeavesOutFailedOnlyPaths(t *testing.T) {
	pinTreeClock(t)
	api, db := newTestAPI(t)
	seedTree(t, db)
	_, _, a := getTree(t, api, "alpha")
	for _, p := range []string{"gno.land/r/ns/never", "gno.land/r/ns/legacy"} {
		if _, ok := a[p]; ok {
			t.Errorf("%s has only failed submissions and is in the tree", p)
		}
	}
	// A failed redeploy over a live package leaves the live one: app/v2's
	// stamp is 25, not the failed 26, and the file it dropped at 25 is gone.
	v2 := a["gno.land/r/ns/app/v2"]
	if v2.H != 25 || len(v2.F) != 2 {
		t.Errorf("app/v2: height %d files %d, want 25 and 2", v2.H, len(v2.F))
	}
}

func TestHandleCodeTreeFamilies(t *testing.T) {
	pinTreeClock(t)
	api, db := newTestAPI(t)
	seedTree(t, db)
	_, _, a := getTree(t, api, "alpha")

	for _, tc := range []struct {
		path string
		fam  string
		gen  []int
	}{
		// One entry per segment under the namespace: app is 0, v1 is 1.
		{"gno.land/r/ns/app/v1", "gno.land/r/ns/app", []int{0, 1}},
		{"gno.land/r/ns/app/v2", "gno.land/r/ns/app", []int{0, 2}},
		{"gno.land/r/ns/user1", "gno.land/r/ns/user", []int{1}},
		// No generation: the family is the path, so both are left out.
		{"gno.land/r/ns/other", "", nil},
		{"gno.land/p/ns/lib", "", nil},
	} {
		got, ok := a[tc.path]
		if !ok {
			t.Errorf("%s missing", tc.path)
			continue
		}
		if got.Fam != tc.fam || !reflect.DeepEqual(got.G, tc.gen) {
			t.Errorf("%s: fam %q gen %v, want %q %v", tc.path, got.Fam, got.G, tc.fam, tc.gen)
		}
	}
}

func TestHandleCodeTreePayloadShape(t *testing.T) {
	pinTreeClock(t)
	api, db := newTestAPI(t)
	seedTree(t, db)
	rec, body, a := getTree(t, api, "alpha")

	if body.Since != "2026-09-02T12:00:00Z" || body.WindowDays != 30 {
		t.Errorf("window: since %q days %d", body.Since, body.WindowDays)
	}
	v2 := a["gno.land/r/ns/app/v2"]
	if v2.NS != "ns" || v2.K != "r" || a["gno.land/p/ns/lib"].K != "p" {
		t.Errorf("namespace/kind: %q %q %q", v2.NS, v2.K, a["gno.land/p/ns/lib"].K)
	}
	// Files are [name, lines, bytes], sorted by name. é is two bytes, one
	// character: a size counted in characters would say 15.
	var files [][]any
	for _, raw := range v2.F {
		var f []any
		if err := json.Unmarshal(raw, &f); err != nil || len(f) != 3 {
			t.Fatalf("file entry %s is not a triple", raw)
		}
		files = append(files, f)
	}
	want := [][]any{{"app.gno", 3.0, 24.0}, {"é.gno", 1.0, 16.0}}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files %v, want %v", files, want)
	}
	if v2.L != 4 || v2.B != 40 {
		t.Errorf("totals: lines %d bytes %d, want 4 and 40", v2.L, v2.B)
	}

	var lines, bytes, nfiles, height int
	for _, p := range body.Packages {
		lines += p.L
		bytes += p.B
		nfiles += len(p.F)
		if p.H > height {
			height = p.H
		}
	}
	if body.Lines != lines || body.Bytes != bytes || body.Files != nfiles || body.Height != height {
		t.Errorf("chain totals %d/%d/%d/%d, want %d/%d/%d/%d",
			body.Lines, body.Bytes, body.Files, body.Height, lines, bytes, nfiles, height)
	}
	// Zero activity is left out rather than sent as 0 for most of a chain.
	if strings.Contains(rec.Body.String(), `"c":0`) || strings.Contains(rec.Body.String(), `"d":0`) {
		t.Error("zero-valued activity fields are serialized")
	}
	// Sorted by path, so a client can binary-search or walk it as a tree.
	for i := 1; i < len(body.Packages); i++ {
		if body.Packages[i-1].P >= body.Packages[i].P {
			t.Errorf("not sorted at %d: %s >= %s", i, body.Packages[i-1].P, body.Packages[i].P)
		}
	}
}

func TestHandleCodeTreeETag(t *testing.T) {
	pinTreeClock(t)
	api, db := newTestAPI(t)
	seedTree(t, db)

	first, body, _ := getTree(t, api, "alpha")
	etag := first.Header().Get("ETag")
	wantPrefix := fmt.Sprintf(`"ct-%d-%d-`, body.Height, body.Count)
	if !strings.HasPrefix(etag, wantPrefix) {
		t.Fatalf("ETag %q, want prefix %s", etag, wantPrefix)
	}
	if cc := first.Header().Get("Cache-Control"); cc != "public, max-age=60" {
		t.Errorf("Cache-Control %q", cc)
	}
	if rec := sourceGET(t, api, "/api/code/tree?network=alpha", "If-None-Match", etag); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: status %d, want 304", rec.Code)
	}
	if getTreeETag(t, api, "beta") == etag {
		t.Error("two networks share an ETag")
	}

	// A call moves the activity and not the code: same prefix, new validator.
	if err := db.InsertCall("alpha", "c-new", 100, 0, "2026-10-01T00:00:00Z", "g1q", "gno.land/r/ns/other", "F", "", "", true); err != nil {
		t.Fatal(err)
	}
	afterCall := getTreeETag(t, api, "alpha")
	if afterCall == etag || !strings.HasPrefix(afterCall, wantPrefix) {
		t.Errorf("after a call: ETag %q (was %q)", afterCall, etag)
	}
	// A deploy moves the code, and the prefix with it.
	deploySource(t, db, "alpha", "gno.land/r/ns/fresh", "tx-fresh", 99, true, indexer.MemFile{Name: "f.gno", Body: "package fresh\n"})
	if afterDeploy := getTreeETag(t, api, "alpha"); !strings.HasPrefix(afterDeploy, fmt.Sprintf(`"ct-99-%d-`, body.Count+1)) {
		t.Errorf("after a deploy: ETag %q", afterDeploy)
	}
}

func getTreeETag(t *testing.T, api *API, network string) string {
	t.Helper()
	rec, _, _ := getTree(t, api, network)
	return rec.Header().Get("ETag")
}

// Through the real cache: the entry gets the tree's own TTL, and a hit
// replays the validator and answers If-None-Match without the handler.
func TestHandleCodeTreeThroughTheCache(t *testing.T) {
	pinTreeClock(t)
	api, db := newTestAPI(t)
	seedTree(t, db)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	c := NewResponseCache(CacheTTL)
	h := WithResponseCache(c, mux)

	if got := c.ttlForRequest(httptest.NewRequest(http.MethodGet, "/api/code/tree?network=alpha", nil)); got != 5*time.Minute {
		t.Errorf("ttl %s, want 5m", got)
	}
	get := func(inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/code/tree?network=alpha", nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first := get("")
	if rec := get(first.Header().Get("ETag")); rec.Code != http.StatusNotModified || rec.Header().Get("X-Cache") != "HIT" {
		t.Errorf("cached If-None-Match: status %d X-Cache %q, want 304 HIT", rec.Code, rec.Header().Get("X-Cache"))
	}
}

func TestCodeTreeIsListedInEndpoints(t *testing.T) {
	api, _ := newTestAPI(t)
	api.RegisterRoutes(http.NewServeMux())
	for _, e := range api.routes {
		if e.Path == CodeTreePath && e.Method == http.MethodGet {
			return
		}
	}
	t.Errorf("%s is not in /api/endpoints", CodeTreePath)
}
