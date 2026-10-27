package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gnoverse/gnoscope/pkg/registry"
	"github.com/gnoverse/gnoscope/pkg/tags"
)

// The document is re-served on every deep link and every cold tab, so it has
// to be small, compressed and revalidatable. Each of those has a silent failure
// mode: a missing Content-Encoding just sends the big body, a missing ETag just
// re-sends it, and a wrong Content-Length truncates the page with no error
// anywhere.
func TestIndexIsCompressedAndRevalidatable(t *testing.T) {
	h, err := Handler(Options{})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	get := func(path, enc string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if enc != "" {
			req.Header.Set("Accept-Encoding", enc)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	raw := get("/", "")

	t.Run("serves the raw body when nothing is accepted", func(t *testing.T) {
		if got := raw.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("Content-Encoding = %q", got)
		}
		body := raw.Body.String()
		if !strings.HasPrefix(body, "<!DOCTYPE html>") || !strings.Contains(body, `id="view-home"`) {
			t.Error("body is not the app's document")
		}
		// The bundle lives elsewhere now. 100 KB is twice what the markup
		// weighs, and a fifteenth of what the document weighed with the
		// script in it.
		if raw.Body.Len() > 100_000 {
			t.Errorf("the document is %d bytes: the bundle is back inline", raw.Body.Len())
		}
	})

	for _, enc := range []struct {
		name   string
		decode func(io.Reader) (io.Reader, error)
	}{
		{"gzip", func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }},
		{"br", func(r io.Reader) (io.Reader, error) { return brotli.NewReader(r), nil }},
	} {
		t.Run(enc.name+" when accepted, and the bytes round-trip", func(t *testing.T) {
			rec := get("/", enc.name)
			if got := rec.Header().Get("Content-Encoding"); got != enc.name {
				t.Fatalf("Content-Encoding = %q, want %s", got, enc.name)
			}
			if rec.Body.Len() >= raw.Body.Len()/2 {
				t.Errorf("compressed to %d bytes from %d: barely compressed at all", rec.Body.Len(), raw.Body.Len())
			}
			if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
				t.Errorf("Content-Length = %q but body is %d bytes: a browser would truncate the page", got, rec.Body.Len())
			}
			zr, err := enc.decode(bytes.NewReader(rec.Body.Bytes()))
			if err != nil {
				t.Fatalf("reader: %v", err)
			}
			out, err := io.ReadAll(zr)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !bytes.Equal(out, raw.Body.Bytes()) {
				t.Error("decoded body is not the document")
			}
		})
	}

	t.Run("prefers br when both are accepted", func(t *testing.T) {
		if got := get("/", "gzip, deflate, br, zstd").Header().Get("Content-Encoding"); got != "br" {
			t.Errorf("Content-Encoding = %q, want br", got)
		}
	})

	t.Run("revalidates with an ETag", func(t *testing.T) {
		etag := raw.Header().Get("ETag")
		if etag == "" {
			t.Fatal("no ETag")
		}

		for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("If-None-Match", inm)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusNotModified {
				t.Errorf("If-None-Match %q: status %d, want 304", inm, rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("If-None-Match %q: %d bytes of body on a 304", inm, rec.Body.Len())
			}
		}

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("If-None-Match", `"stale-from-an-older-build"`)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("a non-matching ETag got %d, want 200: a new build would never reach the reader", rec.Code)
		}
	})

	// A deep link is not a 404: the SPA routes on the URL, so every path that
	// is not an embedded file has to arrive at the app. Each with its own
	// title, and each loading the same bundle.
	t.Run("every deep link is the app, under its own title", func(t *testing.T) {
		js, css := bundleRefs(t, raw.Body.String())
		seen := map[string]string{}
		for _, path := range []string{"/txs", "/blocks", "/address/g1abc", "/realmish/r/x/y", "/realm/gno.land/r/demo/boards"} {
			rec := get(path, "")
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Errorf("%s: status %d", path, rec.Code)
				continue
			}
			if j, c := bundleRefs(t, body); j != js || c != css {
				t.Errorf("%s loads %s and %s, / loads %s and %s", path, j, c, js, css)
			}
			if prev, ok := seen[rec.Header().Get("ETag")]; ok {
				t.Errorf("%s and %s share an ETag", path, prev)
			}
			seen[rec.Header().Get("ETag")] = path
		}
	})
}

