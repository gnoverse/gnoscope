package badge

import (
	"encoding/xml"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every badge is a document parsed by somebody else's XML parser, so the first
// thing worth proving is that it is one at all. A card whose title happened to
// contain a `<` used to be a perfectly plausible way to ship a document nothing
// could read.
func TestRenderIsWellFormedXML(t *testing.T) {
	tests := []struct {
		name string
		card Card
	}{
		{"ordinary", Card{Title: "gno.land/r/moul/home", Headline: "1,234 messages", Sub: "last 30 days", Series: ramp(30)}},
		{"no series", Card{Title: "gno.land/r/moul/home", Headline: "0 messages"}},
		{"one point", Card{Title: "x", Series: ramp(1)}},
		{"all zero", Card{Title: "x", Series: flat(10, 0)}},
		{"markup in every field", Card{
			Title:    `</svg><script>alert(1)</script>`,
			Headline: `" onload="alert(1)`,
			Sub:      `a & b < c > d '`,
			Empty:    `<img src=x onerror=alert(1)>`,
		}},
		{"control bytes", Card{Title: "a\x00b\x1fc\td", Headline: "e\nf"}},
		{"light", Card{Title: "x", Series: ramp(5), Theme: ThemeLight}},
		{"dark", Card{Title: "x", Series: ramp(5), Theme: ThemeDark}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := Render(tt.card)
			d := xml.NewDecoder(strings.NewReader(string(out)))
			for {
				_, err := d.Token()
				if err != nil {
					if err.Error() == "EOF" {
						break
					}
					t.Fatalf("not well-formed XML: %v\n%s", err, out)
				}
			}
		})
	}
}

// The escaper is the single thing standing between a package path off the chain
// and script execution on gnoscope's own origin, since a badge visited
// directly is a top-level document there.
func TestRenderEscapesEveryField(t *testing.T) {
	const payload = `</title></svg><script>alert(1)</script>`
	cards := map[string]Card{
		"title":    {Title: payload},
		"headline": {Headline: payload},
		"sub":      {Sub: payload},
		"empty":    {Empty: payload, Series: ramp(1)},
	}
	for name, c := range cards {
		t.Run(name, func(t *testing.T) {
			out := string(Render(c))
			if strings.Contains(out, "<script") {
				t.Fatalf("unescaped <script> reached the output:\n%s", out)
			}
			if strings.Contains(out, "</svg><") {
				t.Fatalf("the payload closed the document:\n%s", out)
			}
			if !strings.Contains(out, "&lt;script&gt;") {
				t.Fatalf("payload neither escaped nor dropped:\n%s", out)
			}
		})
	}
}

// An attribute-context break is its own case: a `"` that survives lands inside
// aria-label, which is written as an attribute value, and the escaped form
// `onload=&quot;` is still a substring of the output. So this is asserted
// against the parsed tree rather than against the bytes: the question is
// whether an *attribute* called onload exists, not whether the six characters
// do.
func TestRenderEscapesAttributeContext(t *testing.T) {
	for _, c := range []Card{
		{Title: `x" onload="alert(1)`},
		{Headline: `x' onload='alert(1)`},
		{Sub: `x" href="javascript:alert(1)`},
	} {
		d := xml.NewDecoder(strings.NewReader(string(Render(c))))
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			se, ok := tok.(xml.StartElement)
			if !ok {
				continue
			}
			for _, a := range se.Attr {
				if strings.HasPrefix(strings.ToLower(a.Name.Local), "on") {
					t.Fatalf("%+v produced an event-handler attribute %q", c, a.Name.Local)
				}
				// Only where a value would be dereferenced. aria-label is a
				// text node in attribute clothing, and the string
				// "javascript:" sitting in it is a realm's title, not a URL.
				switch strings.ToLower(a.Name.Local) {
				case "href", "src":
					if strings.Contains(strings.ToLower(a.Value), "javascript:") {
						t.Fatalf("%+v produced a javascript: URL in %q", c, a.Name.Local)
					}
				}
			}
		}
	}
}

