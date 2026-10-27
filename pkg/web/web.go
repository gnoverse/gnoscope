// Package web serves the single-file frontend embedded in the binary.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

//go:embed frontend
var frontendFS embed.FS

// Options configures what gets served.
type Options struct {
	// AnalyticsScript is the URL of a third-party analytics script to load on
	// every page, or "" to load none. Off by default: an explorer that phones
	// a third party home without its operator having asked for it is not a
	// default anyone should inherit by upgrading.
	//
	// The deployment at gnoscope.com passes Simple Analytics'
	// https://scripts.simpleanalyticscdn.com/latest.js, which sets no cookie
	// and stores nothing on the device. Any provider serving a single
	// self-contained script works the same way.
	AnalyticsScript string

	// Shots is true when a capture service is configured, and it is injected
	// into the page rather than fetched because the answer has to be known
	// before the first row is drawn. /api/version arrives after the first
	// paint, so gating on it would mean a listing rendering without pictures
	// and then growing a column, which is worse than either outcome on its
	// own.
	Shots bool

	// DefaultNetwork is the chain a visitor with no stored choice lands on.
	//
	// Injected for the same reason as Shots: the page asks for data before
	// /api/networks could answer, so a default resolved from that fetch
	// arrives after the first load has already picked a chain. Empty leaves
	// the frontend's own fallback in place.
	DefaultNetwork string

	// WidgetFrameAncestors is the `frame-ancestors` source list sent with
	// /widget, and only with /widget.
	//
	// Configurable at runtime, never compiled in. A widget exists to be put in
	// somebody else's page, and the host cannot find out whether it is allowed
	// to: cross-origin, a refused frame still fires `load`, so what a viewer
	// gets is the browser's grey error page covering the tile that would have
	// named the tool. moul/ghoblin#63 is the precedent — a compiled-in
	// `frame-ancestors 'none'` cost that tool a dead tile on a wall for a week,
	// and nobody could tell from either side why.
	//
	// The default is `*`, which is also why this is sent at all rather than
	// left absent. An absent directive permits framing too, but it permits it
	// by saying nothing, so anything upstream that adds an `X-Frame-Options`
	// to every response — a reverse proxy's security-header block is the usual
	// one — silently takes the widget with it. A present `frame-ancestors`
	// overrides `X-Frame-Options` in every browser that implements both, so
	// declaring permission is the only form of permission that survives a
	// proxy nobody remembers configuring.
	//
	// Permissive is the right default on a read-only public explorer: every
	// byte this page can show is already at gnoscope.com for anyone who asks.
	// Narrowing it is a line in the deployment's own config, where the
	// origins being named are the operator's business and not published to
	// everyone who curls the site. Empty sends no header at all.
	WidgetFrameAncestors string
}

// WidgetPath is the one route that carries a framing policy. A constant
// because two places have to agree on it: the handler that sets the header and
// the test that checks no other route gets one.
const WidgetPath = "/widget"

// DefaultWidgetFrameAncestors is what /widget sends when nothing says
// otherwise. See Options.WidgetFrameAncestors for why it is permissive and why
// it is sent rather than omitted.
const DefaultWidgetFrameAncestors = "*"

// validateFrameAncestors refuses a source list that could do more than name
// origins.
//
// The value goes straight into a response header, so a newline is header
// injection and a `;` or a `,` is a second directive or a second policy
// smuggled into a field that should only ever hold hosts. Rejected at startup
// rather than at request time, the same way withAnalytics refuses a bad script
// URL: a misconfigured flag should stop the process, not ship a broken policy
// to every reader.
func validateFrameAncestors(v string) error {
	if v == "" {
		return nil
	}
	for _, r := range v {
		switch {
		case r == ';' || r == ',':
			return fmt.Errorf("frame-ancestors %q: %q would start another directive or policy", v, r)
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("frame-ancestors %q: contains a control character", v)
		case r > 0x7e:
			return fmt.Errorf("frame-ancestors %q: contains a non-ASCII character", v)
		}
	}
	return nil
}