// bundleRefs returns the script and stylesheet a document loads from
// AssetPrefix, failing unless there is exactly one of each.
func bundleRefs(t *testing.T, doc string) (js, css string) {
	t.Helper()
	scripts := regexp.MustCompile(`<script src="(/assets/[^"]+)"></script>`).FindAllStringSubmatch(doc, -1)
	sheets := regexp.MustCompile(`<link rel="stylesheet" href="(/assets/[^"]+)">`).FindAllStringSubmatch(doc, -1)
	if len(scripts) != 1 || len(sheets) != 1 {
		t.Fatalf("the document loads %d bundled scripts and %d stylesheets, want one of each", len(scripts), len(sheets))
	}
	return scripts[0][1], sheets[0][1]
}

// inlineBlock is a data-bundle block's body as written in index.html.
func inlineBlock(t *testing.T, tag, name string) []byte {
	t.Helper()
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	open := []byte(`<` + tag + ` data-bundle="` + name + `">`)
	i := bytes.Index(index, open)
	if i < 0 {
		t.Fatalf("no %s in index.html", open)
	}
	rest := index[i+len(open):]
	return rest[:bytes.Index(rest, []byte("</"+tag+">"))]
}

// The bundle is the 1.5 MB that used to ride inside every document. It is only
// worth splitting out if a browser is allowed to keep it, and only safe to let
// a browser keep it forever if its name changes whenever a byte of it does.
func TestBundleIsAnImmutableAsset(t *testing.T) {
	h, err := Handler(Options{})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	doc := httptest.NewRecorder()
	h(doc, httptest.NewRequest("GET", "/", nil))
	js, css := bundleRefs(t, doc.Body.String())

	for _, c := range []struct {
		path, tag, name, ctype string
	}{
		{js, "script", "app.js", "text/javascript"},
		{css, "style", "app.css", "text/css"},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := inlineBlock(t, c.tag, c.name)
			if bytes.Contains(doc.Body.Bytes(), want[:200]) {
				t.Error("the document still carries the block inline")
			}

			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest("GET", c.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status %d", c.path, rec.Code)
			}
			if !bytes.Equal(rec.Body.Bytes(), want) {
				t.Errorf("%s is not the block index.html carries", c.path)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, c.ctype) {
				t.Errorf("Content-Type = %q, want %s", got, c.ctype)
			}
			if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") || !strings.Contains(got, "max-age=31536000") {
				t.Errorf("Cache-Control = %q, want a year, immutable", got)
			}

			// The name is the content: a different body is a different URL.
			sum := sha256.Sum256(want)
			if !strings.Contains(c.path, hex.EncodeToString(sum[:6])) {
				t.Errorf("%s does not carry the hash of its body", c.path)
			}

			req := httptest.NewRequest("GET", c.path, nil)
			req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
			rec304 := httptest.NewRecorder()
			h(rec304, req)
			if rec304.Code != http.StatusNotModified {
				t.Errorf("revalidating %s: status %d, want 304", c.path, rec304.Code)
			}

			req = httptest.NewRequest("GET", c.path, nil)
			req.Header.Set("Accept-Encoding", "gzip")
			gz := httptest.NewRecorder()
			h(gz, req)
			gzLen := gz.Body.Len()
			zr, err := gzip.NewReader(gz.Body)
			if err != nil {
				t.Fatalf("gzip: %v", err)
			}
			if out, _ := io.ReadAll(zr); !bytes.Equal(out, want) {
				t.Error("gzip does not round-trip")
			}

			// Brotli is computed in the background; wait for it here.
			b, _ := bundle()
			<-b.byPath[c.path].brDone
			req = httptest.NewRequest("GET", c.path, nil)
			req.Header.Set("Accept-Encoding", "br")
			br := httptest.NewRecorder()
			h(br, req)
			if got := br.Header().Get("Content-Encoding"); got != "br" {
				t.Fatalf("Content-Encoding = %q, want br", got)
			}
			if br.Body.Len() >= gzLen {
				t.Errorf("br is %d bytes and gzip %d: brotli is not paying for itself", br.Body.Len(), gzLen)
			}
			if out, _ := io.ReadAll(brotli.NewReader(br.Body)); !bytes.Equal(out, want) {
				t.Error("br does not round-trip")
			}
		})
	}

	// A document from the previous build names a hash this one does not have.
	// It must get a plain 404 and not the app: HTML with a 200 where script
	// was asked for is a MIME error in the console and a page that looks hung.
	t.Run("an unknown hash is a 404, never the app", func(t *testing.T) {
		for _, path := range []string{"/assets/app.000000000000.js", "/assets/", "/assets/app.js"} {
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: status %d, want 404", path, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
				t.Errorf("%s: answered with the app", path)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s: Cache-Control = %q, want no-store so a miss mid-deploy is not remembered", path, got)
			}
		}
	})

	// Stays where the inline script was, at the foot of the body, so it still
	// runs after the markup it draws into, and still parser-blocking.
	t.Run("the script loads where the inline one ran", func(t *testing.T) {
		body := doc.Body.String()
		at := strings.Index(body, `<script src="`+js+`">`)
		if footer := strings.Index(body, "</footer>"); at < footer {
			t.Errorf("the bundle loads at %d, before </footer> at %d", at, footer)
		}
		if tag := body[at : at+strings.Index(body[at:], ">")]; strings.Contains(tag, "defer") || strings.Contains(tag, "async") {
			t.Error("the bundle is deferred: the inline script it replaces was not")
		}
		if link := strings.Index(body, `href="`+css+`"`); link > strings.Index(body, "</head>") {
			t.Error("the stylesheet is not in <head>")
		}
	})
}

