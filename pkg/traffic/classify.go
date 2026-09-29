package traffic

import (
	"net/url"
	"strings"
)

// Reducing a request to something that can be counted without growing forever.
//
// The naive access log stores the URL, and then every distinct URL is a row the
// dashboard has to group. That is fine until a crawler walks pagination and
// /api/txs?limit=50&offset=4300 becomes ten thousand distinct "pages". So a
// request is split in two here: a Route, which is the mux pattern and therefore
// bounded by the routing table, and a Target, which is the variable part and is
// the half worth reading ("which realm did they open").

// maxTargetLen bounds the one unbounded field. A realm path is well under this;
// anything longer is a scanner probing, and its first 160 characters are as
// much as anyone needs to see to recognise that.
const maxTargetLen = 160

// SplitPattern turns a matched mux pattern and the request path into the pair
// the table stores.
//
// It reads the pattern rather than a hand-written list of prefixes, which is
// the only version that cannot drift: a route added to RegisterRoutes is
// classified correctly here the moment it exists, with nothing to remember.
//
// Go's mux patterns are "METHOD /literal/{wildcard}/literal", so the variable
// part is whatever sits between the literal prefix and the literal suffix:
//
//	GET /api/realm/{path...}          -> prefix "/api/realm/",   suffix ""
//	GET /api/address/{addr}/identity  -> prefix "/api/address/", suffix "/identity"
//	GET /api/txs                      -> no wildcard, no target
func SplitPattern(pattern, path string) (route, target string) {
	route = strings.TrimSpace(pattern)
	if i := strings.IndexByte(route, ' '); i >= 0 {
		route = route[i+1:] // drop the method; it is stored on its own
	}
	if route == "" {
		route = "/"
	}

	open := strings.IndexByte(route, '{')
	if open < 0 {
		return route, ""
	}
	closeIdx := strings.LastIndexByte(route, '}')
	prefix, suffix := route[:open], ""
	if closeIdx >= 0 && closeIdx+1 < len(route) {
		suffix = route[closeIdx+1:]
	}

	target = strings.TrimPrefix(path, prefix)
	target = strings.TrimSuffix(target, suffix)
	return route, clip(strings.Trim(target, "/"))
}

// clip bounds a stored string without lying about having done so.
func clip(s string) string {
	if len(s) <= maxTargetLen {
		return s
	}
	return s[:maxTargetLen] + "…"
}

// assetExts are the extensions the SPA route serves that are not pages. Kept
// explicit rather than "has a dot", because a realm path has dots in it.
var assetExts = []string{
	".js", ".mjs", ".css", ".map", ".ico", ".png", ".jpg", ".jpeg",
	".svg", ".gif", ".webp", ".avif", ".woff", ".woff2", ".ttf", ".txt", ".json", ".xml",
}

// PageViewPath is the beacon a single-page app calls when it changes page.
//
// It exists because a page view is otherwise invisible to this server. The
// frontend navigates with history.pushState, so moving from /realms to /apps
// sends no document request at all: the only trace is whatever XHRs the new
// page happens to fetch. Counting those as "traffic" is what made one refresh
// look like ten requests, and counting the document fetch instead would miss
// every in-app navigation after the first.
const PageViewPath = "/api/traffic/pageview"

// Kind buckets a request by what it is for, which is the split every panel on
// the dashboard is drawn along: a person reading pages, an agent calling tools,
// and the machinery underneath both.
//
// "page" means a page *view*, reported by the beacon. The HTML document that
// bootstraps the app is "document", which is a different event: one document
// load carries many page views, and a crawler that never runs JavaScript
// produces a document load and no page view at all. Keeping them apart is what
// lets the headline number mean what a reader expects it to mean.
func Kind(route, path, mcpPath string) string {
	switch {
	case route == PageViewPath:
		return "page"
	case route == mcpPath || strings.HasPrefix(route, mcpPath+"/"):
		return "mcp"
	case strings.HasPrefix(route, "/api/shield/"), strings.HasPrefix(route, "/_badges"):
		return "badge"
	case strings.HasPrefix(route, "/api/"):
		return "api"
	}
	lower := strings.ToLower(path)
	for _, ext := range assetExts {
		if strings.HasSuffix(lower, ext) {
			return "asset"
		}
	}
	return "document"
}

