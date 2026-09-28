package badge

import (
	"fmt"
	"strings"
)

// Shields: the other badge shape, the one a README already knows how to read.
//
// Card (Render, above) answers "how is this realm doing" with a graph. A shield
// answers one question with one word, in the 20-pixel plate shields.io made the
// convention of every repository's front page. The two are not variants of each
// other: a card is a figure in a document, a shield sits in a row of six at the
// top of a README and has to be legible at that size, next to a CI badge drawn
// by somebody else.
//
// Geometry is copied from shields.io on purpose, down to the 11px Verdana stack
// and the one-pixel text shadow, because the value of this shape is that it is
// already familiar. A badge that is nearly the same height, or whose corners are
// nearly the same radius, reads as a knock-off of the row it is standing in.
//
// The same secure-animated-mode constraint as the rest of this package applies:
// no fonts to load, no external references, nothing fetched. Which is why the
// text is measured here, in Go, against a table of Verdana advance widths, and
// then pinned with textLength so a reader whose system substitutes a different
// face still gets text that fills its plate instead of spilling out of it.

// ShieldStyle selects the plate's shape.
type ShieldStyle string

const (
	// ShieldFlat is shields.io's default: rounded corners, a text shadow.
	ShieldFlat ShieldStyle = "flat"
	// ShieldFlatSquare is the same plates with square corners and no shadow.
	ShieldFlatSquare ShieldStyle = "flat-square"
)

// ParseShieldStyle maps a query-string value to a style, defaulting to flat.
//
// Falls back rather than erroring, like ParseTheme and for the same reason:
// these URLs are typed by hand into a README and a badge in the wrong shape
// beats no badge at all.
func ParseShieldStyle(s string) ShieldStyle {
	if ShieldStyle(strings.ToLower(strings.TrimSpace(s))) == ShieldFlatSquare {
		return ShieldFlatSquare
	}
	return ShieldFlat
}

// Shield is one label/message pair.
type Shield struct {
	// Label is the left plate: what is being measured.
	Label string
	// Message is the right plate: the answer.
	Message string
	// Color fills the message plate. A shields.io colour name
	// ("brightgreen", "blue", …) or a bare hex triplet ("4c1", "007ec6").
	// Anything else falls back to grey, deliberately: an unreadable colour
	// name is a typo in a README, not a reason to serve nothing.
	Color string
	// LabelColor fills the label plate. Empty means shields' own #555.
	LabelColor string
	Style      ShieldStyle
}

// Shield geometry, in the units the SVG is emitted in.
const (
	shieldH = 20
	// shieldPad is the space between a plate's edge and its text, per side.
	shieldPad = 6
	// shieldFont is 11px because that is what every other badge in the row is.
	shieldFont = 11
	// Labels and messages are capped before they are measured: a plate wide
	// enough to hold a 200-character on-chain string would push every other
	// badge in the row off the line.
	shieldMaxLabel   = 32
	shieldMaxMessage = 40
)

// RenderShield draws the badge and returns a complete SVG document.
func RenderShield(s Shield) []byte {
	label := truncate(s.Label, shieldMaxLabel)
	message := truncate(s.Message, shieldMaxMessage)

	labelText := textWidth(label)
	msgText := textWidth(message)
	labelW := labelText + 2*shieldPad
	msgW := msgText + 2*shieldPad
	// A plate with no text still has to be a plate: zero width would put the
	// message's rounded corner in the middle of the badge.
	if label == "" {
		labelW = 0
	}
	if message == "" {
		msgW = 0
	}
	total := labelW + msgW

	desc := label
	switch {
	case label == "":
		desc = message
	case message != "":
		desc = label + ": " + message
	}

	var b strings.Builder
	b.Grow(1024)

	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%d" viewBox="0 0 %.0f %d" role="img" aria-label="%s">`,
		total, shieldH, total, shieldH, esc(desc))
	fmt.Fprintf(&b, `<title>%s</title>`, esc(desc))

	labelFill := shieldColor(s.LabelColor, "#555")
	msgFill := shieldColor(s.Color, "#007ec6")

	plates := fmt.Sprintf(`<rect width="%.0f" height="%d" fill="%s"/><rect x="%.0f" width="%.0f" height="%d" fill="%s"/>`,
		labelW, shieldH, labelFill, labelW, msgW, shieldH, msgFill)

	if s.Style == ShieldFlatSquare {
		b.WriteString(plates)
	} else {
		// The rounded corners are a mask rather than rx on each plate: rx on
		// both would round the two edges where the plates meet, leaving a
		// notch down the middle of the badge.
		fmt.Fprintf(&b, `<mask id="m"><rect width="%.0f" height="%d" rx="3" fill="#fff"/></mask><g mask="url(#m)">%s</g>`,
			total, shieldH, plates)
	}

	fmt.Fprintf(&b, `<g fill="#fff" text-anchor="middle" font-family="Verdana,DejaVu Sans,Geneva,sans-serif" font-size="%d">`, shieldFont)
	b.WriteString(shieldText(label, labelW/2, labelText, s.Style))
	b.WriteString(shieldText(message, labelW+msgW/2, msgText, s.Style))
	b.WriteString(`</g></svg>`)

	return []byte(b.String())
}

