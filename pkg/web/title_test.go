package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// doc fetches a document the way a browser on gnoscope.example would.
func doc(t *testing.T, opts Options, target string) string {
	t.Helper()
	h, err := Handler(opts)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	req := httptest.NewRequest("GET", target, nil)
	req.Host = "gnoscope.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Body.String()
}

var titleRe = regexp.MustCompile(`<title>([^<]*)</title>`)

func titleIn(t *testing.T, body string) string {
	t.Helper()
	m := titleRe.FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("the document has %d <title> elements, want 1", len(m))
	}
	return m[0][1]
}

// Every tab, bookmark and history entry on the site used to read "gnoscope".
// e2e/tests/title.spec.js checks the client agrees with each of these.
func TestPageTitles(t *testing.T) {
	for _, c := range []struct{ path, want string }{
		{"/", "gnoscope"},
		{"/no-such-page", "gnoscope"},
		{"/widget", "gnoscope"},
		{"/realms", "realms · contracts on gnoscope"},
		{"/discover", "discover on gnoscope"},
		{"/analytics", "analytics on gnoscope"},
		{"/gas/realms", "by realm · gas on gnoscope"},
		{"/govdao/options", "options · govdao on gnoscope"},
		{"/govdao", "govdao on gnoscope"},
		{"/params", "params · govdao on gnoscope"},
		{"/tokens", "grc20 · defi on gnoscope"},
		{"/apps/games", "games · apps on gnoscope"},
		{"/cartography", "cartography · lab on gnoscope"},
		{"/gnohub/anything", "gnohub · lab on gnoscope"},
		{"/realm/r/gov/dao", "gno.land/r/gov/dao on gnoscope"},
		{"/realm/gno.land/r/gov/dao/", "gno.land/r/gov/dao on gnoscope"},
		{"/realm/x/y", "gnoscope"},
		{"/tx/abc%2Fdef=", "tx abc/def= on gnoscope"},
		{"/block/1234", "block 1234 on gnoscope"},
		{"/block/007x", "block 7 on gnoscope"},
		{"/block/latest", "gnoscope"},
		{"/govdao/12", "proposal 12 on gnoscope"},
		{"/address/g1abc", "g1abc on gnoscope"},
		{"/validator/g1abc", "g1abc on gnoscope"},
		{"/grc20/gno.land%2Fr%2Fdemo%2Ffoo20.FOO", "gno.land/r/demo/foo20.FOO on gnoscope"},
		{"/stdlib/strings", "strings · stdlib on gnoscope"},
		{"/code/r/demo/boards", "r/demo/boards · code on gnoscope"},
		{"/code", "code map · developer on gnoscope"},
		{"/search/foo", "search on gnoscope"},
	} {
		t.Run(c.path, func(t *testing.T) {
			if got := titleIn(t, doc(t, Options{}, c.path)); got != c.want {
				t.Errorf("title = %q, want %q", got, c.want)
			}
		})
	}
}

// The realm page already had a title, in og:title. One format, not two.
func TestRealmTitleIsItsCardTitle(t *testing.T) {
	body := doc(t, Options{Shots: true}, "/realm/r/gov/dao?tab=state")
	if !strings.Contains(body, `property="og:title" content="`+titleIn(t, body)+`"`) {
		t.Errorf("<title>%s</title> is not the og:title", titleIn(t, body))
	}
}

// Most of a title comes off the URL and a realm path comes off the chain.
func TestTitleIsEscapedAndBounded(t *testing.T) {
	body := doc(t, Options{}, "/address/%3Cscript%3Ealert(1)%3C%2Fscript%3E")
	if strings.Contains(body, "<script>alert(1)") {
		t.Fatal("a path reached the title unescaped")
	}
	if got := titleIn(t, body); got != "&lt;script&gt;alert(1)&lt;/script&gt; on gnoscope" {
		t.Errorf("title = %q", got)
	}

	long := strings.Repeat("g", 500)
	if got := titleIn(t, doc(t, Options{}, "/address/"+long)); len(got) > maxTitleThing+len("… on gnoscope")+1 {
		t.Errorf("a 500-character address became a %d-byte title", len(got))
	}
}

// The labels the server titles a rail page with are NAV's labels, which the
// client titles it with. TestRailMatchesNavTable already pins id and path.
func TestRailLabelsMatchNavTable(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	rail := railTitles(index)
	navTable := between(t, string(index), "const NAV = [", "\n];")
	re := regexp.MustCompile(`label: '([^']+)', path: '([^']+)'`)
	n := 0
	for _, m := range re.FindAllStringSubmatch(navTable, -1) {
		label, path := m[1], m[2]
		if path == "/" {
			continue
		}
		n++
		got, ok := rail[path]
		if !ok {
			t.Errorf("NAV has %s and the rail does not", path)
			continue
		}
		if got != label && !strings.HasPrefix(got, label+" · ") {
			t.Errorf("%s: the rail titles it %q, NAV labels it %q", path, got, label)
		}
	}
	if n != len(rail) || n < 40 {
		t.Errorf("NAV has %d paths and the rail %d", n, len(rail))
	}
}

// A shared link has to canonicalise to the view that was shared. og:url used
// to drop the query string, which took the network and the tab with it.
func TestCanonicalURLKeepsTheView(t *testing.T) {
	ogURL := func(body string) string {
		m := regexp.MustCompile(`property="og:url" content="([^"]*)"`).FindStringSubmatch(body)
		if m == nil {
			t.Fatal("no og:url")
		}
		return strings.ReplaceAll(m[1], "&amp;", "&")
	}
	for _, c := range []struct{ target, want string }{
		{"/", "https://gnoscope.example/"},
		{"/txs?network=test12", "https://gnoscope.example/txs?network=test12"},
		{
			"/realm/r/gov/dao?network=test12&tab=state&key=a.b&utm_source=x&fbclid=y&embed=1",
			"https://gnoscope.example/realm/r/gov/dao?key=a.b&network=test12&tab=state",
		},
		{"/realm/gno.land/r/gov/dao/?tab=calls", "https://gnoscope.example/realm/r/gov/dao?tab=calls"},
		{"/cartography?v=nope&network=gnoland1", "https://gnoscope.example/cartography?network=gnoland1&v=metropolis"},
		{"/address/g1abc?q=%22%3E%3Cx", "https://gnoscope.example/address/g1abc?q=%22%3E%3Cx"},
	} {
		t.Run(c.target, func(t *testing.T) {
			if got := ogURL(doc(t, Options{Shots: true}, c.target)); got != c.want {
				t.Errorf("og:url = %q, want %q", got, c.want)
			}
		})
	}
}