// SPARoute is the mux pattern that matches everything the API did not claim.
const SPARoute = "/"

// PageTarget is the SPA's variable half: the app path itself.
//
// Unbounded on purpose, and safe because it is bounded in practice by what the
// frontend routes to plus whatever scanners ask for. That long tail is not
// noise to be suppressed; "what do people request that we do not serve" is one
// of the few things an access log is uniquely good at answering.
func PageTarget(path string) string {
	if path == "" || path == "/" {
		return "/"
	}
	return clip(path)
}

// botMarkers are the substrings that identify a crawler by self-declaration.
// Nothing here tries to catch a crawler that lies; a log that pretends to has
// the worse failure mode, because then nobody checks.
var botMarkers = []string{
	"bot", "crawl", "spider", "slurp", "headless", "preview", "fetcher",
	"monitor", "uptime", "scrape", "archive",
}

// agentMarkers identify a program calling deliberately: scripts, SDKs, and the
// MCP clients this explorer exists to serve. Distinct from a bot, because an
// agent asking for a realm is a reader and a crawler indexing it is not.
var agentMarkers = []string{
	"curl", "wget", "python-requests", "httpx", "aiohttp", "go-http-client",
	"node-fetch", "axios", "okhttp", "java/", "claude", "gpt", "openai", "anthropic", "mcp",
}

// InternalUA is the user-agent this server sends to itself.
//
// The cache warmer replays real request paths through the real handler stack,
// which is the whole point of it: the entries it fills are keyed exactly as a
// reader's request would key them. That also means it arrives at the outermost
// middleware looking like a reader, and on 2026-09-29 it was one: the first
// hour on val1 recorded 117 requests where Caddy, which sees only what crosses
// the network, had seen 25. Every panel was inflated by the server talking to
// itself, and the ratio gets worse the quieter the site is.
//
// pkg/httpapi's ViewCounter already excluded this by name. This is the same
// exclusion, one layer out.
const InternalUA = "gnoscope-warmer"

// ClientClass reduces a user-agent to one of five words, which is the only form
// of it that is stored. The full string is a fingerprint; this is a category.
func ClientClass(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return "unknown"
	}
	if strings.Contains(strings.ToLower(ua), InternalUA) {
		return "internal"
	}
	l := strings.ToLower(ua)
	for _, m := range botMarkers {
		if strings.Contains(l, m) {
			return "bot"
		}
	}
	for _, m := range agentMarkers {
		if strings.Contains(l, m) {
			return "agent"
		}
	}
	if strings.Contains(l, "mozilla/") || strings.Contains(l, "safari/") || strings.Contains(l, "gecko") {
		return "browser"
	}
	return "unknown"
}

// trackingParams are query parameters that identify a campaign rather than a
// page. They carry no information about *which* link was followed and they are
// the part of a URL most likely to be per-recipient, so they are dropped.
var trackingParams = []string{
	"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "utm_id",
	"fbclid", "gclid", "dclid", "msclkid", "twclid", "igshid", "mc_cid", "mc_eid",
	"ref_src", "ref_url", "s", "t", // the two X/Twitter adds to every share
}