func TestEsc(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"a&b", "a&amp;b"},
		{"<x>", "&lt;x&gt;"},
		{`"q"`, "&quot;q&quot;"},
		{"it's", "it&#39;s"},
		{"a\x00b", "ab"},
		{"a\x1fb", "ab"},
		{"a\x7fb", "ab"},
		{"a\tb\nc\rd", "a b c d"},
		{"héllo ∂", "héllo ∂"},
	}
	for _, tt := range tests {
		if got := esc(tt.in); got != tt.want {
			t.Errorf("esc(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"eleven-char", 10, "eleven-ch…"},
		{"ünïcödé-cut-here", 5, "ünïc…"},
		{"anything", 0, ""},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// A single point is not a line, and drawing it as one would claim a window of
// history the series does not have. It takes the same branch as no points.
func TestSparklineRefusesToDrawTooFewPoints(t *testing.T) {
	for _, n := range []int{0, 1} {
		out := string(Render(Card{Series: ramp(n), Empty: "no activity yet"}))
		if strings.Contains(out, `class="line"`) {
			t.Errorf("%d point(s) drew a line", n)
		}
		if !strings.Contains(out, "no activity yet") {
			t.Errorf("%d point(s) did not say why there is no graph:\n%s", n, out)
		}
	}
}

// A window in which nothing happened is a real answer, and the one most likely
// to divide by its own maximum. It must draw a flat line on the baseline, not
// NaN coordinates that silently make the whole path unrenderable.
func TestSparklineAllZeroDrawsAFlatBaseline(t *testing.T) {
	out := string(Render(Card{Series: flat(12, 0)}))
	if strings.Contains(out, "NaN") || strings.Contains(out, "Inf") {
		t.Fatalf("non-finite coordinates:\n%s", out)
	}
	ys := pathYs(t, out)
	if len(ys) == 0 {
		t.Fatalf("no line drawn:\n%s", out)
	}
	for _, y := range ys {
		if math.Abs(y-float64(plotBottom)) > 0.05 {
			t.Fatalf("y = %v, want the baseline %v", y, plotBottom)
		}
	}
}

// A flat line on the baseline is correct and unreadable: it looks exactly like
// a graph that failed to draw. When there is an Empty message to give, the
// window says why it is flat *as well as* drawing the line.
func TestSparklineLabelsAnAllZeroWindow(t *testing.T) {
	out := string(Render(Card{Series: flat(20, 0), Empty: "no activity in this window"}))
	if !strings.Contains(out, "no activity in this window") {
		t.Errorf("a flat-zero window did not say why it is flat:\n%s", out)
	}
	if !strings.Contains(out, `class="line"`) {
		t.Errorf("the label replaced the line instead of joining it:\n%s", out)
	}

	// And it stays out of the way when there is something to look at.
	busy := string(Render(Card{Series: ramp(20), Empty: "no activity in this window"}))
	if strings.Contains(busy, "no activity in this window") {
		t.Errorf("a window with traffic claimed to be empty:\n%s", busy)
	}
}

// The peak has to touch the top of the plot and the trough the bottom, or the
// graph is not using the height it was given and a reader cannot compare two
// badges by shape.
func TestSparklineUsesTheFullPlotHeight(t *testing.T) {
	out := string(Render(Card{Series: []Point{{Value: 0}, {Value: 5}, {Value: 10}}}))
	ys := pathYs(t, out)
	if len(ys) != 3 {
		t.Fatalf("got %d points, want 3: %v", len(ys), ys)
	}
	if math.Abs(ys[0]-float64(plotBottom)) > 0.05 {
		t.Errorf("the zero point is at %v, want the baseline %v", ys[0], plotBottom)
	}
	if math.Abs(ys[2]-float64(plotTop)) > 0.05 {
		t.Errorf("the peak is at %v, want the top %v", ys[2], plotTop)
	}
	if !(ys[1] < ys[0] && ys[1] > ys[2]) {
		t.Errorf("the middle point %v is not between %v and %v", ys[1], ys[0], ys[2])
	}
}

// Auto ships both palettes behind a media query; an explicit theme ships one
// and no query, so the badge cannot flip under a reader whose OS disagrees with
// the page that embedded it.
func TestThemeSelectsOnePaletteOrBoth(t *testing.T) {
	tests := []struct {
		theme     Theme
		wantQuery bool
		wantFill  string
	}{
		{ThemeAuto, true, "#ffffff"},
		{ThemeLight, false, "#ffffff"},
		{ThemeDark, false, "#0d1117"},
	}
	for _, tt := range tests {
		t.Run(string(tt.theme), func(t *testing.T) {
			out := string(Render(Card{Title: "x", Theme: tt.theme}))
			if got := strings.Contains(out, "prefers-color-scheme"); got != tt.wantQuery {
				t.Errorf("prefers-color-scheme present = %v, want %v", got, tt.wantQuery)
			}
			if !strings.Contains(out, tt.wantFill) {
				t.Errorf("palette %q missing from output", tt.wantFill)
			}
		})
	}
}

func TestParseTheme(t *testing.T) {
	tests := []struct {
		in   string
		want Theme
	}{
		{"", ThemeAuto},
		{"auto", ThemeAuto},
		{"light", ThemeLight},
		{"DARK", ThemeDark},
		{" dark ", ThemeDark},
		{"neon", ThemeAuto},
	}
	for _, tt := range tests {
		if got := ParseTheme(tt.in); got != tt.want {
			t.Errorf("ParseTheme(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// An SVG in an <img> cannot fetch anything, so anything that looks like a fetch
// is a silent no-op that makes the badge render differently in a preview than
// on the page it ships to.
func TestRenderReferencesNothingExternal(t *testing.T) {
	out := string(Render(Card{Title: "gno.land/r/moul/home", Headline: "1 message", Series: ramp(9)}))
	// The SVG namespace URI is a name, not a fetch, and is the one http:// an
	// SVG document is required to carry.
	out = strings.Replace(out, `xmlns="http://www.w3.org/2000/svg"`, "", 1)
	for _, bad := range []string{"http://", "https://", "xlink:href", "@import", "url("} {
		if strings.Contains(out, bad) {
			t.Errorf("output references something external (%q):\n%s", bad, out)
		}
	}
}

// --- helpers ---------------------------------------------------------------

func ramp(n int) []Point {
	pts := make([]Point, n)
	for i := range pts {
		pts[i] = Point{Label: strconv.Itoa(i), Value: float64(i)}
	}
	return pts
}

func flat(n int, v float64) []Point {
	pts := make([]Point, n)
	for i := range pts {
		pts[i] = Point{Label: strconv.Itoa(i), Value: v}
	}
	return pts
}

var linePathRe = regexp.MustCompile(`<path class="line" d="([^"]*)"`)
var coordRe = regexp.MustCompile(`[ML]([-0-9.]+) ([-0-9.]+)`)

// pathYs pulls the y coordinates out of the drawn line, so a test can assert
// about the geometry rather than about a string of path commands.
func pathYs(t *testing.T, svg string) []float64 {
	t.Helper()
	m := linePathRe.FindStringSubmatch(svg)
	if m == nil {
		return nil
	}
	var ys []float64
	for _, c := range coordRe.FindAllStringSubmatch(m[1], -1) {
		y, err := strconv.ParseFloat(c[2], 64)
		if err != nil {
			t.Fatalf("unparseable coordinate %q in %q", c[2], m[1])
		}
		ys = append(ys, y)
	}
	return ys
}