// Handler serves the frontend: the hashed bundle under AssetPrefix, a static
// file when the path names one, and the app's document for everything else.
//
// The fallback is what makes deep links work. The frontend is a single-page app
// that routes on the URL, so /realm/gno.land/r/demo/boards has to arrive at the
// same app as /; serving a 404 there would break every link anyone shares.
//
// The static attempt comes first and is an existence check rather than a path
// prefix: anything actually embedded is served as itself, and only genuine
// misses fall through to the app.
func Handler(opts Options) (http.HandlerFunc, error) {
	sub, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		return nil, fmt.Errorf("frontend fs: %w", err)
	}
	static := http.FileServer(http.FS(sub))
	split, err := bundle()
	if err != nil {
		return nil, err
	}
	index := split.doc
	rail := railTitles(index)

	// Before the ETag below, so it describes what is actually sent: turning
	// analytics on changes the body, and a reader holding the previous build's
	// tag has to be told so.
	index, err = withAnalytics(index, opts.AnalyticsScript)
	if err != nil {
		return nil, err
	}
	index, err = withFeatures(index, opts)
	if err != nil {
		return nil, err
	}

	// The document is a template with one hole, where index.html says
	// <title>gnoscope</title>: each request fills it with its own title and
	// link preview. Near the top of <head> on purpose, since a crawler that
	// reads only the first few KB of a page still finds its card.
	const placeholder = "<title>gnoscope</title>\n"
	hole := bytes.Index(index, []byte(placeholder))
	if hole < 0 {
		return nil, fmt.Errorf("no %q in index.html", strings.TrimSpace(placeholder))
	}
	index = append(index[:hole:hole], index[hole+len(placeholder):]...)

	// The template never changes for the life of the process, and it names
	// the bundle by hash, so this tag moves whenever a byte of the app does.
	// `no-cache` is deliberate rather than a max-age: it means "revalidate",
	// so a new build is picked up on the next request, while an unchanged one
	// costs a 304 with no body.
	sum := sha256.Sum256(index)
	etag := `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`

	if err := validateFrameAncestors(opts.WidgetFrameAncestors); err != nil {
		return nil, err
	}
	widgetCSP := ""
	if opts.WidgetFrameAncestors != "" {
		widgetCSP = "frame-ancestors " + opts.WidgetFrameAncestors
	}

	return func(w http.ResponseWriter, r *http.Request) {
		// One route, named, before anything else writes a header. Everything
		// else here stays exactly as unframeable as whatever is in front of
		// it: a policy that leaked onto every path would be a site-wide
		// decision taken by a widget.
		if widgetCSP != "" && r.URL.Path == WidgetPath {
			w.Header().Set("Content-Security-Policy", widgetCSP)
		}
		if strings.HasPrefix(r.URL.Path, AssetPrefix) {
			serveAsset(w, r, split.byPath)
			return
		}
		if r.URL.Path != "/" {
			if f, err := sub.Open(r.URL.Path[1:]); err == nil {
				f.Close()
				static.ServeHTTP(w, r)
				return
			}
		}
		origin := requestOrigin(r)
		title := titleOf(pageThing(r.URL.Path, rail))
		var tags []byte
		switch {
		// A realm's preview is its capture, and a cartography link's is that
		// drawing. Every other route gets a card with no picture.
		case realmPathFromURL(r.URL.Path) != "":
			tags = ogTags(origin, realmPathFromURL(r.URL.Path), r, opts.Shots)
		case r.URL.Path == "/cartography":
			v, _ := cartographyViewFromRequest(r)
			tags = cartographyTags(origin, v, r)
		default:
			tags = pageTags(origin, title, r)
		}
		serveDocument(w, r, index, hole, etag, append([]byte(titleTag(title)), tags...))
	}, nil
}

