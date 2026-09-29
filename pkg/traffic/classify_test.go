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
		{"/", "/realms", "page"},
		{"/", "/", "page"},
		{"/", "/assets/index-a3f.js", "asset"},
		{"/", "/favicon.ico", "asset"},
		// A realm path has dots in it and is still a page, which is why the
		// asset test is an extension list and not "contains a dot".
		{"/", "/realm/gno.land/r/moul/home", "page"},
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

func TestRefererHost(t *testing.T) {
	tests := []struct {
		name, referer, self, want string
	}{
		{"external", "https://news.ycombinator.com/item?id=1", "gnoscope.com", "news.ycombinator.com"},
		{"own origin dropped", "https://gnoscope.com/realms", "gnoscope.com", ""},
		{"own origin case insensitive", "https://GnoScope.com/realms", "gnoscope.com", ""},
		{"empty", "", "gnoscope.com", ""},
		{"garbage", "not a url", "gnoscope.com", ""},
		{"no self configured keeps everything", "https://gnoscope.com/x", "", "gnoscope.com"},
		// The path is the part that must never be stored: it is somebody
		// else's page, and often somebody else's private URL.
		{"path discarded", "https://mail.example.com/inbox/secret-thread-42", "", "mail.example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RefererHost(tc.referer, tc.self); got != tc.want {
				t.Errorf("RefererHost(%q, %q) = %q, want %q", tc.referer, tc.self, got, tc.want)
			}
		})
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
