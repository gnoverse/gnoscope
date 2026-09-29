package store

import "testing"

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
