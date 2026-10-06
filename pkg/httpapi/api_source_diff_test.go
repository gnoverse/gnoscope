package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/indexer"
)

// seedSource's app/v2: 20 (app.gno, gone.gno), 25 (app.gno with A, é.gno),
// 26 failed ("broken"). app/v1 at 10.
func TestHandleSourceDiffDefaults(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)

	rec := sourceGET(t, api, "/api/source/r/ns/app/v2/diff?network=alpha")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var d sourceDiff
	decodeInto(t, rec, &d)
	// to = the current stamp, from = the successful submission before it.
	if d.To.Height != 25 || d.From == nil || d.From.Height != 20 || d.From.Path != "gno.land/r/ns/app/v2" {
		t.Fatalf("from %+v to %+v, want 20 -> 25", d.From, d.To)
	}
	status := map[string]string{}
	for _, f := range d.Files {
		status[f.Name] = f.Status
	}
	if status["app.gno"] != "modified" || status["gone.gno"] != "removed" || status["é.gno"] != "added" {
		t.Errorf("files = %v", status)
	}
	if !d.API.ExportsChanged || len(d.API.Added) != 1 || d.API.Added[0].Name != "A" {
		t.Errorf("api = %+v, want A added", d.API)
	}
	if d.Totals.FilesAdded != 1 || d.Totals.FilesRemoved != 1 || d.Totals.FilesModified != 1 {
		t.Errorf("totals = %+v", d.Totals)
	}
	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("a defaulted diff is immutable: %q", cc)
	}
}

func TestHandleSourceDiffPinnedAndFailed(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)

	// Both heights: immutable. 26 is the failed submission, served as such.
	rec := sourceGET(t, api, "/api/source/r/ns/app/v2/diff?network=alpha&from=25&to=26")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	var d sourceDiff
	decodeInto(t, rec, &d)
	if !d.To.Failed || d.From.Height != 25 {
		t.Errorf("from %+v to %+v", d.From, d.To)
	}
	// "broken" does not parse, so A is gone from what was sent.
	if len(d.API.Removed) != 1 || d.API.Removed[0].Name != "A" {
		t.Errorf("api = %+v", d.API)
	}
	etag := rec.Header().Get("ETag")
	if rec := sourceGET(t, api, "/api/source/r/ns/app/v2/diff?network=alpha&from=25&to=26", "If-None-Match", etag); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rec.Code)
	}
}

// A new version compares against the previous generation's latest source.
func TestHandleSourceDiffAcrossGenerations(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)
	rec := sourceGET(t, api, "/api/source/r/ns/app/v2/diff?network=alpha&from_path=gno.land/r/ns/app/v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var d sourceDiff
	decodeInto(t, rec, &d)
	if d.From == nil || d.From.Path != "gno.land/r/ns/app/v1" || d.From.Height != 10 || d.To.Height != 25 {
		t.Errorf("from %+v to %+v", d.From, d.To)
	}
	// The short form of from_path works too.
	if rec := sourceGET(t, api, "/api/source/r/ns/app/v2/diff?network=alpha&from_path=r/ns/app/v1"); rec.Code != http.StatusOK {
		t.Errorf("short from_path: %d", rec.Code)
	}
}

// The first publication at a path has nothing before it: from is null and
// every file is added.
func TestHandleSourceDiffFirstPublication(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)
	var d sourceDiff
	rec := sourceGET(t, api, "/api/source/r/ns/app/v1/diff?network=alpha")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	decodeInto(t, rec, &d)
	if d.From != nil || len(d.Files) != 1 || d.Files[0].Status != "added" {
		t.Errorf("first publication: from %+v files %+v", d.From, d.Files)
	}
	if !strings.Contains(rec.Body.String(), `"from":null`) {
		t.Errorf("from is not an explicit null: %s", rec.Body.String())
	}
}

func TestHandleSourceDiffErrors(t *testing.T) {
	api, db := newTestAPI(t)
	seedSource(t, db)
	for _, tc := range []struct {
		url  string
		want int
	}{
		{"/api/source/r/ns/app/v2/diff", http.StatusBadRequest},
		{"/api/source/r/ns/nope/diff?network=alpha", http.StatusNotFound},
		{"/api/source/r/ns/app/v2/diff?network=beta", http.StatusNotFound},
		{"/api/source/r/ns/app/v2/diff?network=alpha&to=99", http.StatusConflict},
		{"/api/source/r/ns/app/v2/diff?network=alpha&from=21", http.StatusConflict},
		{"/api/source/r/ns/app/v2/diff?network=alpha&from=x", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2/diff?network=alpha&to=-2", http.StatusBadRequest},
		{"/api/source/r/ns/app/v2/diff?network=alpha&from_path=r/ns/none", http.StatusNotFound},
	} {
		if rec := sourceGET(t, api, tc.url); rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", tc.url, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// A package whose last segment is diff keeps its URL.
func TestHandleSourceDiffNamedPackage(t *testing.T) {
	api, db := newTestAPI(t)
	deploySource(t, db, "alpha", "gno.land/p/ns/diff", "tx-d", 3, true, indexer.MemFile{Name: "diff.gno", Body: "package diff\n"})
	var m sourceManifest
	rec := sourceGET(t, api, "/api/source/p/ns/diff?network=alpha")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	decodeInto(t, rec, &m)
	if m.Path != "gno.land/p/ns/diff" {
		t.Errorf("manifest path %q", m.Path)
	}
	if rec := sourceGET(t, api, "/api/source/p/ns/diff/diff?network=alpha"); rec.Code != http.StatusOK {
		t.Errorf("its diff: %d", rec.Code)
	}
}

// What a diff carries is on-chain text: it goes out as JSON strings, never
// interpreted.
func TestHandleSourceDiffHostileContent(t *testing.T) {
	api, db := newTestAPI(t)
	evil := "package x\n// <script>alert(1)</script>\nfunc A() {}\n"
	deploySource(t, db, "alpha", "gno.land/r/ns/x", "tx1", 1, true, indexer.MemFile{Name: "x.gno", Body: "package x\n"})
	deploySource(t, db, "alpha", "gno.land/r/ns/x", "tx2", 2, true, indexer.MemFile{Name: "<img src=x>.gno", Body: evil})
	rec := sourceGET(t, api, "/api/source/r/ns/x/diff?network=alpha")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<script>") || strings.Contains(rec.Body.String(), "<img") {
		t.Errorf("markup is not escaped in the JSON: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
}