// A one-byte change to the script has to change its URL and therefore the
// document's ETag, or a reader revalidating the document is told nothing
// changed and runs the old bundle forever.
func TestBundleHashFollowsTheContent(t *testing.T) {
	page := func(js string) string {
		return "<html><head><style data-bundle=\"app.css\">a{}</style></head><body><script data-bundle=\"app.js\">" + js + "</script></body></html>"
	}
	docA, assetsA, err := splitBundle([]byte(page("var a = 1;")))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	docB, assetsB, err := splitBundle([]byte(page("var a = 2;")))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(assetsA) != 2 || len(assetsB) != 2 {
		t.Fatalf("got %d and %d assets, want 2", len(assetsA), len(assetsB))
	}
	if assetsA[0].path != assetsB[0].path {
		t.Error("the stylesheet did not change and its URL did")
	}
	if assetsA[1].path == assetsB[1].path {
		t.Error("the script changed and its URL did not")
	}
	if bytes.Equal(docA, docB) {
		t.Error("the script changed and the document did not, so its ETag would not either")
	}
	if strings.Contains(string(docA), "var a") {
		t.Error("the script is still inline")
	}

	for _, bad := range []string{
		"<html></html>",
		`<script data-bundle="app.js">never closed`,
		`<style data-bundle="app.js">a{}</style>`,
	} {
		if _, _, err := splitBundle([]byte(bad)); err == nil {
			t.Errorf("splitBundle(%q) succeeded", bad)
		}
	}
}

// Two `function` declarations sharing a name are not an error in JavaScript.
// The later one wins, silently, for every caller including the earlier one's
// own — so a function written for one page starts answering calls made by
// another, with a different signature, and the first symptom is a TypeError
// deep inside unrelated rendering.
//
// That is not hypothetical. `paramValueEl` was written twice: once by the
// /params page for a parameter object, once by the govdao proposal diff for a
// bare string. Each pull request was green on its own base. They merged eight
// seconds apart, and /params was dead on main from that moment, because the
// diff's one-argument version was hoisted over the page's own.
//
// Nothing else catches this. The frontend is one file in one global scope with
// no build step, no module boundary and no bundler to warn; the Go tests never
// execute the JavaScript, and a browser test only finds it if a test happens to
// exercise the losing caller. So the guard is textual and deliberately cheap.
//
// `const` and `let` need no guard: redeclaring either is a SyntaxError, which
// takes the whole script down and cannot reach main unnoticed.
func TestFrontendHasNoDuplicateTopLevelDeclarations(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Anchored at column zero on purpose: that is what "top level" means in
	// this file, and it keeps every nested closure and method out.
	decl := regexp.MustCompile(`(?m)^(?:function|var)\s+([A-Za-z0-9_$]+)`)
	seen := map[string]int{}
	for _, m := range decl.FindAllSubmatch(index, -1) {
		seen[string(m[1])]++
	}

	var dupes []string
	for name, n := range seen {
		if n > 1 {
			dupes = append(dupes, fmt.Sprintf("%s (%d declarations)", name, n))
		}
	}
	sort.Strings(dupes)
	if len(dupes) > 0 {
		t.Errorf("declared more than once at the top level of index.html, so the last one silently wins everywhere: %s",
			strings.Join(dupes, ", "))
	}
}

