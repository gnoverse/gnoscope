package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/andybalholm/brotli"
)

// The bundle, served as a file of its own.
//
// index.html carries the whole app inline: 115 KB of stylesheet and 1.46 MB of
// script, in one document served `no-cache` to every deep link. Measured on
// gnoscope.com on 2026-10-27, the script on / and on a realm page were
// byte-identical, and a cold visit to any URL paid for all of it again. A
// reader who opened three links in three tabs downloaded the same 1.5 MB three
// times, because there was nothing in the document a cache was allowed to keep.
//
// The source stays one file. A `<style data-bundle>` and a `<script
// data-bundle>` are cut out of it here, once per process, and served from
// /assets/ under a name carrying their content hash, with a year's
// `immutable`. The document keeps a `<link>` and a `<script src>` in their
// place and goes on being revalidated, so a deploy is picked up by the next
// request and costs a returning reader only the few KB that changed. There is
// still no build step and no asset directory: the split is a pure function of
// the embedded index.html.
//
// Cut at startup and not in the repository on purpose. The script is where
// every frontend change lands, and moving 30 000 lines into a new file would
// turn every open branch's diff into a conflict with this one.

// AssetPrefix is where the hashed bundle lives.
const AssetPrefix = "/assets/"

// asset is one file cut out of index.html.
type asset struct {
	path  string // AssetPrefix + name + "." + hash + ext
	ctype string
	raw   []byte
	gz    []byte
	etag  string

	// br is filled in the background. Brotli at its best ratio takes about
	// five seconds on the script, which is too long to hold startup for and
	// short enough that nobody notices gzip in the meantime.
	br atomic.Pointer[[]byte]
	// brDone closes once br is set, for tests that need to see it.
	brDone chan struct{}
}

// bundleRe finds the blocks to cut. The closing tag is the first one after
// the opening tag, which is exact rather than a guess: an inline script cannot
// contain `</script>` and an inline stylesheet cannot contain `</style>`, or
// the HTML parser would have ended it there too.
var bundleRe = regexp.MustCompile(`<(script|style) data-bundle="([a-z0-9-]+)\.(js|css)">`)

// splitBundle returns index.html with every data-bundle block replaced by a
// reference to its asset, and the assets themselves.
func splitBundle(index []byte) ([]byte, []*asset, error) {
	var out bytes.Buffer
	var assets []*asset
	rest := index
	for {
		m := bundleRe.FindSubmatchIndex(rest)
		if m == nil {
			out.Write(rest)
			break
		}
		tag, name, ext := string(rest[m[2]:m[3]]), string(rest[m[4]:m[5]]), string(rest[m[6]:m[7]])
		if (tag == "script") != (ext == "js") {
			return nil, nil, fmt.Errorf("bundle %s.%s is in a <%s>", name, ext, tag)
		}
		closing := []byte("</" + tag + ">")
		end := bytes.Index(rest[m[1]:], closing)
		if end < 0 {
			return nil, nil, fmt.Errorf("bundle %s.%s: no %s", name, ext, closing)
		}
		body := rest[m[1] : m[1]+end]

		sum := sha256.Sum256(body)
		a := &asset{
			path:   AssetPrefix + name + "." + hex.EncodeToString(sum[:6]) + "." + ext,
			raw:    body,
			etag:   `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`,
			brDone: make(chan struct{}),
		}
		out.Write(rest[:m[0]])
		if ext == "js" {
			a.ctype = "text/javascript; charset=utf-8"
			// Classic and parser-blocking, exactly like the inline script it
			// replaces: no defer, no async, so it still runs after the markup
			// above it and before anything below it.
			out.WriteString(`<script src="` + a.path + `"></script>`)
		} else {
			a.ctype = "text/css; charset=utf-8"
			out.WriteString(`<link rel="stylesheet" href="` + a.path + `">`)
		}
		rest = rest[m[1]+end+len(closing):]
		assets = append(assets, a)
	}
	if len(assets) == 0 {
		return nil, nil, fmt.Errorf("index.html has no data-bundle block")
	}
	return out.Bytes(), assets, nil
}

// bundle is the split, done once per process. It depends on nothing but the
// embedded index.html, so every Handler shares one, and the test binary pays
// for the five-second brotli pass once rather than once per test.
var bundle = sync.OnceValues(func() (*splitResult, error) {
	index, err := frontendFS.ReadFile("frontend/index.html")
	if err != nil {
		return nil, fmt.Errorf("read index.html: %w", err)
	}
	doc, assets, err := splitBundle(index)
	if err != nil {
		return nil, err
	}
	res := &splitResult{doc: doc, byPath: map[string]*asset{}}
	for _, a := range assets {
		a.gz = gzipBytes(a.raw)
		res.byPath[a.path] = a
		go func(a *asset) {
			if br := brotliBytes(a.raw, brotli.BestCompression); br != nil {
				a.br.Store(&br)
			}
			close(a.brDone)
		}(a)
	}
	return res, nil
})

type splitResult struct {
	doc    []byte
	byPath map[string]*asset
}

// serveAsset answers a request under AssetPrefix.
//
// A miss is a 404 and never the app. Falling through to index.html, the way
// every other unknown path does, would hand a document that still names an
// older build's hash a page of HTML with a 200 where it asked for script, and
// the browser would report a MIME error instead of the missing file.
func serveAsset(w http.ResponseWriter, r *http.Request, assets map[string]*asset) {
	a := assets[r.URL.Path]
	if a == nil {
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	// The name changes whenever a byte does, so a stored copy can never be
	// wrong, and `immutable` tells the browser not to revalidate it on reload
	// either.
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", a.etag)
	h.Set("Vary", "Accept-Encoding")
	h.Set("X-Content-Type-Options", "nosniff")
	if matchesETag(r.Header.Get("If-None-Match"), a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := a.raw
	switch br := a.br.Load(); {
	case br != nil && acceptsEncoding(r, "br"):
		h.Set("Content-Encoding", "br")
		body = *br
	case a.gz != nil && acceptsEncoding(r, "gzip"):
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

// brotliBytes is gzipBytes for brotli, nil on failure for the same reason.
func brotliBytes(b []byte, level int) []byte {
	var buf bytes.Buffer
	zw := brotli.NewWriterLevel(&buf, level)
	if _, err := zw.Write(b); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// acceptsEncoding reports whether the client listed an encoding. Same rule as
// the httpapi middleware: a bare name match, no q-values. Duplicated rather
// than imported because pkg/web must not depend on pkg/httpapi; it is the leaf
// the whole binary can serve without.
func acceptsEncoding(r *http.Request, enc string) bool {
	for _, v := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(v), ";")
		if strings.EqualFold(name, enc) {
			return true
		}
	}
	return false
}
