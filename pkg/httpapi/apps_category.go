package httpapi

import (
	"sort"
	"strings"
)

// Guessing a category, and saying that it is a guess.
//
// Categories were curated only, so the five realms somebody had written a line
// about had one and the fifty-four the chain discovered had none. The facet
// chips on /apps therefore filtered a page down to the curated subset, which
// is the opposite of what discovery is for.
//
// Two signals, and the first one is the one worth having. A realm's *imports*
// are the chain's own record of what it handles: `gno.land/p/nt/grc20/v0` in
// the list means this realm moves a fungible token, and no naming convention
// has to be trusted for that. Only when there is no such signal does this fall
// back to words in the path, the name and the sentence, which is a guess about
// somebody else's code and is labelled as one.
//
// Every inferred category carries the evidence that produced it, the same rule
// registry.KindInferred states for curated data. A chip a reader cannot check
// is a claim this page has no business making.

// Categories, in the order a tie is broken. Deliberately the existing five:
// inventing a sixth to hold the awkward cases would move the judgement from
// "which of these is it" to "how many buckets are there", which is a product
// question and not one a heuristic gets to answer.
const (
	catDefi    = "defi"
	catGames   = "games"
	catGov     = "governance"
	catContent = "content"
	catInfra   = "infrastructure"
)

var categoryPriority = []string{catDefi, catGames, catGov, catContent, catInfra}

// categoryImports maps an import path fragment to what importing it proves.
//
// Only what a realm *handles*, never what governs or serves it. The first
// version of this table read `gno.land/r/gov/dao` as governance and filed
// `r/sys/namereg`, the realm anyone registers a username in, under it: namereg
// imports the DAO because its own admin calls are proposal-gated, which makes
// it governed and not governance. Measured live on mainnet 2026-09-29, minutes
// after the deploy. The same argument retires `r/sys/params` and `r/sys/users`
// from here: reading a chain parameter and resolving a @handle are things any
// realm does.
//
// What survives is the token standards and the named defi realms. Those are a
// claim about the thing itself, recorded by the chain rather than chosen by
// whoever named the package, which is the whole reason this half of the
// guesser is not a guess.
var categoryImports = map[string]string{
	"/grc20":            catDefi,
	"/grc20reg":         catDefi,
	"/r/gnoland/wugnot": catDefi,
	"/r/gnoswap/":       catDefi,
}

// categoryWords are the fallback, matched against tokens of the path, the name
// and the description.
//
// Short words are matched whole, because `bet` inside `alphabet` and `pad`
// inside `keypad` are how a substring list quietly mislabels a page. Four
// characters and up may also match inside a token, which is what catches
// `gnoswap`, `trialmint` and `memba_reviews`.
var categoryWords = map[string][]string{
	catDefi: {
		"defi", "swap", "amm", "dex", "liquidity", "pool", "token", "coin",
		"grc20", "wugnot", "wrap", "stake", "staking", "vault", "lend",
		"borrow", "treasury", "otc", "mint", "airdrop", "auction", "escrow",
		"bond", "yield", "collateral", "perp",
	},
	catGames: {
		"game", "rumble", "chess", "dice", "lottery", "raffle", "bet",
		"arena", "puzzle", "settlers", "quest", "battle", "tournament",
	},
	catGov: {
		"dao", "govern", "governance", "proposal", "vote", "voting", "ballot",
		"council", "election", "quorum",
	},
	catContent: {
		"blog", "board", "forum", "post", "feed", "comment", "review", "wiki",
		"note", "pad", "home", "profile", "page", "article", "memo", "journal",
		"social", "thread", "gallery",
	},
	catInfra: {
		"registry", "namereg", "valoper", "validator", "users", "username",
		"sys", "indexer", "monitor", "relay", "oracle", "faucet", "bridge",
		"wallet", "explorer", "status", "dashboard", "playground", "editor",
		"mcp", "node", "rpc", "deploy", "keeper",
	},
}

// inferCategories fills in the cards nobody has categorised.
func (a *API) inferCategories(cards []*AppCard, network string) {
	want := make([]string, 0, len(cards))
	for _, c := range cards {
		if c.Category == "" && c.Path != "" {
			want = append(want, c.Path)
		}
	}
	imports := map[string][]string{}
	if network != "" && len(want) > 0 {
		if got, err := a.db.PackageImports(network, want); err == nil {
			imports = got
		}
	}
	for _, c := range cards {
		if c.Category != "" {
			continue
		}
		if cat, why := categoryFromImports(imports[c.Path]); cat != "" {
			c.Category, c.CategoryFrom, c.CategoryWhy = cat, fromInferred, why
			continue
		}
		if cat, why := categoryFromWords(c.Path, c.Name, c.Description); cat != "" {
			c.Category, c.CategoryFrom, c.CategoryWhy = cat, fromInferred, why
		}
	}
}

