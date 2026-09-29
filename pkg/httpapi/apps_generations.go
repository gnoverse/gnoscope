package httpapi

import (
	"sort"

	"github.com/gnoverse/gnoscope/pkg/discover"
)

// Deriving the generation relation instead of curating it.
//
// `supersedes` in apps.json is a human saying "v3 replaces v2". It works, and
// it is always late: bubblerumble5 shipped and the hub ranked it as a sixth
// game beside the four it replaces, with a description it inherited from
// nothing, until somebody edited a file. The relation was derivable the whole
// time.
//
// Two passes, and they are separate on purpose. The first one finds the edges.
// The second one carries what a human already wrote forward along them, which
// is the half that makes curation worth doing once instead of once per
// release: the sentence somebody wrote about Bubble Rumble is about the game,
// not about generation four of it.
//
// Curation still wins everywhere it speaks. A derived edge is never added to a
// path a curated entry already claims, and every field carried forward keeps
// the provenance it had, plus the path it came from, so a reader can see that
// nobody wrote this sentence about this realm.

// deriveGenerations adds the supersede edges the paths already imply.
//
// Only between consecutive generations of one family, so a four-deep chain is
// a chain rather than a star: collapseSuperseded follows it to the survivor
// either way, and a chain is what the curated data already looked like.
func deriveGenerations(cards []*AppCard) {
	type member struct {
		card *AppCard
		gen  []int
	}
	families := map[string][]member{}
	for _, c := range cards {
		if c.Path == "" {
			continue
		}
		family, gen, ok := discover.Generation(c.Path)
		if !ok {
			continue
		}
		families[family] = append(families[family], member{c, gen})
	}

	// A path a curated entry already replaces is not up for grabs. Deriving a
	// second edge into it would give the page two answers to "what replaced
	// this", and the curated one is the one a human checked.
	claimed := map[string]bool{}
	for _, c := range cards {
		for _, old := range c.Supersedes {
			claimed[old] = true
		}
	}

	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ms := families[name]
		if len(ms) < 2 {
			continue
		}
		sort.SliceStable(ms, func(i, j int) bool {
			return discover.GenerationLess(ms[i].gen, ms[j].gen)
		})
		for i := 1; i < len(ms); i++ {
			older, newer := ms[i-1], ms[i]
			// Equal after zero-padding: `ns/foo` and `ns/foo/v0` are the same
			// generation of the same family, and an edge between them would be
			// invented out of a path shape.
			if !discover.GenerationLess(older.gen, newer.gen) {
				continue
			}
			if claimed[older.card.Path] {
				continue
			}
			// The chain is the check on the path. A higher number deployed
			// *earlier* is not a successor, it is a numbering scheme this rule
			// does not understand, and guessing there would reorder somebody
			// else's realms on the front page.
			if a, b := newer.card.DeployedAt, older.card.DeployedAt; a != "" && b != "" && a < b {
				continue
			}
			newer.card.Supersedes = append(newer.card.Supersedes, older.card.Path)
			newer.card.SupersedesFrom = mergeProvenance(newer.card.SupersedesFrom, fromDerived)
			claimed[older.card.Path] = true
		}
	}
	for _, c := range cards {
		if len(c.Supersedes) > 0 && c.SupersedesFrom == "" {
			c.SupersedesFrom = fromCurated
		}
	}
}

// mergeProvenance records that a list has entries from more than one source.
func mergeProvenance(have, add string) string {
	// Nothing else writes Supersedes, so anything already there when a derived
	// edge lands was curated.
	switch have {
	case "", add:
		return add
	default:
		return fromCurated + "+" + fromDerived
	}
}

// fieldRank orders the sources a card's fields can come from.
//
// The same order the merge already uses when it overwrites: a human who looked
// at this page beats the community list, which beats the realm's own doc
// comment, which beats its README, which beats a name cut out of the path.
var fieldRank = map[string]int{
	"":            0,
	fromPath:      1,
	fromInferred:  2,
	fromReadme:    3,
	fromChain:     4,
	fromCommunity: 5,
	fromCurated:   6,
}

// inheritAcrossGenerations carries a described generation's fields forward to
// the one that replaced it.
//
// Nearest ancestor first, and only ever an upgrade: a card that has its own
// curated sentence keeps it, and one whose name is still cut out of its path
// takes the name a human wrote for the generation before. Without this, making
// the relation automatic would have been a regression: the new card would rank
// correctly and then say `bubblerumble5` with no description, which is a worse
// page than the stale one it replaced.
func inheritAcrossGenerations(cards []*AppCard) {
	byPath := map[string]*AppCard{}
	replacedBy := map[string]*AppCard{}
	for _, c := range cards {
		if c.Path != "" {
			byPath[c.Path] = c
		}
	}
	for _, c := range cards {
		for _, old := range c.Supersedes {
			replacedBy[old] = c
		}
	}
	for _, c := range cards {
		if c.Path == "" || replacedBy[c.Path] != nil {
			continue // not the survivor of its chain
		}
		for _, anc := range ancestry(c, byPath, len(cards)) {
			adopt(c, anc)
		}
	}
}

// ancestry lists the generations a card replaces, nearest first.
func ancestry(c *AppCard, byPath map[string]*AppCard, limit int) []*AppCard {
	var out []*AppCard
	seen := map[string]bool{c.Path: true}
	queue := append([]string{}, c.Supersedes...)
	for len(queue) > 0 && len(out) < limit {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		seen[path] = true
		anc := byPath[path]
		if anc == nil {
			continue
		}
		out = append(out, anc)
		queue = append(queue, anc.Supersedes...)
	}
	return out
}

// adopt copies anything the ancestor knows better into the survivor.
func adopt(c, anc *AppCard) {
	took := false
	take := func(dst *string, dstFrom *string, src, srcFrom string) {
		if src == "" || fieldRank[srcFrom] <= fieldRank[*dstFrom] {
			return
		}
		*dst, *dstFrom = src, srcFrom
		took = true
	}
	take(&c.Name, &c.NameFrom, anc.Name, anc.NameFrom)
	if anc.Description != "" && fieldRank[anc.DescriptionFrom] > fieldRank[c.DescriptionFrom] {
		c.Description, c.DescriptionFrom, c.Checked = anc.Description, anc.DescriptionFrom, anc.Checked
		took = true
	}
	take(&c.Website, &c.WebsiteFrom, anc.Website, anc.WebsiteFrom)
	take(&c.Category, &c.CategoryFrom, anc.Category, anc.CategoryFrom)
	if c.CommunityURL == "" && anc.CommunityURL != "" {
		c.CommunityURL, took = anc.CommunityURL, true
	}
	if took && c.InheritedFrom == "" {
		c.InheritedFrom = anc.Path
	}
}
