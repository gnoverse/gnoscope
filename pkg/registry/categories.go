package registry

// The app taxonomy.
//
// A category is what somebody who does not read gno is browsing by: "show me
// the games". It used to be a free string in apps.json, read back by /apps as a
// chip strip, which meant the set of categories was whatever had been typed so
// far and nothing outside that page could name one.
//
// It is navigation now (one rail entry per category), so the list has to be
// declared somewhere both apps.json and the rail can be checked against. This
// is that place. Adding one is a three-line change here plus the rail entry,
// and TestNavAppsMatchesCategories fails until both are done.
const (
	CategoryGames          = "games"
	CategorySocial         = "social"
	CategoryDefi           = "defi"
	CategoryContent        = "content"
	CategoryGovernance     = "governance"
	CategoryInfrastructure = "infrastructure"
)

// Categories is the vocabulary, in reader order: the order the rail and the
// page's strip print them in, most-browsed first. It is deliberately not the
// order a heuristic breaks a tie in — that is categoryPriority in
// pkg/httpapi/apps_category.go, which is a different question with a different
// answer, and asserted there to be a permutation of this.
var Categories = []string{
	CategoryGames,
	CategorySocial,
	CategoryDefi,
	CategoryContent,
	CategoryGovernance,
	CategoryInfrastructure,
}

// ValidCategory reports whether s is one of them.
func ValidCategory(s string) bool {
	for _, c := range Categories {
		if c == s {
			return true
		}
	}
	return false
}

// CategoryRank orders a category by Categories, for sorting a set of them back
// into reader order. An unknown one sorts last rather than erroring: this is
// used on data that has already been through validateApps, and on inferred
// categories that never reach apps.json at all.
func CategoryRank(s string) int {
	for i, c := range Categories {
		if c == s {
			return i
		}
	}
	return len(Categories)
}
