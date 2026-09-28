// Package badge draws the small SVG cards other documents embed.
//
// The point is a graph that survives leaving mygnoscan. A realm's Render()
// returns markdown, gnoweb turns it into HTML, and an `![](…)` is the only
// hook a realm has into anything the chain does not store. So the unit here is
// an image, not a component: whatever a browser will paint inside an <img> and
// nothing else.
//
// That constraint is sharper than it looks. An SVG loaded through <img> runs in
// what the SVG spec calls secure animated mode: no scripts, no external
// references, no web fonts, no fetches of any kind. Anything this package emits
// has to be self-contained down to the last glyph, which is why there is no
// font file here and no <use xlink:href>, and why the numbers are laid out by
// hand rather than by measuring text.
//
// It also has to be inert as a *document*. Served at its own URL it is a
// top-level document on mygnoscan's origin, and every string it interpolates
// (a package path off the chain, a label off a query string) is attacker
// controlled. So: one escaper, applied at the single point where text becomes
// markup, and no code path that writes a caller's bytes any other way.
package badge

import (
	"fmt"
	"strings"
)

// Theme selects the palette. Auto emits both and lets the reading document's
// prefers-color-scheme decide, which is what a badge embedded in a page whose
// theme we cannot know wants.
type Theme string

const (
	ThemeAuto  Theme = "auto"
	ThemeLight Theme = "light"
	ThemeDark  Theme = "dark"
)

// ParseTheme maps a query-string value to a Theme, defaulting to auto.
//
// An unrecognised value falls back rather than erroring, for the same reason
// the API's filters do: these arrive from links people wrote by hand, and a
// badge that renders in the wrong palette is a far better outcome than one that
// does not render at all.
func ParseTheme(s string) Theme {
	switch Theme(strings.ToLower(strings.TrimSpace(s))) {
	case ThemeLight:
		return ThemeLight
	case ThemeDark:
		return ThemeDark
	default:
		return ThemeAuto
	}
}

// Point is one bucket of a series.
//
// Label is what the bucket is called on the chain's own terms (a date, an
// hour); the card never prints it, but it goes into the accessible description
// of the first and last bucket so a screen reader is told the window rather
// than being handed "a graph".
type Point struct {
	Label string
	Value float64
}

// Card is everything one badge says.
//
// Headline and Sub are two lines of text, not a format string: the caller knows
// what its numbers mean and this package has no business inventing a sentence
// about them. Empty ones are skipped and the rest moves up.
type Card struct {
	// Title is the small line at the top, usually the subject's path.
	Title string
	// Headline is the number this badge exists to show.
	Headline string
	// Sub qualifies the headline: the window, the unit, the caveat.
	Sub string
	// Series is drawn left to right, oldest first. Buckets MUST be dense:
	// a day with no traffic belongs here as a zero. Handed a sparse series,
	// the sparkline closes its own gaps and draws a gentle slope across a
	// fortnight of silence, which is a lie a reader has no way to detect.
	Series []Point
	// Empty is the message drawn in place of the sparkline when there is
	// nothing to draw. "no activity" reads as an answer; an empty plot area
	// reads as a broken badge.
	Empty string
	Theme Theme
}

// Card geometry. Fixed rather than fitted to the content: an <img> with no
// width attribute is laid out at the SVG's own intrinsic size, so a badge whose
// width tracked the length of a package path would make the surrounding
// paragraph reflow every time a realm was renamed.
const (
	cardW = 480
	cardH = 120

	padX = 14
	// plotTop leaves room for the title and headline above the graph.
	plotTop    = 58
	plotBottom = cardH - 22
)

// Render draws the card and returns a complete SVG document.
func Render(c Card) []byte {
	var b strings.Builder
	b.Grow(2048)

	desc := describe(c)

	// role="img" plus aria-label is what a screen reader in a page that
	// embedded this with an empty alt still has to go on. <title> is the
	// tooltip a sighted reader gets on hover, and the two say the same thing
	// deliberately.
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s">`,
		cardW, cardH, cardW, cardH, esc(desc))
	fmt.Fprintf(&b, `<title>%s</title>`, esc(desc))
	b.WriteString(style(c.Theme))

	fmt.Fprintf(&b, `<rect class="bg" x="0.5" y="0.5" width="%d" height="%d" rx="8"/>`, cardW-1, cardH-1)

	y := 24
	if c.Title != "" {
		fmt.Fprintf(&b, `<text class="t" x="%d" y="%d">%s</text>`, padX, y, esc(truncate(c.Title, 58)))
		y += 24
	}
	if c.Headline != "" {
		fmt.Fprintf(&b, `<text class="h" x="%d" y="%d">%s</text>`, padX, y, esc(truncate(c.Headline, 40)))
	}

	b.WriteString(sparkline(c))

	if c.Sub != "" {
		fmt.Fprintf(&b, `<text class="s" x="%d" y="%d">%s</text>`, padX, cardH-8, esc(truncate(c.Sub, 64)))
	}

	b.WriteString(`</svg>`)
	return []byte(b.String())
}

// describe builds the one sentence that stands in for the whole card when it
// cannot be seen. Assembled from the same fields the card draws, so a badge
// cannot say one thing visually and another to a screen reader.
func describe(c Card) string {
	parts := make([]string, 0, 3)
	for _, s := range []string{c.Title, c.Headline, c.Sub} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "mygnoscan badge"
	}
	return strings.Join(parts, " · ")
}