// Analytics is off by default and on by flag, and the two states are not the
// same document. Three things go wrong quietly here: a tag that ships to every
// deployment because it was written into index.html instead of injected, a URL
// dropped straight into an attribute unescaped, and a body that changes while
// the ETag does not, which serves the old page from cache until the reader
// hard-reloads.
func TestAnalyticsScript(t *testing.T) {
	const sa = "https://scripts.simpleanalyticscdn.com/latest.js"

	body := func(t *testing.T, opts Options) string {
		t.Helper()
		h, err := Handler(opts)
		if err != nil {
			t.Fatalf("Handler: %v", err)
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/", nil))
		return rec.Body.String()
	}

	t.Run("off by default", func(t *testing.T) {
		if got := body(t, Options{}); strings.Contains(got, "simpleanalytics") {
			t.Error("the default build ships an analytics tag")
		}
		index, err := Index()
		if err != nil {
			t.Fatalf("Index: %v", err)
		}
		if bytes.Contains(index, []byte("simpleanalyticscdn")) {
			t.Error("index.html carries a hardcoded analytics tag, so every deployment reports to one account")
		}
	})

	t.Run("injected into the head when configured", func(t *testing.T) {
		got := body(t, Options{AnalyticsScript: sa})
		tag := `<script async src="` + sa + `"></script>`
		i := strings.Index(got, tag)
		if i < 0 {
			t.Fatalf("no %s in the served page", tag)
		}
		if head := strings.Index(got, "</head>"); i > head {
			t.Errorf("tag at %d is after </head> at %d", i, head)
		}
	})

	t.Run("the ETag follows the body", func(t *testing.T) {
		etag := func(opts Options) string {
			h, err := Handler(opts)
			if err != nil {
				t.Fatalf("Handler: %v", err)
			}
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest("GET", "/", nil))
			return rec.Header().Get("ETag")
		}
		if on, off := etag(Options{AnalyticsScript: sa}), etag(Options{}); on == off {
			t.Errorf("same ETag %s with and without analytics, so caches keep serving the old page", on)
		}
	})

	t.Run("a URL that cannot go in an attribute is a startup error", func(t *testing.T) {
		for _, src := range []string{
			"javascript:alert(1)",
			"latest.js",
			"//scripts.simpleanalyticscdn.com/latest.js",
			"https://x.example/a.js\" onload=\"alert(1)",
		} {
			t.Run(src, func(t *testing.T) {
				h, err := Handler(Options{AnalyticsScript: src})
				if err != nil {
					return // rejected outright, which is the preferred outcome
				}
				// Accepted, so it must at least be inert in the attribute it
				// landed in.
				rec := httptest.NewRecorder()
				h(rec, httptest.NewRequest("GET", "/", nil))
				if strings.Contains(rec.Body.String(), `onload="alert(1)`) {
					t.Error("the URL broke out of the src attribute")
				}
			})
		}
	})
}