// serveDocument writes the template with a request's head block in its hole.
//
// The ETag is derived from the template's plus the block, so it moves when the
// build moves *and* when the page being described changes, and two pages never
// share a tag.
//
// Built and compressed per request. That used to be the expensive branch, kept
// to realms alone, because the body was 1.6 MB; with the bundle served on its
// own it is about 40 KB, and brotli at a middle level does it in about a
// millisecond.
func serveDocument(w http.ResponseWriter, r *http.Request, index []byte, hole int,
	baseETag string, block []byte,
) {
	sum := sha256.Sum256(append([]byte(baseETag), block...))
	etag := `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	// Set, not Add: WithCompression has already added one on the way in,
	// and two identical values in a Vary header is noise a shared cache
	// has to parse.
	w.Header().Set("Vary", "Accept-Encoding")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body := make([]byte, 0, len(index)+len(block))
	body = append(body, index[:hole]...)
	body = append(body, block...)
	body = append(body, index[hole:]...)

	switch {
	case acceptsEncoding(r, "br"):
		if br := brotliBytes(body, 5); br != nil {
			w.Header().Set("Content-Encoding", "br")
			body = br
		}
	case acceptsEncoding(r, "gzip"):
		if gz := gzipBytesFast(body); gz != nil {
			w.Header().Set("Content-Encoding", "gzip")
			body = gz
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

// gzipBytesFast is gzipBytes at the other end of the ratio/latency trade, for
// a body that is compressed once per request rather than once per process.
func gzipBytesFast(b []byte) []byte {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil
	}
	if _, err := zw.Write(b); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// gzipBytes compresses at the best available ratio, or returns nil if it
// cannot. A nil result is a supported outcome, not a failure: the caller simply
// serves the body uncompressed, and the generic middleware still gets a chance
// at it.
func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil
	}
	if _, err := zw.Write(b); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// matchesETag reports whether an If-None-Match header names the given tag.
//
// The header is a comma-separated list and a revalidating browser may send the
// tag back weakened (`W/"..."`) even though it was issued strong, so a plain
// string comparison against the whole header misses the match and re-sends
// the body for nothing.
func matchesETag(inm, etag string) bool {
	if inm == "" {
		return false
	}
	for _, candidate := range strings.Split(inm, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// withAnalytics returns index.html with an analytics script tag added just
// before </head>, or unchanged when src is empty.
//
// Injected here rather than written into index.html because the frontend is
// one file compiled into the binary and shared by every deployment, and a
// hardcoded tag would make anyone else running gnoscope report to our account.
//
// The URL lands in an HTML attribute, so it is validated as an absolute http(s)
// URL and escaped. A misconfigured flag fails at startup rather than shipping
// a broken tag, or a javascript: URL, to every reader.
func withAnalytics(index []byte, src string) ([]byte, error) {
	if src == "" {
		return index, nil
	}
	u, err := url.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("analytics script %q: %w", src, err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("analytics script %q: want an absolute http(s) URL", src)
	}
	i := bytes.Index(index, []byte("</head>"))
	if i < 0 {
		return nil, fmt.Errorf("analytics script: no </head> in index.html")
	}
	tag := "<!-- Third-party analytics, off unless -analytics-script names one. -->\n" +
		`<script async src="` + html.EscapeString(u.String()) + `"></script>` + "\n"
	out := make([]byte, 0, len(index)+len(tag))
	out = append(out, index[:i]...)
	out = append(out, tag...)
	out = append(out, index[i:]...)
	return out, nil
}

// withFeatures injects the server-side feature flags the first paint depends on.
//
// A boolean literal built here, never a value echoed from a request: this is a
// <script> in the document, and the one rule it has to keep is that nothing
// attacker-influenced can reach it.
func withFeatures(index []byte, opts Options) ([]byte, error) {
	// Nothing to say when every flag is off, and saying nothing keeps the
	// default build byte-identical to the embedded file.
	if !opts.Shots && opts.DefaultNetwork == "" {
		return index, nil
	}
	i := bytes.Index(index, []byte("</head>"))
	if i < 0 {
		return nil, fmt.Errorf("features: no </head> in index.html")
	}
	tag := "<script>window.FEATURES={shots:" + strconv.FormatBool(opts.Shots) +
		",defaultNetwork:" + strconv.Quote(opts.DefaultNetwork) + "};</script>\n"
	out := make([]byte, 0, len(index)+len(tag))
	out = append(out, index[:i]...)
	out = append(out, tag...)
	out = append(out, index[i:]...)
	return out, nil
}

// Index is the embedded index.html, for callers that need the bytes rather than
// a handler.
func Index() ([]byte, error) { return fs.ReadFile(frontendFS, "frontend/index.html") }