// style emits the palette.
//
// A media query inside an SVG *is* evaluated when that SVG is loaded through
// <img>: the referenced document gets its own CSS environment and
// prefers-color-scheme resolves against the user's system setting. That is the
// whole reason auto is the default and is also its one limitation, since the
// embedding page's own light/dark toggle is invisible from in here. A caller
// that knows better passes theme=light or theme=dark and gets one palette with
// no query at all.
func style(t Theme) string {
	const light = `.bg{fill:#ffffff;stroke:#d8dee4}.t{fill:#6a737d}.h{fill:#1f2328}.s{fill:#8b949e}.line{stroke:#2f6feb}.area{fill:#2f6feb}.dot{fill:#2f6feb}.grid{stroke:#eaeef2}`
	const dark = `.bg{fill:#0d1117;stroke:#30363d}.t{fill:#8b949e}.h{fill:#e6edf3}.s{fill:#6e7681}.line{stroke:#58a6ff}.area{fill:#58a6ff}.dot{fill:#58a6ff}.grid{stroke:#21262d}`
	// No font file can be loaded from in here, so the stack has to name faces
	// the reader already has. Same list GitHub ships, for the same reason:
	// whatever it lands on, the metrics are close enough that the fixed
	// geometry above still looks deliberate.
	const base = `text{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}` +
		`.t{font-size:11px}.h{font-size:20px;font-weight:600}.s{font-size:10px}` +
		`.bg{stroke-width:1}.line{fill:none;stroke-width:2;stroke-linejoin:round;stroke-linecap:round}` +
		`.area{opacity:.14}.grid{stroke-width:1;stroke-dasharray:2 3}`

	switch t {
	case ThemeLight:
		return `<style>` + base + light + `</style>`
	case ThemeDark:
		return `<style>` + base + dark + `</style>`
	default:
		return `<style>:root{color-scheme:light dark}` + base + light +
			`@media(prefers-color-scheme:dark){` + dark + `}</style>`
	}
}

// sparkline draws the series, or says why it did not.
func sparkline(c Card) string {
	plotW := float64(cardW - 2*padX)
	base := float64(plotBottom)
	top := float64(plotTop)

	// One point is not a line. Drawing it as one would produce a flat segment
	// spanning the full window, which claims a history the series does not
	// have, so it falls into the same branch as no points at all.
	if len(c.Series) < 2 {
		msg := c.Empty
		if msg == "" {
			msg = "not enough history to draw"
		}
		return fmt.Sprintf(`<line class="grid" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/>`+
			`<text class="s" x="%d" y="%.1f">%s</text>`,
			padX, base, cardW-padX, base, padX, top+14, esc(truncate(msg, 60)))
	}

	max := 0.0
	for _, p := range c.Series {
		if p.Value > max {
			max = p.Value
		}
	}

	// An all-zero window is a real answer and gets a real baseline. Scaling by
	// a max of zero would divide by it; scaling by 1 instead would draw the
	// flat line at the *bottom* of the plot, which is exactly right.
	scale := max
	if scale <= 0 {
		scale = 1
	}

	// A flat line on the baseline is correct and unreadable: it looks
	// identical to a graph that failed to draw. So when nothing happened at
	// all, the line is still drawn and the reason is written over it.
	var label string
	if max == 0 && c.Empty != "" {
		label = fmt.Sprintf(`<text class="s" x="%d" y="%.1f">%s</text>`,
			padX, base-10, esc(truncate(c.Empty, 60)))
	}

	n := len(c.Series)
	step := plotW / float64(n-1)
	x := func(i int) float64 { return float64(padX) + step*float64(i) }
	y := func(v float64) float64 { return base - (v/scale)*(base-top) }

	var line strings.Builder
	for i, p := range c.Series {
		if i == 0 {
			fmt.Fprintf(&line, "M%.1f %.1f", x(i), y(p.Value))
			continue
		}
		fmt.Fprintf(&line, "L%.1f %.1f", x(i), y(p.Value))
	}

	// The area is the same path closed down to the baseline. Built from the
	// line rather than plotted again so the two can never disagree by a pixel.
	area := line.String() +
		fmt.Sprintf("L%.1f %.1fL%.1f %.1fZ", x(n-1), base, x(0), base)

	last := c.Series[n-1]
	return fmt.Sprintf(`<line class="grid" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/>`+
		`<path class="area" d="%s"/><path class="line" d="%s"/>`+
		`<circle class="dot" cx="%.1f" cy="%.1f" r="2.5"/>%s`,
		padX, base, cardW-padX, base,
		area, line.String(),
		x(n-1), y(last.Value), label)
}

// truncate caps a string at n runes, ending in an ellipsis when it cut.
//
// Runes, not bytes: package paths are ASCII but titles and labels are not
// guaranteed to be, and cutting a multi-byte sequence in half would put invalid
// UTF-8 into an XML document. Counted rather than measured because there is no
// font here to measure against (see the package comment).
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

// esc is the single point where a caller's bytes become markup.
//
// Both the text-node set and the attribute set, in one function, because the
// only thing worse than escaping too much in an SVG is having two escapers and
// calling the wrong one. `'` is escaped as the numeric reference rather than
// &apos;: that entity is not predefined in HTML, and an SVG can be parsed by
// an HTML parser when it is inlined.
//
// C0 controls other than tab, newline and carriage return are not
// representable in XML 1.0 at all, by any escape, so they are dropped. A
// package path containing one is already not a package path.
func esc(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\'':
			b.WriteString("&#39;")
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