// The rail is static HTML and the NAV table beside it is JavaScript, and
// they describe the same navigation twice: the markup so the chrome paints
// without waiting on two render-blocking CDN scripts, the table so route()
// knows which parent to light up and pageNav() can build each section's pill
// strip.
//
// Two copies means one can go stale, and the stale one fails quietly. Adding
// a rail entry and forgetting the table gives a page that navigates fine and
// has no section strip, which reads as a page that is simply not in a
// section. Adding it to the table only gives a strip linking to a rail entry
// that never highlights. Neither throws, neither shows up in a browser test
// that only checks the page renders.
//
// So: same ids, same order, same paths. Parsed textually, which is enough
// because both sides are written by hand in a fixed shape and a shape change
// fails here loudly rather than silently.
func TestRailMatchesNavTable(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	html := string(index)

	rail := between(t, html, "<nav>", "</nav>")
	navTable := between(t, html, "const NAV = [", "\n];")

	// id and path, in document order, from each side. The rail's <a> carries
	// both as `href="<path>"` and `id="nav-<id>"`; NAV's entries carry them as
	// `id: '<id>'` and `path: '<path>'`.
	//
	// The rail used to say `onclick="navigate('<path>')"` and this pattern
	// matched that. Links are real anchors now, so the path lives in href: the
	// destination is the truth and the click handler is the optimisation over
	// it, rather than the other way round.
	railRe := regexp.MustCompile(`href="([^"]+)" id="nav-([A-Za-z0-9-]+)"`)
	tableRe := regexp.MustCompile(`id: '([A-Za-z0-9-]+)',[^\n]*?path: '([^']+)'`)

	type entry struct{ id, path string }
	var fromRail, fromTable []entry
	for _, m := range railRe.FindAllStringSubmatch(rail, -1) {
		fromRail = append(fromRail, entry{id: m[2], path: m[1]})
	}
	for _, m := range tableRe.FindAllStringSubmatch(navTable, -1) {
		fromTable = append(fromTable, entry{id: m[1], path: m[2]})
	}

	if len(fromRail) == 0 || len(fromTable) == 0 {
		t.Fatalf("parsed %d rail entries and %d table entries — the shape of one of them changed",
			len(fromRail), len(fromTable))
	}
	if len(fromRail) != len(fromTable) {
		t.Fatalf("the rail has %d entries and NAV has %d:\nrail:  %v\ntable: %v",
			len(fromRail), len(fromTable), fromRail, fromTable)
	}
	for i := range fromRail {
		if fromRail[i] != fromTable[i] {
			t.Errorf("entry %d: the rail says %+v, NAV says %+v", i, fromRail[i], fromTable[i])
		}
	}
}

// The frontend colours each code tag from TAG_COLORS and orders chips by its
// keys, so it is a second copy of the rule table's names and order. A tag the
// table gained and this map did not is drawn grey, sorted nowhere, and missing
// from the code map's legend: quiet, so it is held here.
func TestTagColorsFollowTheRules(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	table := between(t, string(index), "const TAG_COLORS = {", "\n};")
	re := regexp.MustCompile(`(?:^|[\s,])'?([a-z][a-z-]*)'?: '(#[0-9a-f]{6})'`)
	var got []string
	seen := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(table, -1) {
		got = append(got, m[1])
		if prev, ok := seen[m[2]]; ok {
			t.Errorf("%s and %s share the colour %s", prev, m[1], m[2])
		}
		seen[m[2]] = m[1]
	}
	var want []string
	for _, r := range tags.Rules {
		want = append(want, r.Tag)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("TAG_COLORS has\n  %v\ntags.Rules has\n  %v", got, want)
	}
}

// The apps section's rail children are the app categories, and they are the
// only rail entries that are not a view of their own: every one of them
// renders #view-apps filtered by the last path segment. So the set has to be
// exactly registry.Categories, in that order.
//
// Drift here is silent in the way TestRailMatchesNavTable describes and one
// step worse. A category in apps.json with no rail entry is a bucket nothing
// links to, reachable only by typing its URL; a rail entry with no category
// behind it is a link to a page that says "nothing here yet" forever. Neither
// throws, and neither shows up in a test that only checks the page renders.
//
// Checked against the rail alone: TestRailMatchesNavTable already pins the
// rail and the NAV table to each other, so one of them is enough. The other
// direction, apps.json using only these, is validateApps in pkg/registry.
func TestNavAppsMatchesCategories(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	rail := between(t, string(index), "<nav>", "</nav>")

	re := regexp.MustCompile(`href="/apps/([a-z]+)" id="nav-apps-([a-z]+)"`)
	var got []string
	for _, m := range re.FindAllStringSubmatch(rail, -1) {
		if m[1] != m[2] {
			t.Errorf("rail entry for /apps/%s has id nav-apps-%s: the path and the id must name the same category", m[1], m[2])
		}
		got = append(got, m[1])
	}

	if len(got) != len(registry.Categories) {
		t.Fatalf("the rail has %d app categories and registry.Categories has %d:\nrail:     %v\nregistry: %v",
			len(got), len(registry.Categories), got, registry.Categories)
	}
	for i := range got {
		if got[i] != registry.Categories[i] {
			t.Errorf("category %d: the rail says %q, registry.Categories says %q", i, got[i], registry.Categories[i])
		}
	}
}

