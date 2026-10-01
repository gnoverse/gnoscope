package ghlab

import "strings"

// Seed is one curated repository: something a human decided belongs in the
// picture of the gno ecosystem, whether or not any search would find it.
//
// Tracked seeds get the expensive treatment, a walk of their pull requests
// and contributors. Untracked ones are listed and their stars kept fresh, and
// that is all: a repository nobody has pushed to in two years costs one
// request a pass instead of six.
type Seed struct {
	FullName string
	Kind     string
	Tracked  bool
	Note     string
}

// Seeds is the curated list. Every entry was confirmed on 2026-09-29 both to
// resolve and to be public, and both halves of that matter.
//
// Names that look right and are not, all 404: gnolang/gnoweb (a directory
// inside the monorepo), gnolang/contribs (likewise), gnoverse/gnodev
// (likewise), gnolang/hall-of-fame. Two resolve only via a rename, which is
// why FetchRepo keys on the answer rather than the ask: gnolang/awesome-gno
// is gnoverse/awesome-gno, and gnolang/docs is gnolang/docs.gno.land.
//
// And two plausible entries were dropped because they are private, which a
// list written from memory cannot know and which no amount of curation makes
// safe: this list ships in a public binary and the page it feeds is public,
// so a private repository named here is a disclosure whether or not any
// instance's token can read it. Check visibility before adding a row.
// The syncer refuses one anyway (see Repo.Private) and says so in the log.
var Seeds = []Seed{
	{"gnolang/gno", "core", true, "the monorepo: GnoVM, tm2 and gno.land"},
	{"gnolang/tx-indexer", "core", true, "the indexer every explorer here reads"},
	{"gnolang/tm2-js-client", "js", true, "the Tendermint2 JS client"},
	{"gnolang/gno-js-client", "js", true, "the gno JS client, built on tm2-js-client"},
	{"gnolang/faucet", "core", true, "the faucet behind every testnet"},
	{"gnolang/blog", "docs", true, "the gno.land blog, published on chain"},
	{"gnolang/docs.gno.land", "docs", true, "the documentation site"},
	{"gnolang/hackerspace", "community", true, "where proposals and experiments are filed"},
	{"gnolang/workshops", "community", false, "workshop material, pushed in bursts"},
	{"gnolang/tx-exports", "ops", false, "every deployed package, decoded back to source"},
	{"gnoverse/awesome-gno", "community", true, "the curated list this explorer also parses"},
	{"gnoverse/gnoscope", "core", true, "this explorer"},
	{"gnoverse/gnoshot", "core", false, "the screenshot service behind /api/shot"},
	{"gnoverse/gnochess", "app", false, "chess as a realm"},
	{"gnoverse/memeland", "app", false, "the workshop dapp"},
	{"onbloc/adena-wallet", "app", true, "the wallet most gno.land users hold"},
	{"onbloc/gnoscan", "app", true, "the other explorer"},
	{"gnoswap-labs/gnoswap", "app", true, "the AMM that most of mainnet's gas goes to"},
	{"samouraiworld/gnopls", "tooling", false, "the language server"},
	{"samouraiworld/gnoland-packages", "app", false, "their realms"},
	{"gnoverse/gnockpit", "tooling", false, "the ops dashboard"},
	{"gnoverse/gno-mcp", "tooling", true, "gno through the model context protocol"},
	{"onbloc/adena-wallet-sdk", "js", false, "the SDK behind the wallet"},
	{"gnoswap-labs/gnoswap-interface", "app", false, "the AMM's front end"},
	{"moul/gno-contracts", "app", false, "moul's realms"},
	{"moul/gnopie", "tooling", false, "the read-only chain CLI"},
}

// DiscoveryQuery is one way of finding a repository nobody curated.
//
// Why is the sentence the page prints beside a result. A discovered row makes
// a claim ("this project depends on gno") and the claim has to carry its own
// evidence, the same rule pkg/discover works under: a generated explanation
// may not add a fact.
type DiscoveryQuery struct {
	Kind  string // go-import | js-import | realm | topic
	Code  bool   // code search, rather than repository search
	Query string
	Why   string
}

