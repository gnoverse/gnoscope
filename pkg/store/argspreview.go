package store

import "strings"

// The per-argument budgets, in bytes. Three of them, because cutting every
// argument to one length is what made the first version of this useless for the
// two kinds of argument anybody wants to click.
//
// A gno address is `g1` plus 38 characters, exactly 40, and a *shortened*
// address is still a link only if the full one survived: the display form
// (`g1vc883g…c2dh`) is derived in the browser from the whole thing. Cut at 32,
// what reached the page was `g1vc883gshu5z7ytk5cdynhc8c2dh…`, which is not an
// address, cannot be linked, and cannot even be copied. Same for a realm path,
// where the tail is the part that says which realm.
//
// So an address is never truncated, a path gets enough room for the shapes that
// exist on chain (`gno.land/r/moul/x/upgrade/schema/impl/bad/v0` is 43), and
// free-form values keep the tight bound, since a 4 KB markdown body is a real
// argument here and nothing is lost by cutting it.
const (
	argPreviewMax    = 32
	argAddressMax    = 48
	argPathMax       = 72
	argsPreviewMax   = 224
	addressLen       = 40
	gnoAddressPrefix = "g1"
	gnoPathPrefix    = "gno.land/"
)

// argsPass names the truncation rules that produced the rows currently in
// calls.args. Bump it whenever those rules change in a way that would give a
// different answer, and every call is offered to the backfill once more.
//
// Same idea as dependencyExtractorVersion in pkg/analyzer, and for the same
// reason: sync only moves forward, so a row written under the old rules is
// never revisited otherwise. v1 cut every argument at 32 bytes and so destroyed
// every address it touched; v2 keeps addresses and paths whole.
const argsPass = 2

// argBudget is how much of one argument is worth keeping, by what it looks like.
func argBudget(a string) int {
	switch {
	case looksLikeAddress(a):
		return argAddressMax
	case strings.Contains(a, gnoPathPrefix):
		return argPathMax
	default:
		return argPreviewMax
	}
}

// looksLikeAddress is deliberately shape-only: the right length, the right
// prefix, and bech32's alphabet. It never validates the checksum, because this
// is deciding how much of a string to keep, not whether an account exists, and
// a malformed address the chain rejected is still what the caller typed.
func looksLikeAddress(s string) bool {
	if len(s) != addressLen || !strings.HasPrefix(s, gnoAddressPrefix) {
		return false
	}
	for _, r := range s[2:] {
		if !strings.ContainsRune("023456789acdefghjklmnpqrstuvwxyz", r) {
			return false
		}
	}
	return true
}

// BuildArgsPreview renders a call's arguments as a short, mostly-truncated
// human string: `g1alice…, 1000000ugnot, tru…`.
//
// Truncation is marked, always, with the same ellipsis at both levels, so a
// reader can never mistake a shortened value for the value. Addresses and realm
// paths are the exception and are kept whole: the browser shortens them for
// display and needs the original to link to. Nothing downstream may parse this
// back; see the column comment in schema.go.
//
// The empty string means "no arguments", which is also what a row the backfill
// has not reached holds. The two are deliberately indistinguishable, because a
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
		b.WriteString(truncRunes(a, argBudget(a)))
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
