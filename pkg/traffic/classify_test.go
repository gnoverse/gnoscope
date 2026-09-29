package traffic

import "testing"

func TestSplitPattern(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		path       string
		wantRoute  string
		wantTarget string
	}{
		{"no wildcard", "GET /api/txs", "/api/txs", "/api/txs", ""},
		{"trailing wildcard", "GET /api/realm/{path...}", "/api/realm/r/moul/home", "/api/realm/{path...}", "r/moul/home"},
		{"wildcard with suffix", "GET /api/address/{addr}/identity", "/api/address/g1abc/identity", "/api/address/{addr}/identity", "g1abc"},
		{"bare wildcard segment", "GET /api/block/{height}", "/api/block/91234", "/api/block/{height}", "91234"},
		{"two literals around it", "GET /api/shield/{kind}/{path...}", "/api/shield/status/r/moul/home", "/api/shield/{kind}/{path...}", "status/r/moul/home"},
		{"spa root", "GET /", "/realms", "/", ""},
		{"empty pattern falls back", "", "/whatever", "/", ""},
		{"no method prefix", "/api/stats", "/api/stats", "/api/stats", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			route, target := SplitPattern(tc.pattern, tc.path)
			if route != tc.wantRoute {
				t.Errorf("route = %q, want %q", route, tc.wantRoute)
			}
			if target != tc.wantTarget {
				t.Errorf("target = %q, want %q", target, tc.wantTarget)
			}
		})
	}
}

// A scanner's URL must not become an unbounded row. The clip is what keeps the
// one variable-length column from being attacker-sized.
func TestSplitPatternClipsTarget(t *testing.T) {
	long := ""
	for len(long) < maxTargetLen*2 {
		long += "abcdefghij"
	}
	_, target := SplitPattern("GET /api/realm/{path...}", "/api/realm/"+long)
	if len([]rune(target)) > maxTargetLen+1 {
		t.Fatalf("target is %d runes, want at most %d", len([]rune(target)), maxTargetLen+1)
	}
}

func TestKind(t *testing.T) {
	tests := []struct {
		route, path, want string
	}{
		{"/mcp", "/mcp", "mcp"},
		{"/api/shield/{kind}/{path...}", "/api/shield/status/r/x", "badge"},
		{"/api/realm/{path...}", "/api/realm/r/moul/home", "api"},
		{"/api/stats", "/api/stats", "api"},
		// The HTML document, not a page view. One document load carries
		// however many page views the reader goes on to make, and the beacon
		// is what reports those.
		{"/", "/realms", "document"},
		{"/", "/", "document"},
		{"/", "/assets/index-a3f.js", "asset"},
		{"/", "/favicon.ico", "asset"},
		// A realm path has dots in it and is still a document, which is why
		// the asset test is an extension list and not "contains a dot".
		{"/", "/realm/gno.land/r/moul/home", "document"},
		// The beacon is the only thing that produces a page view.
		{PageViewPath, PageViewPath, "page"},
	}
	for _, tc := range tests {
		if got := Kind(tc.route, tc.path, "/mcp"); got != tc.want {
			t.Errorf("Kind(%q, %q) = %q, want %q", tc.route, tc.path, got, tc.want)
		}
	}
}

func TestClientClass(t *testing.T) {
	tests := []struct{ ua, want string }{
		{"", "unknown"},
		{"Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/120 Safari/537.36", "browser"},
		{"Googlebot/2.1 (+http://www.google.com/bot.html)", "bot"},
		{"curl/8.4.0", "agent"},
		{"claude-code/1.0 mcp", "agent"},
		{"Go-http-client/2.0", "agent"},
		// Not "unknown": this server talking to itself is its own category, and
		// the one the report drops unconditionally.
		{"gnoscope-warmer", "internal"},
		{"something entirely made up", "unknown"},
		// A headless browser sends a browser UA and is still not a reader, so
		// the bot markers are checked before the browser ones.
		{"Mozilla/5.0 HeadlessChrome/120", "bot"},
	}
	for _, tc := range tests {
		if got := ClientClass(tc.ua); got != tc.want {
			t.Errorf("ClientClass(%q) = %q, want %q", tc.ua, got, tc.want)
		}
	}
}

func TestRefererURL(t *testing.T) {
	tests := []struct {
		name, referer, self, want string
	}{
		// The whole point: one tweet and one aggregator thread are both
		// "twitter.com" and are not the same event.
		{"keeps the path", "https://news.ycombinator.com/item?id=1", "gnoscope.com", "https://news.ycombinator.com/item?id=1"},
		{"keeps a post id", "https://x.com/someone/status/1234567890", "gnoscope.com", "https://x.com/someone/status/1234567890"},
		{"own origin dropped", "https://gnoscope.com/realms", "gnoscope.com", ""},
		{"own origin case insensitive", "https://GnoScope.com/realms", "gnoscope.com", ""},
		{"empty", "", "gnoscope.com", ""},
		{"garbage", "not a url", "gnoscope.com", ""},
		{"bare host loses its slash", "https://example.com/", "", "https://example.com"},
		// Campaign parameters identify a recipient, not a link.
		{"utm stripped", "https://example.com/p?utm_source=x&utm_campaign=y&id=7", "", "https://example.com/p?id=7"},
		{"click ids stripped", "https://example.com/p?fbclid=abc&gclid=def", "", "https://example.com/p"},
		{"x share params stripped", "https://x.com/a/status/9?s=20&t=abc", "", "https://x.com/a/status/9"},
		// Credentials in a referer are always an accident, never published.
		{"credentials removed", "https://user:pw@example.com/p", "", "https://example.com/p"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RefererURL(tc.referer, tc.self); got != tc.want {
				t.Errorf("RefererURL(%q, %q) = %q, want %q", tc.referer, tc.self, got, tc.want)
			}
		})
	}
}

func TestRefererHostFromStoredURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://news.ycombinator.com/item?id=1": "news.ycombinator.com",
		"https://x.com/a/status/9":               "x.com",
		"":                                       "",
		"example.com":                            "example.com", // stored before full URLs
	} {
		if got := RefererHost(in); got != want {
			t.Errorf("RefererHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// The scanner list exists so mistyped realm paths are not buried under people
// looking for an unpatched WordPress.
func TestIsProbe(t *testing.T) {
	for _, p := range []string{
		"/wp-login.php", "/wp-admin/setup-config.php", "/.env", "/.git/config",
		"/xmlrpc.php", "/phpmyadmin/index.php", "/vendor/phpunit/eval-stdin.php",
		"/rest/api/1.0/application-properties", "/WP-LOGIN.PHP",
	} {
		if !IsProbe(p) {
			t.Errorf("IsProbe(%q) = false, want true", p)
		}
	}
	// And nothing real is caught. A realm path is not a probe.
	for _, p := range []string{
		"/", "/realms", "/realm/gno.land/r/moul/home", "/api/stats",
		"/address/g1abc", "/gnohub/r/sys/cla", "/traffic",
	} {
		if IsProbe(p) {
			t.Errorf("IsProbe(%q) = true, want false", p)
		}
	}
}

// The warmer replays real request paths through the real handler stack, so it
// reaches the outermost middleware looking like a reader. It is not one.
func TestClientClassNamesTheWarmerInternal(t *testing.T) {
	for _, ua := range []string{InternalUA, "gnoscope-warmer", "GNOSCOPE-WARMER/1"} {
		if got := ClientClass(ua); got != "internal" {
			t.Errorf("ClientClass(%q) = %q, want internal; the server would count itself as traffic", ua, got)
		}
	}
}

func TestEntity(t *testing.T) {
	tests := []struct {
		path, wantKind, wantID string
	}{
		// One realm read four ways is one realm.
		{"/realm/gno.land/r/moul/home", EntityRealm, "r/moul/home"},
		{"/realm/r/moul/home", EntityRealm, "r/moul/home"},
		{"/gnohub/r/moul/home", EntityRealm, "r/moul/home"},
		{"/gnohub/r/moul/home/-/blob/render.gno", EntityRealm, "r/moul/home"},
		{"/gnohub/r/moul/home/-/commits", EntityRealm, "r/moul/home"},
		{"/stdlib/strings", EntityRealm, "strings"},
		// An address is one segment; anything after it is a tab.
		{"/address/g1abc", EntityAddress, "g1abc"},
		{"/address/g1abc/holdings", EntityAddress, "g1abc"},
		{"/validator/g1val", EntityAddress, "g1val"},
		{"/tx/ABC123", EntityTx, "ABC123"},
		{"/block/91234", EntityBlock, "91234"},
		{"/grc20/gno.land%2Fr%2Fx.FOO", EntityAsset, "gno.land%2Fr%2Fx.FOO"},
		// Listing pages are genuinely not about one thing.
		{"/realms", EntityNone, ""},
		{"/", EntityNone, ""},
		{"/apps", EntityNone, ""},
		{"/traffic", EntityNone, ""},
		// A prefix with nothing after it identifies nothing.
		{"/realm/", EntityNone, ""},
	}
	for _, tc := range tests {
		gotKind, gotID := Entity(tc.path)
		if gotKind != tc.wantKind || gotID != tc.wantID {
			t.Errorf("Entity(%q) = (%q, %q), want (%q, %q)", tc.path, gotKind, gotID, tc.wantKind, tc.wantID)
		}
	}
}

func TestPageKind(t *testing.T) {
	tests := []struct{ path, want string }{
		{"/", "home"},
		{"/realms", "listing"},
		{"/apps", "listing"},
		{"/txs", "listing"},
		{"/realm/gno.land/r/moul/home", "realm detail"},
		{"/gnohub/r/sys/cla/-/blob/x.gno", "source browser"},
		{"/address/g1abc", "address detail"},
		{"/tx/ABC", "transaction"},
		{"/govdao/proposals", "governance"},
		{"/developer/search", "developer tools"},
		// Longest prefix wins, or /gas/realms lands on the gas overview.
		{"/gas/realms", "gas"},
		{"/traffic", "traffic"},
		{"/something/deep/unknown", "other"},
	}
	for _, tc := range tests {
		if got := PageKind(tc.path); got != tc.want {
			t.Errorf("PageKind(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
