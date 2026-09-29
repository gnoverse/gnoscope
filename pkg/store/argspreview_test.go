package store

import (
	"strings"
	"testing"
)

// TestBuildArgsPreview pins the two bounds and the one thing a reader must
// never be able to mistake: a shortened value for the value.
func TestBuildArgsPreview(t *testing.T) {
	long := ""
	for len(long) < 200 {
		long += "abcdefghij"
	}
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"none", nil, ""},
		{"empty slice", []string{}, ""},
		{"one short", []string{"g1alice"}, "g1alice"},
		{"several short", []string{"g1alice", "1000000ugnot", "true"}, "g1alice, 1000000ugnot, true"},
		{"an empty argument is still an argument", []string{"", "x"}, ", x"},
		// Whitespace is flattened: an argument carrying a .gno file would
		// otherwise make the row several lines tall.
		{"newlines flattened", []string{"package main\n\nfunc main() {}"}, "package main func main() {}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildArgsPreview(tt.args); got != tt.want {
				t.Errorf("BuildArgsPreview(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestBuildArgsPreviewKeepsIdentifiersWhole is the point of the three budgets.
//
// A shortened address on the page is derived in the browser from the whole one,
// so an address cut here is not a short address, it is a dead string: not
// linkable, not copyable, not even recognisable. v1 cut everything at 32 and
// turned every address into exactly that.
func TestBuildArgsPreviewKeepsIdentifiersWhole(t *testing.T) {
	const addr = "g1vc883gshu5z7ytk5cdynhc8c2dhqnfhsmnhpaz"
	const path = "gno.land/r/moul/x/upgrade/schema/impl/bad/v0"

	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"an address survives whole", []string{addr}, addr},
		{"a path survives whole", []string{path}, path},
		{"an address beside a number", []string{addr, "1306556088"}, addr + ", 1306556088"},
		{"two addresses", []string{addr, addr}, addr + ", " + addr},
		{"a path beside an address", []string{path, addr}, path + ", " + addr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildArgsPreview(tt.args)
			if got != tt.want {
				t.Errorf("BuildArgsPreview(%q)\n got %q\nwant %q", tt.args, got, tt.want)
			}
		})
	}

	// And free-form values keep the tight bound, so a markdown body cannot
	// crowd out the address behind it.
	body := strings.Repeat("x", 500)
	got := BuildArgsPreview([]string{body, addr})
	if !strings.Contains(got, addr) {
		t.Errorf("BuildArgsPreview(long, addr) = %q, want the address still present", got)
	}
	if len(got) > argsPreviewMax {
		t.Errorf("BuildArgsPreview(long, addr) = %d bytes, want <= %d", len(got), argsPreviewMax)
	}
}

// TestLooksLikeAddress pins the shape test, including what it must reject: the
// budget it picks is the difference between a linkable identifier and a dead
// string, so a false positive spends the whole list budget on one value.
func TestLooksLikeAddress(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"g1vc883gshu5z7ytk5cdynhc8c2dhqnfhsmnhpaz", true},
		{"", false},
		{"g1", false},
		{"g1tooshort", false},
		{"g1vc883gshu5z7ytk5cdynhc8c2dhqnfhsmnhpazEXTRA", false},
		// bech32 excludes 1, b, i and o; a string of the right length that uses
		// them is not an address however much it looks like one.
		{"g1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", false},
		{"gno.land/r/gnoswap/position", false},
	} {
		if got := looksLikeAddress(tt.in); got != tt.want {
			t.Errorf("looksLikeAddress(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestBuildArgsPreviewBounds(t *testing.T) {
	long := ""
	for len(long) < 500 {
		long += "abcdefghij"
	}

	// One huge argument is cut to the per-argument bound, not the whole budget,
	// so the arguments behind it still have room.
	one := BuildArgsPreview([]string{long})
	if len(one) > argPreviewMax {
		t.Errorf("single arg = %d bytes (%q), want <= %d", len(one), one, argPreviewMax)
	}
	if !hasEllipsis(one) {
		t.Errorf("single arg = %q, want a visible ellipsis: a cut value must not read as the value", one)
	}

	// The whole preview is bounded however many arguments there are.
	many := BuildArgsPreview([]string{long, long, long, long, long})
	if len(many) > argsPreviewMax {
		t.Errorf("many args = %d bytes, want <= %d", len(many), argsPreviewMax)
	}
	if !hasEllipsis(many) {
		t.Errorf("many args = %q, want a visible ellipsis", many)
	}

	// A multi-byte argument is never cut mid-rune: the result has to stay valid
	// UTF-8, or the cell renders a replacement glyph.
	emoji := ""
	for i := 0; i < 80; i++ {
		emoji += "é"
	}
	got := BuildArgsPreview([]string{emoji})
	for i, r := range got {
		if r == 0xFFFD {
			t.Fatalf("rune at %d is U+FFFD: %q was cut mid-rune", i, got)
		}
	}
}

func hasEllipsis(s string) bool {
	for _, r := range s {
		if r == '…' {
			return true
		}
	}
	return false
}
