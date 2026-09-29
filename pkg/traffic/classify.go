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

// Kind buckets a request by what it is for, which is the split every panel on
// the dashboard is drawn along: a person reading pages, an agent calling tools,
// and the machinery underneath both.
func Kind(route, path, mcpPath string) string {
	switch {
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
	return "page"
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

// RefererHost keeps where a reader came from and discards what they were
// reading there. A referer path is somebody else's page and often somebody
// else's private URL; the host is what answers "who links to us".
//
// Own-origin referers return "", since a link from one page of this app to the
// next says nothing about acquisition and would otherwise dominate the list.
func RefererHost(referer string, self string) string {
	referer = strings.TrimSpace(referer)
	if referer == "" {
		return ""
	}
	u, err := url.Parse(referer)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if self != "" && strings.EqualFold(host, self) {
		return ""
	}
	return clip(host)
}
