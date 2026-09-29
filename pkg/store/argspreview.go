package store

import "strings"

// argsPreviewMax bounds what BuildArgsPreview will return, in bytes.
//
// The column exists to tell one row apart from the fifty around it, not to
// reproduce the call. 96 bytes is enough for the shapes that actually repeat on
// chain (an address and an amount, a path and a flag) and small enough that the
// whole column stays a rounding error next to package_files.
const argsPreviewMax = 96

// argPreviewMax bounds a single argument, so one long blob cannot crowd out
// every argument after it. A call carrying a 4 KB markdown body as its first
// argument would otherwise render as that body, truncated, and say nothing
// about the three arguments behind it.
const argPreviewMax = 32

// BuildArgsPreview renders a call's arguments as a short, already-truncated
// human string: `alice, 1000000ugnot, tru…`.
//
// Truncation is marked, always, and with the same ellipsis at both levels, so a
// reader can never mistake a shortened value for the value. Nothing downstream
// may parse this back: see the column comment in schema.go.
//
// The empty string means "no arguments", which is also what an un-backfilled
// historical row holds. The two are deliberately indistinguishable, because a
// row that says nothing is the honest rendering of a row we know nothing about.
func BuildArgsPreview(args []string) string {
	if len(args) == 0 {
		return ""
	}
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		// Newlines and tabs turn a one-line table cell into a ragged block, and
		// an argument carrying a whole .gno file is a real shape on this chain.
		a = strings.Join(strings.Fields(a), " ")
		b.WriteString(truncRunes(a, argPreviewMax))
		if b.Len() >= argsPreviewMax {
			break
		}
	}
	return truncRunes(b.String(), argsPreviewMax)
}

// truncRunes cuts at a rune boundary rather than a byte one, so a multi-byte
// character is never split into a replacement glyph, and marks the cut.
func truncRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	// max is a byte budget, so walk runes until the budget is spent rather than
	// indexing by it: a string of 3-byte runes has a third as many as bytes.
	n, used := 0, 0
	for n < len(r) {
		w := len(string(r[n]))
		if used+w > max-3 {
			break
		}
		used += w
		n++
	}
	return string(r[:n]) + "…"
}
