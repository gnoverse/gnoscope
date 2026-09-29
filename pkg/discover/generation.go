package discover

import (
	"strconv"
	"strings"
)

// Generations: which realms are the same app at different dates.
//
// A chain nobody can delete from accumulates generations, and the directory's
// answer to that was a hand-written `supersedes` list in apps.json. That list
// is a maintenance debt with a predictable failure: bubblerumble5 shipped and
// the page ranked it as a sixth game beside the four it replaces, because
// nobody had edited the file yet. The relation is derivable. `bubblerumble5`
// is obviously the generation after `bubblerumble4`, and a directory that
// makes a human say so is asking for an edit it could have computed.
//
// What makes deriving it safe is how narrow the rule is. Two realms are the
// same family only when every segment of their paths matches after the
// generation number is removed, which means the same namespace, the same
// nesting and the same name. `bubble` and `bubblerumble` are not a family,
// `wbubble` is not either, and neither is one person's `gems/v1` and another
// person's. Curation still wins wherever it speaks, and the moderation list
// still removes.

// genMaxSegmentDigits caps how long a run of trailing digits may be and still
// read as a generation.
//
// `erc721` ends in three digits and is a standard's number, not a version of
// `erc`. Two digits covers every generation anybody has deployed (v0 to v99)
// and refuses the four-digit years and the token standards, which are the two
// shapes that would otherwise fold unrelated realms together.
const genMaxSegmentDigits = 2

// Generation splits a package path into the family it belongs to and which
// generation of it this path is.
//
// The family is the path with every generation number removed, and a pure
// version segment removed entirely: `gno.land/r/ns/boards2/v0` and
// `gno.land/r/ns/boards/v0` are both the family `gno.land/r/ns/boards`, at
// generations [2 0] and [0 0]. The vector is per segment and compared left to
// right, so a version segment nested inside a path (gnoswap deploys as
// `gnoswap/v1/position`) orders correctly without a special case.
//
// ok is false for anything that is not a `gno.land/r/...` or `/p/...` path with
// a namespace and at least one segment under it. Those have no family to be a
// generation of.
func Generation(path string) (family string, gen []int, ok bool) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) < 4 || (segs[1] != "r" && segs[1] != "p") {
		return "", nil, false
	}
	// The namespace is who deployed it, never a generation of anything: two
	// accounts of one person (`nym-thegnomic001`, `nym-thegnomic002`) are not
	// two versions of an app, and folding them would merge two people's realms
	// on the strength of a suffix.
	keep := append([]string{}, segs[:3]...)
	gen = make([]int, 0, len(segs)-3)
	for _, seg := range segs[3:] {
		stem, n := splitGeneration(seg)
		gen = append(gen, n)
		if stem != "" {
			keep = append(keep, stem)
		}
	}
	return strings.Join(keep, "/"), gen, true
}

// splitGeneration takes one path segment apart into its stem and its
// generation number.
//
// A bare version segment (`v0`, `v12`) has no stem: it names a generation and
// nothing else, so it leaves the family key entirely. That is what lets
// `ns/boards/v0` and `ns/boards2/v0` be one family rather than two, which is
// the shape a rename-and-bump actually produces.
func splitGeneration(seg string) (stem string, gen int) {
	i := len(seg)
	for i > 0 && seg[i-1] >= '0' && seg[i-1] <= '9' {
		i--
	}
	digits := seg[i:]
	if digits == "" || len(digits) > genMaxSegmentDigits {
		return seg, 0
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return seg, 0
	}
	stem = seg[:i]
	// `padv3` is generation 3 of `pad`, so the `v` goes with the number. A
	// segment that is only `v` plus digits is a pure version segment and has
	// no stem left at all.
	if strings.HasSuffix(stem, "v") {
		stem = stem[:len(stem)-1]
	}
	return stem, n
}

// GenerationLess orders two generation vectors, shorter ones zero-padded.
//
// Padding rather than treating a missing segment as unknown, because
// `ns/foo` and `ns/foo/v0` really are the same generation of the same family
// and the pair must compare equal: an edge between them would claim one
// replaced the other on no evidence but a path shape.
func GenerationLess(a, b []int) bool {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}
