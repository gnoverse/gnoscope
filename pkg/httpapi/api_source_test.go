package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/analyzer"
	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// deploySource runs one submission through the same path the syncer does.
func deploySource(t *testing.T, db *store.DB, network, path, tx string, height int, ok bool, files ...indexer.MemFile) {
	t.Helper()
	name := path[strings.LastIndex(path, "/")+1:]
	pkg := &indexer.MemPackage{Name: name, Path: path, Files: files}
	if err := analyzer.NewAnalyzer(db).ProcessPackage(network, pkg, "g1ns", tx, height, 0,
		"2026-09-01T00:00:00Z", "", ok); err != nil {
		t.Fatalf("ProcessPackage(%s %s): %v", network, path, err)
	}
}

// seedSource builds one app family on alpha, the same family on beta, and an
// unrelated realm in the same namespace.
//
// app/v2 is deployed at 20, redeployed at 25 (one file dropped), and then a
// submission at 26 fails: the stamp is 25, three submissions, one redeploy.
func seedSource(t *testing.T, db *store.DB) {
	t.Helper()
	f := func(name, body string) indexer.MemFile { return indexer.MemFile{Name: name, Body: body} }
	deploySource(t, db, "alpha", "gno.land/r/ns/app/v1", "tx-v1", 10, true, f("app.gno", "package v1\n"))
	deploySource(t, db, "alpha", "gno.land/r/ns/app/v2", "tx-v2a", 20, true,
		f("app.gno", "package v2\n"), f("gone.gno", "package v2\n"))
	deploySource(t, db, "alpha", "gno.land/r/ns/app/v2", "tx-v2b", 25, true,
		f("app.gno", "package v2\n\nfunc A() {}\n"), f("é.gno", "package v2 // é"))
	deploySource(t, db, "alpha", "gno.land/r/ns/app/v2", "tx-v2c", 26, false, f("app.gno", "broken"))
	deploySource(t, db, "alpha", "gno.land/r/ns/other", "tx-o", 11, true, f("o.gno", "package other\n"))
	deploySource(t, db, "beta", "gno.land/r/ns/app/v3", "tx-b3", 5, true, f("app.gno", "package v3\n"))
}

func sourceGET(t *testing.T, api *API, url string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
}

func TestHandleSourceManifest(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)

	rec := sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got sourceManifest
	decodeInto(t, rec, &got)

	if got.Path != "gno.land/r/ns/app/v2" || got.Network != "alpha" || got.Kind != "realm" {
		t.Errorf("identity = %q %q %q", got.Path, got.Network, got.Kind)
	}
	// The failed submission at 26 is not the stamp: its source never existed.
	if got.Stamp.Height != 25 || got.Stamp.TxHash != "tx-v2b" || got.Stamp.Time == "" {
		t.Errorf("stamp = %+v, want height 25 tx-v2b with a time", got.Stamp)
	}
	if got.Submissions != 3 || got.Redeploys != 1 {
		t.Errorf("submissions %d redeploys %d, want 3 and 1", got.Submissions, got.Redeploys)
	}
	// gone.gno was dropped by the redeploy. Sizes are bytes, so é counts two.
	want := []store.SourceFile{
		{Name: "app.gno", Size: 24, Lines: 3},
		{Name: "é.gno", Size: 16, Lines: 1},
	}
	if len(got.Files) != len(want) {
		t.Fatalf("files = %+v, want %+v", got.Files, want)
	}
	for i, w := range want {
		g := got.Files[i]
		if g.Name != w.Name || g.Size != w.Size || g.Lines != w.Lines || g.Body != nil {
			t.Errorf("file %d = %+v (body %v), want %+v and no body", i, g, g.Body != nil, w)
		}
	}
	// v1 is a sibling; other is not the same app; beta's v3 is another chain.
	if len(got.Siblings) != 1 || got.Siblings[0].Path != "gno.land/r/ns/app/v1" ||
		got.Siblings[0].Stamp.Height != 10 {
		t.Errorf("siblings = %+v, want only alpha's app/v1 at 10", got.Siblings)
	}

	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("manifest is marked immutable (%q), but it moves with every deploy", cc)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("manifest has no ETag")
	}
	if rec := sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha", "If-None-Match", etag); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match with the current ETag: status %d, want 304", rec.Code)
	}

	// A sibling deploy changes the manifest's bytes without moving this
	// package's stamp, so the validator has to move with it.
	deploySource(t, db, "alpha", "gno.land/r/ns/app/v3", "tx-v3", 30, true,
		indexer.MemFile{Name: "app.gno", Body: "package v3\n"})
	rec = sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha", "If-None-Match", etag)
	if rec.Code != http.StatusOK {
		t.Errorf("after a sibling deploy, the old ETag still answered %d", rec.Code)
	}
}