func between(t *testing.T, s, openTag, closeTag string) string {
	t.Helper()
	i := strings.Index(s, openTag)
	if i < 0 {
		t.Fatalf("index.html has no %q", openTag)
	}
	rest := s[i+len(openTag):]
	j := strings.Index(rest, closeTag)
	if j < 0 {
		t.Fatalf("index.html has no %q after %q", closeTag, openTag)
	}
	return rest[:j]
}

// The icon sprite is a block of <symbol> definitions at the top of
// index.html, referenced two ways: <use href="#i-name"> in static markup, and
// icon('name') from the script, which builds the same <use> at runtime.
// Nothing links a symbol to either one: an orphaned symbol ships to every
// reader forever, and a reference to a symbol that is not there renders as an
// empty 15px box with no error anywhere.
//
// Both happen for the same reason, which is that the sprite is edited from
// the other end of a 13,000-line file. Nesting the nav orphaned nine of them
// in one commit: every icon that belonged to an entry which became a child,
// since children are indented text and carry none.
//
// The icon('name') form is why the argument has to stay a string literal:
// a computed name is invisible to this grep, and its symbol would then read
// as an orphan and fail the build.
func TestFrontendSpriteHasNoOrphansOrDanglingRefs(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	html := string(index)

	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`<symbol id="(i-[a-z0-9-]+)"`).FindAllStringSubmatch(html, -1) {
		defined[m[1]] = true
	}
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`href="#(i-[a-z0-9-]+)"`).FindAllStringSubmatch(html, -1) {
		used[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`\bicon\('([a-z0-9-]+)'\)`).FindAllStringSubmatch(html, -1) {
		used["i-"+m[1]] = true
	}
	if len(defined) == 0 || len(used) == 0 {
		t.Fatalf("found %d symbols and %d references — the shape of the sprite changed", len(defined), len(used))
	}

	var orphans, dangling []string
	for name := range defined {
		if !used[name] {
			orphans = append(orphans, name)
		}
	}
	for name := range used {
		if !defined[name] {
			dangling = append(dangling, name)
		}
	}
	sort.Strings(orphans)
	sort.Strings(dangling)

	if len(orphans) > 0 {
		t.Errorf("defined but never referenced, so they are dead bytes in every response: %s",
			strings.Join(orphans, ", "))
	}
	if len(dangling) > 0 {
		t.Errorf("referenced but not defined, so they render as an empty box: %s",
			strings.Join(dangling, ", "))
	}
}

