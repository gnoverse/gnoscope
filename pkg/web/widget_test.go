package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The framing header is the only reason /widget is a route rather than a
// stylesheet.
//
// A page that does not say whether it may be framed cannot be relied on inside
// somebody else's page, and the host cannot find out: cross-origin, a refused
// frame still fires `load`, so what a viewer gets is the browser's grey error
// page covering the tile that would have named the tool. moul/ghoblin#63 is
// the precedent — a compiled-in `frame-ancestors 'none'` cost that tool a dead
// tile on a wall for a week, with nothing on either side saying why.
func TestWidgetSaysWhoMayFrameIt(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string // the whole header, or "" for none
	}{
		{
			// Permissive, and *sent*. An absent directive permits framing too,
			// but it permits it by saying nothing, so a reverse proxy adding
			// X-Frame-Options to every response takes the widget with it. A
			// present frame-ancestors overrides X-Frame-Options wherever both
			// are implemented, which is what makes this the durable answer.
			name: "the default allows anyone and says so",
			opts: Options{WidgetFrameAncestors: DefaultWidgetFrameAncestors},
			want: "frame-ancestors *",
		},
		{
			name: "an operator can narrow it without a rebuild",
			opts: Options{WidgetFrameAncestors: "'self' https://status.example"},
			want: "frame-ancestors 'self' https://status.example",
		},
		{
			name: "and can switch it off entirely",
			opts: Options{WidgetFrameAncestors: ""},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := Handler(tt.opts)
			if err != nil {
				t.Fatalf("Handler: %v", err)
			}
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest(http.MethodGet, WidgetPath, nil))
			if got := rec.Header().Get("Content-Security-Policy"); got != tt.want {
				t.Errorf("CSP on %s = %q, want %q", WidgetPath, got, tt.want)
			}
			if rec.Code != http.StatusOK {
				t.Errorf("status %d: the widget has to be served, not only labelled", rec.Code)
			}
		})
	}
}

// One route, and only that route. A policy that leaked onto the rest of the
// site would be a site-wide decision taken by a widget, and the explorer's own
// pages have never carried one.
func TestOnlyTheWidgetCarriesAFramingPolicy(t *testing.T) {
	h, err := Handler(Options{WidgetFrameAncestors: DefaultWidgetFrameAncestors})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	for _, path := range []string{"/", "/cockpit", "/sanity", "/realm/gno.land/r/demo/boards", "/widgets"} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Content-Security-Policy"); got != "" {
			t.Errorf("%s carries %q; only %s should", path, got, WidgetPath)
		}
	}
}

// The value lands in a response header, so anything that could end that header
// and start another one is refused at startup rather than at request time —
// the same way withAnalytics refuses a script URL it cannot vouch for.
func TestFrameAncestorsRefusesWhatCannotBeAHeader(t *testing.T) {
	tests := []struct {
		name string
		v    string
		ok   bool
	}{
		{"the default", "*", true},
		{"a source list", "'self' https://a.example https://*.b.example", true},
		{"nothing at all", "", true},
		{"a newline, which is header injection", "* \nX-Frame-Options: DENY", false},
		{"a carriage return", "*\r\nSet-Cookie: a=b", false},
		{"a semicolon, which starts another directive", "* ; script-src 'none'", false},
		{"a comma, which starts another policy", "'self', default-src 'none'", false},
		{"a NUL", "*\x00", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFrameAncestors(tt.v)
			if tt.ok && err != nil {
				t.Errorf("validateFrameAncestors(%q) = %v, want accepted", tt.v, err)
			}
			if !tt.ok && err == nil {
				t.Errorf("validateFrameAncestors(%q) was accepted", tt.v)
			}
			if !tt.ok {
				// And the refusal has to stop the process, not degrade.
				if _, herr := Handler(Options{WidgetFrameAncestors: tt.v}); herr == nil {
					t.Errorf("Handler accepted %q", tt.v)
				}
			}
		})
	}
}

// /widget is not a static file and not a realm, so it has to fall through to
// the app shell the same way every other SPA route does.
func TestWidgetServesTheApp(t *testing.T) {
	h, err := Handler(Options{WidgetFrameAncestors: DefaultWidgetFrameAncestors})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, WidgetPath, nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type %q, want HTML", ct)
	}
	if !strings.Contains(rec.Body.String(), `id="view-widget"`) {
		t.Error("the widget view is missing from the document served at /widget")
	}
}