func TestHandleSourcePinned(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)

	rec := sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at=25")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	var got pinnedSource
	decodeInto(t, rec, &got)
	if got.Stamp.Height != 25 || len(got.Files) != 2 {
		t.Fatalf("pinned = %+v", got)
	}
	if got.Files[0].Body == nil || *got.Files[0].Body != "package v2\n\nfunc A() {}\n" {
		t.Errorf("app.gno body = %v", got.Files[0].Body)
	}

	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("pinned read has no ETag")
	}
	if rec := sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at=25", "If-None-Match", etag); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: status %d, want 304", rec.Code)
	}

	// One file, under its own validator.
	rec = sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at=25&file=%C3%A9.gno")
	if rec.Code != http.StatusOK {
		t.Fatalf("file=: status %d: %s", rec.Code, rec.Body.String())
	}
	decodeInto(t, rec, &got)
	if len(got.Files) != 1 || got.Files[0].Name != "é.gno" || got.Files[0].Body == nil {
		t.Errorf("file= returned %+v", got.Files)
	}
	if rec.Header().Get("ETag") == etag {
		t.Error("a single file shares the whole package's ETag")
	}
}

// A height with no stored submission at the path names no bytes here.
// Answering with today's bytes would cache them under that stamp, for a year,
// in every browser that asked.
func TestHandleSourceUnknownStampIsAConflict(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)

	for _, at := range []string{"0", "21", "99"} {
		rec := sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at="+at)
		if rec.Code != http.StatusConflict {
			t.Errorf("at=%s: status %d, want 409", at, rec.Code)
			continue
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("at=%s: Cache-Control %q, want no-store", at, cc)
		}
		var body struct {
			Current store.SourceStamp `json:"current"`
			Files   []any             `json:"files"`
		}
		decodeInto(t, rec, &body)
		if body.Current.Height != 25 || body.Current.TxHash != "tx-v2b" {
			t.Errorf("at=%s: current = %+v, want 25 tx-v2b", at, body.Current)
		}
		if body.Files != nil {
			t.Errorf("at=%s: a conflict carried files", at)
		}
	}
}

// Every submission's files are stored, so a stamp that is no longer current
// still names bytes: the ones that submission carried, failed ones included,
// under the same immutable caching as the current stamp.
func TestHandleSourceServesAnOlderSubmission(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)

	rec := sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("at=20: status %d: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("at=20: Cache-Control = %q", cc)
	}
	var got pinnedSource
	decodeInto(t, rec, &got)
	if got.Stamp.Height != 20 || got.Stamp.TxHash != "tx-v2a" || got.Failed {
		t.Errorf("at=20: stamp %+v failed %v, want 20 tx-v2a, not failed", got.Stamp, got.Failed)
	}
	// gone.gno was dropped at 25 and is still part of what 20 published.
	if len(got.Files) != 2 || got.Files[1].Name != "gone.gno" || got.Files[1].Body == nil || *got.Files[1].Body != "package v2\n" {
		t.Errorf("at=20: files %+v", got.Files)
	}
	etag20 := rec.Header().Get("ETag")

	// The failed submission at 26: what was sent, and marked as rejected.
	rec = sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at=26")
	if rec.Code != http.StatusOK {
		t.Fatalf("at=26: status %d: %s", rec.Code, rec.Body.String())
	}
	decodeInto(t, rec, &got)
	if !got.Failed || got.Stamp.TxHash != "tx-v2c" || len(got.Files) != 1 || *got.Files[0].Body != "broken" {
		t.Errorf("at=26: %+v, want the failed submission's one file", got)
	}
	if rec.Header().Get("ETag") == etag20 {
		t.Error("two submissions share an ETag")
	}

	// Tokens work on an older stamp too.
	rec = sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at=20&file=gone.gno&tokens=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("tokens at=20: status %d: %s", rec.Code, rec.Body.String())
	}

	// The current stamp still answers from current state.
	rec = sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at=25&tx=tx-v2b")
	if rec.Code != http.StatusOK {
		t.Fatalf("at=25 tx=: status %d", rec.Code)
	}
	// A tx that is not at that height is a conflict, not a fallback.
	if rec := sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at=25&tx=tx-v2a"); rec.Code != http.StatusConflict {
		t.Errorf("at=25 tx=tx-v2a: status %d, want 409", rec.Code)
	}
}