// The screenshot feature flag is injected into the document rather than
// fetched, because the first row is drawn before /api/version comes back. Off
// by default, and off means the served bytes are the embedded file unchanged.
func TestFeatureFlagIsInjectedOnlyWhenOn(t *testing.T) {
	t.Run("off leaves the body untouched", func(t *testing.T) {
		h, err := Handler(Options{})
		if err != nil {
			t.Fatalf("Handler: %v", err)
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/", nil))
		// The assignment, not the reads: the application script reads
		// window.FEATURES defensively and that text is in index.html either way.
		if bytes.Contains(rec.Body.Bytes(), []byte("window.FEATURES={")) {
			t.Error("FEATURES was injected with the feature off")
		}
	})

	t.Run("on declares the flag before the app script runs", func(t *testing.T) {
		h, err := Handler(Options{Shots: true})
		if err != nil {
			t.Fatalf("Handler: %v", err)
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/", nil))
		body := rec.Body.Bytes()
		// A prefix, not the whole literal: the object carries more than one
		// flag now, and pinning its full text makes every new flag look like a
		// regression in this test.
		want := []byte("window.FEATURES={shots:true,")
		i := bytes.Index(body, want)
		if i < 0 {
			t.Fatalf("no %q in the served body", want)
		}
		// Before </head>, and therefore before the application script at the
		// foot of the body. A flag that arrives after the code reading it is
		// the same as no flag at all.
		if head := bytes.Index(body, []byte("</head>")); i > head {
			t.Fatalf("FEATURES at %d is after </head> at %d", i, head)
		}
	})

	t.Run("the default network is injected on its own", func(t *testing.T) {
		// The chain a fresh visitor lands on has to be known before the first
		// request for data goes out, so it rides the same injection as Shots
		// rather than being fetched. An instance with no capture service still
		// gets the block.
		h, err := Handler(Options{DefaultNetwork: "onyx"})
		if err != nil {
			t.Fatalf("Handler: %v", err)
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/", nil))
		body := rec.Body.Bytes()
		want := []byte(`defaultNetwork:"onyx"`)
		i := bytes.Index(body, want)
		if i < 0 {
			t.Fatalf("no %q in the served body", want)
		}
		if head := bytes.Index(body, []byte("</head>")); i > head {
			t.Fatalf("the default network at %d is after </head> at %d", i, head)
		}
	})

	t.Run("nothing configured injects nothing", func(t *testing.T) {
		// An instance with no networks and no shots serves the embedded file
		// byte for byte, so the page keeps its own fallback rather than being
		// told the default is the empty string.
		h, err := Handler(Options{})
		if err != nil {
			t.Fatalf("Handler: %v", err)
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/", nil))
		if bytes.Contains(rec.Body.Bytes(), []byte("window.FEATURES={")) {
			t.Error("FEATURES was injected with nothing to declare")
		}
	})

	t.Run("the ETag moves with the flag", func(t *testing.T) {
		// The body differs, so a reader holding the other build's tag has to be
		// told. Serving the same ETag for two different bodies is how a browser
		// keeps a stale page forever.
		off, _ := Handler(Options{})
		on, _ := Handler(Options{Shots: true})
		recOff, recOn := httptest.NewRecorder(), httptest.NewRecorder()
		off(recOff, httptest.NewRequest("GET", "/", nil))
		on(recOn, httptest.NewRequest("GET", "/", nil))
		if recOff.Header().Get("ETag") == recOn.Header().Get("ETag") {
			t.Fatal("both builds serve the same ETag")
		}
	})
}

// TestNoUnguardedShotElAppend keeps a returns-null helper out of a call that
// cannot take null.
//
// shotEl returns nil when no capture service is configured (FEATURES.shots
// off), and `appendChild(null)` throws. One direct
// `header.appendChild(shotEl(...))` therefore took the entire realm page down
// on any deployment without screenshots: the exception escaped mid-render, so
// there was no header and no tab strip at all, on every realm. It was
// invisible wherever shots happen to be enabled, which is why a grep guard is
// worth more here than a browser test.
//
// Every legitimate caller passes the result to el(), which skips a null child,
// or assigns it and checks.
func TestNoUnguardedShotElAppend(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if bytes.Contains(index, []byte("appendChild(shotEl(")) {
		t.Error("appendChild(shotEl(...)) appends a value that is null when screenshots are off, " +
			"which throws and takes the whole page down; assign it and guard, or pass it through el()")
	}
}

// Every persisted setting has to survive the rename, and keep surviving it.
//
// The 2026-09-28 rename moved the localStorage keys from `mygnoscan-*` to
// `gnoscope-*`. Renaming a persistence key is silent data loss: nothing errors,
// the reader simply finds their chain back on "all networks" and their rail wide
// open, and cannot tell a rename from a bug. The pre-paint script migrates them,
// and this test is what keeps that list complete.
//
// It fails on a key the app reads but the migration does not carry, which is the
// shape of the bug: adding a setting is a one-line edit somewhere in 23,000
// lines, and nothing else would notice that it was left behind.
func TestEveryPersistedSettingIsMigratedFromTheOldName(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	html := string(index)

	// What the migration carries: the literal list in the pre-paint loop.
	migrated := map[string]bool{}
	for _, m := range regexp.MustCompile(`for \(const key of \[([^\]]*)\]\)`).FindAllStringSubmatch(html, -1) {
		for _, k := range regexp.MustCompile(`'([a-z-]+)'`).FindAllStringSubmatch(m[1], -1) {
			migrated[k[1]] = true
		}
	}
	if len(migrated) == 0 {
		t.Fatal("no migration loop found: the rename carried the settings and something removed it")
	}

	// It has to move a key from somewhere else.
	//
	// The list being complete is not enough, and that gap is not theoretical:
	// on 2026-09-28 a second pass of the rename script rewrote the loop's own
	// source prefix, turning `mygnoscan-* -> gnoscope-*` into
	// `gnoscope-* -> gnoscope-*`. Every check above still passed. The migration
	// read the key it was about to write, found it, wrote it back, and every
	// reader upgrading from the old name lost their chain and their rail
	// exactly as if there were no migration at all.
	//
	// So: assert the two prefixes differ. A migration that reads what it writes
	// is not a migration, it is a very quiet no-op.
	m := regexp.MustCompile(`const from = '([a-z-]+)' \+ key, to = '([a-z-]+)' \+ key`).FindStringSubmatch(html)
	if m == nil {
		t.Fatal("the migration no longer maps one key prefix to another; if the shape changed, re-pin it here")
	}
	if m[1] == m[2] {
		t.Errorf("the migration reads and writes the same prefix %q: it is a no-op, and every "+
			"reader upgrading from the old name loses the setting silently", m[1])
	}
	if m[2] != "gnoscope-" {
		t.Errorf("the migration writes %q, but the app reads gnoscope-* keys", m[2])
	}

	// Keys that postdate the rename, so there is no old name to carry over.
	// Explicit rather than inferred: an exemption should be a decision someone
	// wrote down, not a key that happened not to match a pattern.
	bornAfterTheRename := map[string]bool{
		"rename-notice-dismissed": true,
	}

	used := regexp.MustCompile(`localStorage\.(?:get|set|remove)Item\('([^']+)'`)
	seen := map[string]bool{}
	for _, m := range used.FindAllStringSubmatch(html, -1) {
		key := m[1]
		if seen[key] || bornAfterTheRename[key] {
			continue
		}
		seen[key] = true
		short, ok := strings.CutPrefix(key, "gnoscope-")
		if !ok {
			t.Errorf("localStorage key %q is neither gnoscope-prefixed nor listed as post-rename", key)
			continue
		}
		if !migrated[short] {
			t.Errorf("localStorage key %q is read by the app but not carried by the migration: "+
				"a reader upgrading from mygnoscan loses it silently", key)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no gnoscope-prefixed localStorage keys found; the regexp or the code shape moved")
	}
}

// The rename notice is a dated announcement, and the date is what stops it
// becoming permanent furniture. A banner with no expiry is a banner still up in
// two years, because removing it is nobody's task.
func TestRenameNoticeExpires(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	html := string(index)

	if !strings.Contains(html, `id="rename-notice"`) {
		t.Fatal("the rename notice is gone; if that is deliberate, delete this test in the same commit")
	}
	m := regexp.MustCompile(`RENAME_NOTICE_UNTIL = Date\.parse\('([0-9T:-]+Z)'\)`).FindStringSubmatch(html)
	if m == nil {
		t.Fatal("the notice has no expiry constant")
	}
	until, err := time.Parse(time.RFC3339, m[1])
	if err != nil {
		t.Fatalf("unparseable expiry %q: %v", m[1], err)
	}
	// Renamed 2026-09-28. Anything beyond a bit over a year is not an
	// announcement any more, it is a permanent fixture that happens to compile.
	if latest := time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC); until.After(latest) {
		t.Errorf("the notice runs until %s, past %s: that is furniture, not an announcement",
			until.Format("2006-01-02"), latest.Format("2006-01-02"))
	}
}
