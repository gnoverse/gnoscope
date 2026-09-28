package badge

import (
	"encoding/xml"
	"math"
	"strconv"
	"strings"
	"testing"
)

// A badge is a document a browser parses. Malformed XML is a broken image with
// no diagnosis, so every case below starts by proving it parses at all.
func parseSVG(t *testing.T, b []byte) {
	t.Helper()
	if err := xml.Unmarshal(b, new(struct {
		XMLName xml.Name
		Inner   string `xml:",innerxml"`
	})); err != nil {
		t.Fatalf("not well-formed XML: %v\n%s", err, b)
	}
}

func TestRenderShieldDrawsBothPlates(t *testing.T) {
	got := RenderShield(Shield{Label: "txs", Message: "1,234", Color: "blue"})
	parseSVG(t, got)
	s := string(got)

	for _, want := range []string{">txs<", ">1,234<", "#007ec6", `role="img"`, `aria-label="txs: 1,234"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// The label plate is drawn from x=0 and the message plate starts where it
	// ends, so a width that did not account for both would overlap them.
	if !strings.Contains(s, `<mask id="m">`) {
		t.Errorf("flat lost its rounded-corner mask:\n%s", s)
	}
}

// The plate has to be wide enough for the text it holds, or the badge draws
// its own message outside the colour it is written on.
func TestRenderShieldWidthTracksItsText(t *testing.T) {
	narrow := svgWidth(t, RenderShield(Shield{Label: "v", Message: "1"}))
	wide := svgWidth(t, RenderShield(Shield{Label: "unique callers", Message: "1,234,567"}))
	if narrow >= wide {
		t.Errorf("width did not grow with the text: %v vs %v", narrow, wide)
	}
	// Both plates, plus their padding, and nothing else.
	want := textWidth("txs") + textWidth("42") + 4*shieldPad
	if got := svgWidth(t, RenderShield(Shield{Label: "txs", Message: "42"})); got != math.Round(want) {
		t.Errorf("width = %v, want %v (label + message + four paddings)", got, math.Round(want))
	}
}

func svgWidth(t *testing.T, b []byte) float64 {
	t.Helper()
	var doc struct {
		Width string `xml:"width,attr"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	w, err := strconv.ParseFloat(doc.Width, 64)
	if err != nil {
		t.Fatalf("width attribute %q is not a number: %v", doc.Width, err)
	}
	return w
}

// flat-square is the same numbers in a different shape: no mask, no shadow.
func TestRenderShieldFlatSquareDropsTheMaskAndTheShadow(t *testing.T) {
	got := string(RenderShield(Shield{Label: "txs", Message: "42", Style: ShieldFlatSquare}))
	parseSVG(t, []byte(got))
	if strings.Contains(got, "mask") {
		t.Errorf("flat-square is not rounded, so it needs no mask:\n%s", got)
	}
	if strings.Contains(got, "fill-opacity") {
		t.Errorf("flat-square has no text shadow:\n%s", got)
	}
	if strings.Count(got, "<text") != 2 {
		t.Errorf("want one <text> per plate, got %d:\n%s", strings.Count(got, "<text"), got)
	}
}

// Everything a badge prints came off a chain or a query string. One escaper,
// applied where text becomes markup, is the package's rule; this is the shield
// half of it.
func TestRenderShieldEscapesEverythingItIsGiven(t *testing.T) {
	got := RenderShield(Shield{
		Label:   `a"><script>alert(1)</script>`,
		Message: `b&<c`,
		Color:   `red" onload="alert(1)`,
	})
	parseSVG(t, got)
	s := string(got)
	if strings.Contains(s, "<script") {
		t.Errorf("a script tag survived:\n%s", s)
	}
	if strings.Contains(s, "onload") {
		t.Errorf("a colour smuggled an attribute into the document:\n%s", s)
	}
	if !strings.Contains(s, "&amp;") {
		t.Errorf("an ampersand was not escaped:\n%s", s)
	}
}

func TestShieldColor(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"a shields name", "brightgreen", "#4c1"},
		{"a semantic alias", "critical", "#e05d44"},
		{"case and space are forgiven", "  Blue ", "#007ec6"},
		{"a bare hex triplet", "4c1", "#4c1"},
		{"a hex triplet with its hash", "#007ec6", "#007ec6"},
		{"eight digits, for alpha", "007ec680", "#007ec680"},
		{"a colour nobody named", "chartreuse", "#111"},
		{"markup dressed as a colour", `red" onload="x`, "#111"},
		{"five digits is not a colour", "12345", "#111"},
		{"empty falls back", "", "#111"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shieldColor(tt.in, "#111"); got != tt.want {
				t.Errorf("shieldColor(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The width table is what decides the plate, so the properties that matter are
// that it is monotonic in the text and that nothing measures as free.
func TestTextWidth(t *testing.T) {
	if got := textWidth(""); got != 0 {
		t.Errorf("textWidth(\"\") = %v, want 0", got)
	}
	if textWidth("ll") >= textWidth("mm") {
		t.Error("an l measured as wide as an m, so the table is not being read")
	}
	if textWidth("1234") <= textWidth("123") {
		t.Error("width did not grow with a fourth digit")
	}
	// Outside the table (a CJK label, an emoji in a realm name) still has to
	// produce a plate rather than a zero-width one.
	if got := textWidth("実"); got <= 0 {
		t.Errorf("textWidth(non-ASCII) = %v, want a positive fallback", got)
	}
}

func TestParseShieldStyle(t *testing.T) {
	for in, want := range map[string]ShieldStyle{
		"":              ShieldFlat,
		"flat":          ShieldFlat,
		"FLAT-SQUARE":   ShieldFlatSquare,
		" flat-square ": ShieldFlatSquare,
		"for-the-badge": ShieldFlat, // not drawn here: see the endpoint route
	} {
		if got := ParseShieldStyle(in); got != want {
			t.Errorf("ParseShieldStyle(%q) = %q, want %q", in, got, want)
		}
	}
}

// A shield with only one side is a legitimate badge (shields draws it when the
// label is empty), and the empty plate must not leave a stub of colour.
func TestRenderShieldWithNoLabel(t *testing.T) {
	got := string(RenderShield(Shield{Message: "live", Color: "brightgreen"}))
	parseSVG(t, []byte(got))
	if strings.Count(got, "<text") != 2 { // the message and its shadow
		t.Errorf("want only the message drawn, got %d <text>:\n%s", strings.Count(got, "<text"), got)
	}
	if !strings.Contains(got, `aria-label="live"`) {
		t.Errorf("the description still names an absent label:\n%s", got)
	}
	// And no stub of the label plate: padding around nothing is a grey sliver
	// down the left of the badge.
	want := textWidth("live") + 2*shieldPad
	if w := svgWidth(t, []byte(got)); w != math.Round(want) {
		t.Errorf("width = %v, want %v (the message plate and nothing else)", w, math.Round(want))
	}
}