// categoryFromImports reads the chain's own record of what a realm handles.
func categoryFromImports(imports []string) (string, string) {
	best, evidence := "", ""
	for _, imp := range imports {
		for frag, cat := range categoryImports {
			if !strings.Contains(imp, frag) {
				continue
			}
			// First match in priority order wins, so a realm that imports both
			// a token and the DAO lands somewhere stable rather than somewhere
			// map iteration decided.
			if best == "" || rankOf(cat) < rankOf(best) {
				best, evidence = cat, "imports "+imp
			}
		}
	}
	return best, evidence
}

func rankOf(cat string) int {
	for i, c := range categoryPriority {
		if c == cat {
			return i
		}
	}
	return len(categoryPriority)
}

// categoryFromWords reads the words a card already shows.
//
// The path and the name are consulted first and alone. A description only
// decides when they say nothing at all, rather than adding to them: Adena is a
// wallet whose blurb mentions tokens, staking and NFTs three times, so letting
// the sentence outvote the name filed it under defi. What a thing is called was
// chosen once and on purpose; what its blurb mentions is whatever it happens to
// touch.
func categoryFromWords(path, name, description string) (string, string) {
	if cat, why := scoreWords(tokenize(path + " " + name)); cat != "" {
		return cat, why
	}
	return scoreWords(tokenize(description))
}

// scoreWords picks the category with the most distinct matches, ties broken by
// categoryPriority so the answer never depends on map iteration order.
func scoreWords(tokens []string) (string, string) {
	if len(tokens) == 0 {
		return "", ""
	}
	hits := map[string][]string{}
	for cat, words := range categoryWords {
		for _, w := range words {
			if matchesToken(tokens, w) {
				hits[cat] = append(hits[cat], w)
			}
		}
	}
	best := ""
	for _, cat := range categoryPriority {
		if len(hits[cat]) > len(hits[best]) {
			best = cat
		}
	}
	if best == "" {
		return "", ""
	}
	sort.Strings(hits[best])
	return best, "names " + strings.Join(quoteAll(hits[best]), ", ")
}

func quoteAll(words []string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = `"` + w + `"`
	}
	return out
}

// matchesToken is exact for short words and a substring for long ones.
const categorySubstringMin = 4

// categoryExactOnly are the words that must match a whole token whatever their
// length, because a common longer word contains them.
//
// `board` inside `leaderboard` filed Gnolove, a contributions leaderboard,
// under content. Each entry here is one of those, and the list is short on
// purpose: the substring rule is what catches `gnoswap` and `trialmint`, and
// every exception to it is a word this guesser can no longer see inside a
// compound.
var categoryExactOnly = map[string]bool{
	"board": true, "post": true, "note": true,
	"name": true, "bond": true, "page": true, "home": true,
}

func matchesToken(tokens []string, word string) bool {
	for _, t := range tokens {
		if t == word {
			return true
		}
		if len(word) >= categorySubstringMin && !categoryExactOnly[word] && strings.Contains(t, word) {
			return true
		}
	}
	return false
}

// tokenize cuts a path or a sentence into comparable words.
//
// A versioned segment yields both spellings, so `padv3` reads as `pad` and a
// card does not lose its category the day it ships a new version, while
// `grc20` keeps the number that is part of its name. The `gno.land` and
// `r`/`p` prefixes go too: every path on the chain carries them and a token
// every row shares can only add noise.
func tokenize(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		t := strings.ToLower(b.String())
		b.Reset()
		switch t {
		case "gno", "land", "r", "p", "gnoland", "v":
			return
		}
		out = append(out, t)
		// The stripped form too, not instead: `padv3` has to read as `pad`,
		// and `grc20` has to stay `grc20`. Emitting only the stem loses every
		// standard whose number is part of its name, which is most of them.
		stem := strings.TrimRight(t, "0123456789")
		if len(stem) > 1 && strings.HasSuffix(stem, "v") {
			stem = stem[:len(stem)-1]
		}
		if stem != "" && stem != t {
			out = append(out, stem)
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}
