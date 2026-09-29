package discover

import "testing"

func TestGeneration(t *testing.T) {
	// Every path here is on mainnet, read off /api/apps on 2026-09-29. The
	// pairs that must NOT fold are the point of the table: a rule this cheap
	// earns its keep only if it refuses the lookalikes.
	tests := []struct {
		path   string
		family string
		gen    []int
		ok     bool
	}{
		{"gno.land/r/g1leu8/bubblerumble", "gno.land/r/g1leu8/bubblerumble", []int{0}, true},
		{"gno.land/r/g1leu8/bubblerumble2", "gno.land/r/g1leu8/bubblerumble", []int{2}, true},
		{"gno.land/r/g1leu8/bubblerumble5", "gno.land/r/g1leu8/bubblerumble", []int{5}, true},
		// Not the same family, and the reason the stem is compared whole.
		{"gno.land/r/g1leu8/bubble", "gno.land/r/g1leu8/bubble", []int{0}, true},
		{"gno.land/r/g1leu8/wbubble", "gno.land/r/g1leu8/wbubble", []int{0}, true},
		// The `v` belongs to the number.
		{"gno.land/r/g1n4pl/gnomi/pad", "gno.land/r/g1n4pl/gnomi/pad", []int{0, 0}, true},
		{"gno.land/r/g1n4pl/gnomi/padv3", "gno.land/r/g1n4pl/gnomi/pad", []int{0, 3}, true},
		{"gno.land/r/g1leu8/kourtv3", "gno.land/r/g1leu8/kourt", []int{3}, true},
		// A bare version segment leaves the family key.
		{"gno.land/r/gnoland/boards/v0", "gno.land/r/gnoland/boards", []int{0, 0}, true},
		{"gno.land/r/gnoland/boards2/v0", "gno.land/r/gnoland/boards", []int{2, 0}, true},
		// Nested version, gnoswap's shape.
		{"gno.land/r/gnoswap/v1/position", "gno.land/r/gnoswap/position", []int{1, 0}, true},
		// A standard's number is not a generation.
		{"gno.land/r/moul/x/daily/erc721/v0", "gno.land/r/moul/x/daily/erc721", []int{0, 0, 0, 0}, true},
		// The namespace is never a generation: two accounts, not two versions.
		{"gno.land/r/nym-thegnomic001/gnomic", "gno.land/r/nym-thegnomic001/gnomic", []int{0}, true},
		{"gno.land/r/nym-thegnomic002/gnomic", "gno.land/r/nym-thegnomic002/gnomic", []int{0}, true},
		// A bump on a non-final segment still folds.
		{"gno.land/r/g1wx60/trialmint/stable", "gno.land/r/g1wx60/trialmint/stable", []int{0, 0}, true},
		{"gno.land/r/g1wx60/trialmint2/stable", "gno.land/r/g1wx60/trialmint/stable", []int{2, 0}, true},
		// ...but a different leaf is a different family.
		{"gno.land/r/g1wx60/trialmint/v1", "gno.land/r/g1wx60/trialmint", []int{0, 1}, true},
		// Too short, or not a package path at all.
		{"gno.land/r/moul", "", nil, false},
		{"https://example.com/x/y/z", "", nil, false},
		{"", "", nil, false},
	}
	for _, tt := range tests {
		family, gen, ok := Generation(tt.path)
		if ok != tt.ok || family != tt.family || !sameInts(gen, tt.gen) {
			t.Errorf("Generation(%q) = %q %v %v, want %q %v %v",
				tt.path, family, gen, ok, tt.family, tt.gen, tt.ok)
		}
	}
}

func TestGenerationFamiliesMatch(t *testing.T) {
	// The pairs the derivation depends on, stated as the question it asks:
	// same family, and which one is newer.
	pairs := []struct {
		older, newer string
		sameFamily   bool
	}{
		{"gno.land/r/g1leu8/bubblerumble4", "gno.land/r/g1leu8/bubblerumble5", true},
		{"gno.land/r/g1leu8/bubble", "gno.land/r/g1leu8/bubblerumble5", false},
		{"gno.land/r/g1leu8/wbubble", "gno.land/r/g1leu8/bubblerumble5", false},
		{"gno.land/r/g1n4pl/gnomi/padv2", "gno.land/r/g1n4pl/gnomi/padv3", true},
		{"gno.land/r/g1ecsuj/kourt", "gno.land/r/g1leu8/kourtv3", false}, // different deployer
		{"gno.land/r/g17cjym/gems/stable", "gno.land/r/g17cjym/gems/v1", false},
	}
	for _, p := range pairs {
		fo, go_, ooko := Generation(p.older)
		fn, gn, okn := Generation(p.newer)
		if !ooko || !okn {
			t.Fatalf("both should parse: %q %q", p.older, p.newer)
		}
		if (fo == fn) != p.sameFamily {
			t.Errorf("%q vs %q: family %q vs %q, sameFamily=%v want %v",
				p.older, p.newer, fo, fn, fo == fn, p.sameFamily)
		}
		if p.sameFamily && !GenerationLess(go_, gn) {
			t.Errorf("%q should be older than %q (%v vs %v)", p.older, p.newer, go_, gn)
		}
	}
}

func TestGenerationLessPadsWithZeros(t *testing.T) {
	// `ns/foo` and `ns/foo/v0` are the same generation and must not order,
	// in either direction: an edge between them would be invented.
	if GenerationLess([]int{0}, []int{0, 0}) || GenerationLess([]int{0, 0}, []int{0}) {
		t.Error("equal-after-padding vectors must not compare less in either direction")
	}
	if !GenerationLess([]int{0, 2}, []int{1, 0}) {
		t.Error("leftmost segment is the most significant")
	}
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