// DiscoveryQueries are the searches run each pass. Totals measured against
// the live API on 2026-09-29 and quoted here so a later reading that comes
// back wildly different is recognisable as a broken query rather than as
// news: go.mod 201, gno-js-client 47, tm2-js-client 37, gnomod.toml 1,504
// once gnolang's own is excluded, gno.mod 730, topic:gnolang 24,
// topic:gnovm 10, topic:gno 41.
//
// `-org:gnolang` is load-bearing on the two file-name queries: without it the
// monorepo's own examples/ tree is 4,500 of the 5,976 gnomod.toml files on
// GitHub and every page of results is the same repository.
// ⚠️ A search runs as the token, so results include private repositories the
// token's owner can read. Measured 2026-09-29, before this was handled: one
// query returned eight of them, two of them the operator's own, with the
// matching file paths ready to print in the evidence column of a public page.
//
// The two search APIs need different fixes and confusing them is silent.
// Repository search honours `is:public` and every repository query below
// carries it. **Code search does not**: it is not a supported qualifier
// there, and rather than erroring it matches nothing, so a code query with
// `is:public` appended returns `total_count: 0` and the section looks empty
// for a reason nobody can see (140 results without it, 0 with). Code hits are
// filtered on `repository.private` in SearchCode instead.
// TestDiscoveryQueryVisibility holds both halves; Repo.Private is the backstop.
var DiscoveryQueries = []DiscoveryQuery{
	{Kind: "go-import", Code: true,
		Query: `"github.com/gnolang/gno" filename:go.mod -org:gnolang`,
		Why:   "requires github.com/gnolang/gno in a go.mod"},
	{Kind: "js-import", Code: true,
		Query: `"@gnolang/gno-js-client" filename:package.json -org:gnolang`,
		Why:   "depends on @gnolang/gno-js-client"},
	{Kind: "js-import", Code: true,
		Query: `"@gnolang/tm2-js-client" filename:package.json -org:gnolang`,
		Why:   "depends on @gnolang/tm2-js-client"},
	{Kind: "realm", Code: true,
		Query: `filename:gnomod.toml -org:gnolang`,
		Why:   "holds a gnomod.toml, so it carries gno packages"},
	{Kind: "realm", Code: true,
		Query: `filename:gno.mod "gno.land/" -org:gnolang`,
		Why:   "holds a gno.mod naming a gno.land path"},
	{Kind: "topic", Code: false, Query: `topic:gnolang is:public`, Why: "tagged topic:gnolang"},
	{Kind: "topic", Code: false, Query: `topic:gnovm is:public`, Why: "tagged topic:gnovm"},
	{Kind: "topic", Code: false, Query: `topic:gno-land is:public`, Why: "tagged topic:gno-land"},
}

// botLogins are accounts whose commits are machine output.
//
// Kept as an explicit list plus a suffix rule rather than trusting GitHub's
// `type: Bot`, because the contributors endpoint reports several of these as
// Users: a bot that pushes through a personal access token is indistinguishable
// from a person in that field.
var botLogins = map[string]bool{
	"dependabot":        true,
	"dependabot[bot]":   true,
	"github-actions":    true,
	"renovate":          true,
	"renovate[bot]":     true,
	"codecov":           true,
	"coderabbitai":      true,
	"gnolang-bot":       true,
	"mergify":           true,
	"allcontributors":   true,
	"semantic-release":  true,
	"web-flow":          true,
	"copilot-swe-agent": true,
	// gnolang's PR bot, a regular User account rather than a GitHub App, so
	// neither the [bot] suffix nor GraphQL's Bot type catches it. Measured
	// 2026-10-01: 2,451 comments on gnolang/gno, sixth in the score.
	"gno2d2": true,
}

// IsBot reports whether a login is machine output rather than a contributor.
func IsBot(login string) bool {
	l := strings.ToLower(strings.TrimSpace(login))
	if l == "" {
		return true
	}
	if botLogins[l] {
		return true
	}
	return strings.HasSuffix(l, "[bot]") || strings.HasSuffix(l, "-bot")
}