// RefererURL keeps the whole referring link, minus its tracking parameters.
//
// This deliberately stores more than the host. The host alone answers "who
// links to us" and cannot answer "which post", which is the question worth
// asking when a link lands: one tweet and one aggregator thread are both
// "twitter.com" and they are not the same event.
//
// ⚠️ The trade, stated because it is a real one: a referring URL is somebody
// else's page, and it can be an internal or private one. This explorer's
// traffic page is public, so those paths are published. Tracking parameters and
// any embedded credentials are stripped, and the fragment never reaches a
// server at all, but a private path in a referer still becomes public here. To
// go back to host-only, return u.Hostname() and delete the rest.
//
// Own-origin referers return "", since a link from one page of this app to the
// next says nothing about acquisition and would otherwise dominate the list.
func RefererURL(referer string, self string) string {
	referer = strings.TrimSpace(referer)
	if referer == "" {
		return ""
	}
	u, err := url.Parse(referer)
	if err != nil || u.Host == "" {
		return ""
	}
	if self != "" && strings.EqualFold(u.Hostname(), self) {
		return ""
	}
	// Credentials in a referer are rare and always an accident. They are
	// removed rather than published.
	u.User = nil
	// The fragment never leaves a browser, but a referer can be constructed by
	// hand and this is written against what arrives, not against what should.
	u.Fragment = ""
	if q := u.Query(); len(q) > 0 {
		for _, k := range trackingParams {
			q.Del(k)
		}
		u.RawQuery = q.Encode()
	}
	u.Host = strings.ToLower(u.Host)
	out := u.String()
	// A bare host keeps its trailing slash off, so "example.com/" and
	// "example.com" do not become two rows for one site.
	out = strings.TrimSuffix(out, "/")
	return clip(out)
}

// RefererHost reduces a stored referer back to its host, for grouping.
func RefererHost(ref string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" {
		// Already a bare host, from before full URLs were stored.
		return ref
	}
	return u.Hostname()
}

// probePaths are the well-known URLs of software this server does not run.
//
// Every one of them is a scanner looking for an unpatched WordPress, an exposed
// .env or a readable .git. They are real requests and are recorded, but they
// are not readers and they are not 404s worth investigating: left in the
// not-found panel they crowd out the mistyped realm paths, which are the ones
// that say something.
//
// This list is meant to grow. Add the prefix, not the exact path: scanners walk
// whole trees.
var probePaths = []string{
	"/wp-login.php", "/wp-admin", "/wp-content", "/wp-includes", "/wordpress",
	"/xmlrpc.php", "/.env", "/.git", "/.aws", "/.ssh", "/.vscode", "/.DS_Store",
	"/phpmyadmin", "/pma", "/myadmin", "/mysql", "/adminer",
	"/administrator", "/admin.php", "/cgi-bin", "/vendor/phpunit",
	"/config.json", "/credentials", "/secrets", "/actuator", "/solr",
	"/owa/", "/autodiscover", "/boaform", "/hudson", "/jenkins",
	"/telescope/requests", "/server-status", "/.well-known/security.txt",
	"/rest/api/1.0", "/api/jsonws", "/HNAP1", "/shell", "/eval-stdin.php",
}