// A database synced before per-submission source existed has the submission
// rows and not their files, until the backfill reaches them: those heights
// stay a conflict rather than being served something else.
func TestHandleSourceUnbackfilledSubmissionIsAConflict(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)
	if _, err := db.SQL().Exec(`DELETE FROM submission_files WHERE tx_hash = 'tx-v2a'`); err != nil {
		t.Fatal(err)
	}
	if rec := sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha&at=20"); rec.Code != http.StatusConflict {
		t.Errorf("at=20 without files: status %d, want 409", rec.Code)
	}
	var m sourceManifest
	decodeInto(t, sourceGET(t, api, "/api/source/r/ns/app/v2?network=alpha"), &m)
	if len(m.History) != 3 || m.History[0].Stored || !m.History[1].Stored || m.History[2].Success {
		t.Errorf("history = %+v, want 20 unstored, 25 stored, 26 failed", m.History)
	}
}

func TestHandleSourceRejects(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)

	for _, tc := range []struct {
		url  string
		want int
	}{
		{"/api/source/r/ns/app/v2", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2?network=all", http.StatusBadRequest},
		{"/api/source/r/ns/nope?network=alpha", http.StatusNotFound},
		{"/api/source/r/ns/nope?network=alpha&at=25", http.StatusNotFound},
		// beta has no v2: network scoping, not a fallback to another chain.
		{"/api/source/r/ns/app/v2?network=beta", http.StatusNotFound},
		{"/api/source/r/ns/app/v2?network=alpha&at=abc", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2?network=alpha&at=-1", http.StatusBadRequest},
		// 0 is a height like any other, so it is an unknown stamp here, not
		// a malformed one.
		{"/api/source/r/ns/app/v2?network=alpha&at=0", http.StatusConflict},
		{"/api/source/r/ns/app/v2?network=alpha&file=app.gno", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2?network=alpha&at=25&file=gone.gno", http.StatusNotFound},
		// tokens= is one file of one stamp, in a version this server speaks.
		{"/api/source/r/ns/app/v2?network=alpha&tokens=1", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2?network=alpha&at=25&tokens=1", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2?network=alpha&file=app.gno&tokens=1", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2?network=alpha&at=25&file=app.gno&tokens=2", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2?network=alpha&at=25&file=app.gno&tokens=", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2?network=alpha&at=25&file=gone.gno&tokens=1", http.StatusNotFound},
		{"/api/source/r/ns/app/v2?network=alpha&at=21&file=app.gno&tokens=1", http.StatusConflict},
	} {
		if rec := sourceGET(t, api, tc.url); rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", tc.url, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// Genesis packages carry height 0, and that has to be pinnable.
func TestHandleSourcePinsAGenesisPackage(t *testing.T) {
	api, db := newTestAPI(t)
	deploySource(t, db, "alpha", "gno.land/p/ns/gen", "tx-genesis", 0, true,
		indexer.MemFile{Name: "gen.gno", Body: "package gen\n"})

	rec := sourceGET(t, api, "/api/source/p/ns/gen?network=alpha&at=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("at=0 on a genesis package: status %d: %s", rec.Code, rec.Body.String())
	}
	var got pinnedSource
	decodeInto(t, rec, &got)
	if got.Kind != "pure" || len(got.Files) != 1 || got.Files[0].Body == nil {
		t.Errorf("pinned genesis package = %+v", got)
	}
}

// tokens=1 classifies one file of a pinned stamp, with the same immutable
// caching as its body and a validator of its own.
func TestHandleSourceTokens(t *testing.T) {
	api, db := newTestAPI(t)
	f := func(name, body string) indexer.MemFile { return indexer.MemFile{Name: name, Body: body} }
	deploySource(t, db, "alpha", "gno.land/r/ns/tok", "tx-t", 30, true,
		f("a.gno", "package tok\n\nimport \"gno.land/p/nt/avl\"\n\nvar s = \"</script>\"\n\nfunc A() *avl.Tree { return B() }\n"),
		f("b.gno", "package tok\n\nfunc B() *avl.Tree { return nil }\n"))

	url := "/api/source/r/ns/tok?network=alpha&at=30&file=a.gno&tokens=1"
	rec := sourceGET(t, api, url)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if strings.Contains(rec.Body.String(), "</script>") {
		t.Error("a script close tag reached the response unescaped")
	}
	var got struct {
		File   string            `json:"file"`
		Tokens string            `json:"tokens"`
		Stamp  store.SourceStamp `json:"stamp"`
		Lines  [][]any           `json:"lines"`
		Decls  map[string][]any  `json:"decls"`
	}
	decodeInto(t, rec, &got)
	if got.File != "a.gno" || got.Tokens != "1" || got.Stamp.Height != 30 {
		t.Errorf("header fields = %q %q %d", got.File, got.Tokens, got.Stamp.Height)
	}
	if len(got.Lines) != 8 {
		t.Fatalf("%d lines, want 8 (the trailing newline is an empty last line)", len(got.Lines))
	}
	// Line 3 is the import, line 7 calls a func declared in b.gno.
	if s := fmt.Sprint(got.Lines[2]); !strings.Contains(s, "[imp \"gno.land/p/nt/avl\"]") {
		t.Errorf("import line = %s", s)
	}
	if s := fmt.Sprint(got.Lines[6]); !strings.Contains(s, "[ref B]") || !strings.Contains(s, "[decl A]") {
		t.Errorf("line 7 = %s", s)
	}
	if loc := fmt.Sprint(got.Decls["B"]); loc != "[b.gno 3]" {
		t.Errorf("decls[B] = %s", loc)
	}

	etag := rec.Header().Get("ETag")
	body := sourceGET(t, api, "/api/source/r/ns/tok?network=alpha&at=30&file=a.gno")
	if etag == "" || etag == body.Header().Get("ETag") {
		t.Errorf("tokens ETag %q must exist and differ from the body's", etag)
	}
	if rec := sourceGET(t, api, url, "If-None-Match", etag); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: status %d, want 304", rec.Code)
	}
}

// The pinned read is the one cached entry whose TTL depends on the query.
func TestPinnedSourceGetsTheLongTTL(t *testing.T) {
	c := NewResponseCache(CacheTTL)
	for _, tc := range []struct {
		url  string
		want string
	}{
		{"/api/source/r/ns/app?network=alpha&at=25", pinnedSourceTTL.String()},
		{"/api/source/r/ns/app?network=alpha&at=25&file=a.gno&tokens=1", pinnedSourceTTL.String()},
		{"/api/source/r/ns/app?network=alpha", CacheTTL.String()},
		{"/api/realm/r/ns/app?network=alpha&at=25", CacheTTL.String()},
	} {
		got := c.ttlForRequest(httptest.NewRequest(http.MethodGet, tc.url, nil)).String()
		if got != tc.want {
			t.Errorf("%s: ttl %s, want %s", tc.url, got, tc.want)
		}
	}
}

// Through the real cache: a hit has to replay the immutable header and the
// validator, and answer If-None-Match itself, since the handler does not run.
func TestPinnedSourceThroughTheCache(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	h := WithResponseCache(NewResponseCache(CacheTTL), mux)

	get := func(inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/source/r/ns/app/v2?network=alpha&at=25", nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first := get("")
	second := get("")
	if second.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second read X-Cache = %q, want HIT", second.Header().Get("X-Cache"))
	}
	if second.Header().Get("Cache-Control") != first.Header().Get("Cache-Control") ||
		second.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Errorf("hit lost headers: %v vs %v", second.Header(), first.Header())
	}
	if rec := get(first.Header().Get("ETag")); rec.Code != http.StatusNotModified {
		t.Errorf("hit with If-None-Match: status %d, want 304", rec.Code)
	}
}