// shieldText writes one plate's text, centred on x.
//
// textLength plus lengthAdjust pins the run to the width the plate was sized
// for. Without it the badge is only correct on a machine that has Verdana: the
// widths below are Verdana's, and a reader falling back to DejaVu Sans (every
// Linux browser) or Liberation Sans gets a run several pixels wider that
// overhangs the colour it is written on. Pinning trades a hair of glyph
// distortion for text that is always inside its plate.
func shieldText(s string, x, width float64, style ShieldStyle) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	// The shadow is what gives flat its slight relief, and is drawn first so
	// the white run covers it. aria-hidden because a screen reader handed the
	// same string twice reads it twice.
	if style != ShieldFlatSquare {
		fmt.Fprintf(&b, `<text aria-hidden="true" x="%.1f" y="15" fill="#010101" fill-opacity=".3" textLength="%.1f" lengthAdjust="spacingAndGlyphs">%s</text>`,
			x, width, esc(s))
	}
	fmt.Fprintf(&b, `<text x="%.1f" y="14" textLength="%.1f" lengthAdjust="spacingAndGlyphs">%s</text>`,
		x, width, esc(s))
	return b.String()
}

// shieldColors are the names shields.io answers to, so a URL written from
// memory lands on the colour the writer meant.
//
// The semantic names are aliases of the literal ones, same as upstream: a
// badge that says "success" in green beside one that says "brightgreen" in a
// different green would be two products.
var shieldColors = map[string]string{
	"brightgreen":   "#4c1",
	"green":         "#97ca00",
	"yellowgreen":   "#a4a61d",
	"yellow":        "#dfb317",
	"orange":        "#fe7d37",
	"red":           "#e05d44",
	"blue":          "#007ec6",
	"grey":          "#555",
	"gray":          "#555",
	"lightgrey":     "#9f9f9f",
	"lightgray":     "#9f9f9f",
	"blueviolet":    "#8a2be2",
	"success":       "#4c1",
	"important":     "#fe7d37",
	"critical":      "#e05d44",
	"informational": "#007ec6",
	"inactive":      "#9f9f9f",
}

// shieldColor resolves a colour name or hex triplet to something safe to put
// in a fill attribute.
//
// Validated rather than escaped: this value reaches markup, and the set of
// things a fill can legally be is small enough to enumerate. Anything outside
// it falls back, so a caller cannot smuggle bytes into the document through a
// colour.
func shieldColor(c, fallback string) string {
	c = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c), "#")))
	if c == "" {
		return fallback
	}
	if hex, ok := shieldColors[c]; ok {
		return hex
	}
	switch len(c) {
	case 3, 4, 6, 8:
		for _, r := range c {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return fallback
			}
		}
		return "#" + c
	}
	return fallback
}

// textWidth is the advance width of s in 11px Verdana, in pixels.
//
// Measured from a table because there is no font here to measure against (see
// the package comment) and because the answer has to be identical on every
// machine that renders the badge: the width decides the plate, and a plate that
// differed between the server that drew it and the browser that shows it would
// be a badge whose text sits off-centre.
func textWidth(s string) float64 {
	var w float64
	for _, r := range s {
		if v, ok := verdana11[r]; ok {
			w += v
			continue
		}
		// Anything outside the table (accents, CJK, an emoji someone put in a
		// realm name) is assumed to be about as wide as a digit. It will be
		// wrong for CJK, which is roughly twice that, and the pinning in
		// shieldText turns that into squeezed glyphs rather than into text
		// running off the plate.
		w += 7.0
	}
	return w
}

// verdana11 is Verdana's advance width per character at font-size 11, rounded
// to a tenth of a pixel. Only the printable ASCII range: see textWidth for
// everything else.
var verdana11 = map[rune]float64{
	' ': 3.9, '!': 4.3, '"': 5.6, '#': 8.5, '$': 6.9, '%': 11.6, '&': 8.1, '\'': 3.1,
	'(': 4.8, ')': 4.8, '*': 6.9, '+': 8.5, ',': 4.3, '-': 4.8, '.': 4.3, '/': 5.2,
	'0': 6.9, '1': 6.9, '2': 6.9, '3': 6.9, '4': 6.9, '5': 6.9, '6': 6.9, '7': 6.9,
	'8': 6.9, '9': 6.9, ':': 5.2, ';': 5.2, '<': 8.5, '=': 8.5, '>': 8.5, '?': 5.9,
	'@': 12.1, 'A': 7.5, 'B': 7.5, 'C': 7.4, 'D': 8.3, 'E': 6.8, 'F': 6.2, 'G': 8.5,
	'H': 8.2, 'I': 4.6, 'J': 5.0, 'K': 7.6, 'L': 6.1, 'M': 9.4, 'N': 8.0, 'O': 8.8,
	'P': 6.8, 'Q': 8.8, 'R': 7.7, 'S': 7.2, 'T': 6.7, 'U': 8.0, 'V': 7.5, 'W': 11.4,
	'X': 7.5, 'Y': 6.7, 'Z': 7.0, '[': 4.8, '\\': 5.2, ']': 4.8, '^': 8.5, '_': 6.9,
	'`': 6.9, 'a': 6.6, 'b': 6.9, 'c': 5.6, 'd': 6.9, 'e': 6.6, 'f': 4.1, 'g': 6.9,
	'h': 6.7, 'i': 3.0, 'j': 3.8, 'k': 6.4, 'l': 3.0, 'm': 10.3, 'n': 6.7, 'o': 6.6,
	'p': 6.9, 'q': 6.9, 'r': 4.7, 's': 5.6, 't': 4.3, 'u': 6.7, 'v': 6.2, 'w': 9.0,
	'x': 6.2, 'y': 6.2, 'z': 5.6, '{': 6.9, '|': 5.2, '}': 6.9, '~': 8.5,
}