// IsProbe reports whether a path is a scanner looking for software that is not
// here. Case-insensitive, because scanners are not consistent.
func IsProbe(path string) bool {
	l := strings.ToLower(path)
	for _, p := range probePaths {
		if l == p || strings.HasPrefix(l, p+"/") || strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// Grouping a page by what it is about, rather than by its URL.
//
// A ranked list of raw paths answers "which URL was opened" and not "which
// realm are people looking at". One realm is read through its overview, its
// usage tab, its source browser and its forge view, which are four paths and
// one subject; a list of paths splits that four ways and buries it under
// whichever listing page happens to be busiest.
//
// So each page view also carries what it is about (EntityKind + Entity) and
// what shape of page it is (PageKind). The two answer opposite questions and
// both are worth having: "which realm" and "which sort of page".

// EntityKind values. Deliberately few: a bucket nobody can act on is noise.
const (
	EntityRealm   = "realm"
	EntityAddress = "address"
	EntityTx      = "tx"
	EntityBlock   = "block"
	EntityAsset   = "asset"
	EntityNone    = ""
)

// entityRoutes maps a frontend path prefix to what the page underneath is
// about, and to how much of the remainder identifies it.
//
// Ordered, because /realm/ and /realms are both prefixes of each other's
// neighbourhood and the longer one has to win.
var entityRoutes = []struct {
	prefix string
	kind   string
	// segments limits how much of the tail is the identity. 0 means all of it,
	// which is right for a realm path and wrong for anything with sub-views.
	segments int
}{
	{"/realm/", EntityRealm, 0},
	{"/gnohub/", EntityRealm, 0},
	{"/address/", EntityAddress, 1},
	{"/validator/", EntityAddress, 1},
	{"/tx/", EntityTx, 1},
	{"/block/", EntityBlock, 1},
	{"/grc20/", EntityAsset, 1},
	{"/stdlib/", EntityRealm, 0},
}

// Entity returns what a page is about: a kind and an identifier.
//
// Returns ("", "") for pages that are about the chain rather than about one
// thing, which is most listing pages. That is not a gap to fill: "/realms" is
// genuinely not about a realm.
func Entity(path string) (kind, id string) {
	for _, r := range entityRoutes {
		rest, ok := strings.CutPrefix(path, r.prefix)
		if !ok || rest == "" {
			continue
		}
		// The frontend hangs sub-views off a "/-/" separator (a source file, a
		// commits list, a forge tab). Everything after it describes the view,
		// not the subject.
		if i := strings.Index(rest, "/-/"); i >= 0 {
			rest = rest[:i]
		}
		if r.segments > 0 {
			parts := strings.SplitN(rest, "/", r.segments+1)
			if len(parts) > r.segments {
				parts = parts[:r.segments]
			}
			rest = strings.Join(parts, "/")
		}
		rest = strings.Trim(rest, "/")
		if rest == "" {
			return EntityNone, ""
		}
		// Realm paths reach the frontend with and without the chain prefix
		// depending on which page linked there. One realm, one row.
		if r.kind == EntityRealm {
			rest = strings.TrimPrefix(rest, "gno.land/")
		}
		return r.kind, clip(rest)
	}
	return EntityNone, ""
}

// pageKinds maps a path to the sort of page it is, for the opposite question:
// not "which realm" but "what do people come here to do".
//
// Matched longest-prefix-first, so /gas/realms is a listing and not the gas
// overview. The fallthrough is "other" rather than a guess.
var pageKinds = []struct{ prefix, kind string }{
	{"/realm/", "realm detail"},
	{"/gnohub/", "source browser"},
	{"/address/", "address detail"},
	{"/validator/", "validator detail"},
	{"/tx/", "transaction"},
	{"/block/", "block"},
	{"/grc20/", "asset detail"},
	{"/stdlib/", "stdlib"},
	{"/govdao", "governance"},
	{"/developer", "developer tools"},
	{"/gas", "gas"},
	{"/storage", "storage"},
	{"/directory", "directory"},
	{"/traffic", "traffic"},
	{"/analytics", "analytics"},
	{"/dashboards", "dashboards"},
	{"/discover", "discover"},
	{"/sanity", "chain health"},
	{"/validators", "validators"},
	{"/search", "search"},
	{"/watch", "watchlist"},
	{"/glossary", "glossary"},
}

// PageKind names the shape of a page. "/" is the landing page, which is worth
// its own row rather than being folded into a listing.
func PageKind(path string) string {
	if path == "" || path == "/" {
		return "home"
	}
	best, bestLen := "", -1
	for _, pk := range pageKinds {
		if strings.HasPrefix(path, pk.prefix) && len(pk.prefix) > bestLen {
			best, bestLen = pk.kind, len(pk.prefix)
		}
	}
	if best != "" {
		return best
	}
	// Everything left is a top-level listing: /realms, /apps, /txs, /blocks,
	// /accounts, /coins and friends. They behave the same way and a row each
	// would be a second copy of the pages panel.
	if strings.Count(strings.Trim(path, "/"), "/") == 0 {
		return "listing"
	}
	return "other"
}
