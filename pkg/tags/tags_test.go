package tags

import (
	"strings"
	"testing"
)

func src(body string) []File { return []File{{Name: "a.gno", Body: "package a\n\n" + body}} }

func has(ts []Tag, tag string) (Tag, bool) {
	for _, t := range ts {
		if t.Tag == tag {
			return t, true
		}
	}
	return Tag{}, false
}

// Every rule, one case that earns the tag and one that comes close and must
// not. The near misses are the calibration's false positives, kept here so
// they stay fixed.
func TestRules(t *testing.T) {
	const realm = "gno.land/r/x/app"
	const pure = "gno.land/p/x/lib"
	cases := []struct {
		name  string
		path  string
		files []File
		tag   string
		want  bool
		why   string // a substring of the evidence, when want
	}{
		{"token: grc20", realm, src(`import "gno.land/p/nt/grc20/v0"`), Token, true, "imports gno.land/p/nt/grc20/v0 (a.gno:3)"},
		{"token: the registry", realm, src(`import "gno.land/r/nt/grc20reg/v0"`), Token, true, "grc20reg"},
		{"token: a word in a string is not an import", realm, src(`var s = "gno.land/p/nt/grc20/v0"`), Token, false, ""},
		{"token: grc20votes is another package", realm, src(`import "gno.land/p/x/grc20votes/v0"`), Token, false, ""},
		{"nft: grc721", realm, src(`import "gno.land/p/nt/grc721/v0"`), NFT, true, "grc721"},
		{"nft: a fork's versioned name", realm, src(`import grc721 "gno.land/p/g1abc/grc721v2"`), NFT, true, "grc721v2"},
		{"nft: grc1155", realm, src(`import "gno.land/p/demo/grc1155"`), NFT, true, "grc1155"},
		{"nft: a mention in a comment", realm, src("// mints a grc721\nfunc F() {}"), NFT, false, ""},
		{"payments: a sending banker", realm, src("import \"chain/banker\"\nfunc F(cur realm) { _ = banker.NewBanker(banker.BankerTypeRealmSend, cur) }"),
			Payments, true, "calls banker.NewBanker (a.gno:4)"},
		{"payments: OriginSend under an alias", realm, src("import ur \"chain/runtime/unsafe\"\nfunc F() { _ = ur.OriginSend() }"),
			Payments, true, "calls ur.OriginSend"},
		{"payments: a read-only banker sends nothing", realm, src("import \"chain/banker\"\nfunc F(cur realm) { _ = banker.NewBanker(banker.BankerTypeReadonly, cur) }"),
			Payments, false, ""},
		{"payments: OriginSend in a comment", realm, src("// a fee via unsafe.OriginSend() later\nfunc F() {}"), Payments, false, ""},
		{"payments: a test setting OriginSend", realm, []File{
			{Name: "a.gno", Body: "package a\nfunc F() {}"},
			{Name: "a_test.gno", Body: "package a\nimport \"testing\"\nfunc TestF(t *testing.T) { testing.SetOriginSend(nil) }"},
		}, Payments, false, ""},
		{"payments: another package's OriginSend", realm, src("import \"gno.land/p/x/unsafe\"\nfunc F() { _ = unsafe.OriginSend() }"), Payments, false, ""},
		{"defi: a gnoswap realm", realm, src(`import "gno.land/r/gnoswap/pool"`), DeFi, true, "gno.land/r/gnoswap/pool"},
		{"defi: wugnot", realm, src(`import "gno.land/r/gnoland/wugnot"`), DeFi, true, "wugnot"},
		{"defi: gnoswap's math library alone", realm, src(`import "gno.land/p/gnoswap/uint256/v1"`), DeFi, false, ""},
		{"governance: a DAO library", realm, src(`import "gno.land/p/moul/udao/v0"`), Governance, true, "udao"},
		{"governance: implements the GovDAO", realm, src("type impl struct{}\nfunc (i *impl) PreExecuteProposal() {}"), Governance, true, "defines the method PreExecuteProposal"},
		{"governance: installs an implementation", realm, src("import \"gno.land/r/gov/dao\"\nfunc init() { dao.UpdateImpl(0, nil) }"), Governance, true, "calls dao.UpdateImpl"},
		// The namereg lesson: governed is not governance.
		{"governance: governed by the GovDAO", realm, src("import \"gno.land/r/gov/dao\"\nfunc F() { dao.MustCreateProposal(0, nil) }"), Governance, false, ""},
		{"governance: reads the member store", realm, src(`import "gno.land/r/gov/dao/memberstore/v0"`), Governance, false, ""},
		{"governance: a realm named dao", realm, src(`import "gno.land/r/x/memba_dao"`), Governance, false, ""},
		{"social: boards", realm, src(`import "gno.land/p/gnoland/boards/v0"`), Social, true, "boards"},
		{"social: boards2", realm, src(`import "gno.land/r/gnoland/boards2/v0"`), Social, true, "boards2"},
		{"social: resolving a @name", realm, src(`import "gno.land/r/sys/users"`), Social, false, ""},
		{"social: leaderboard is not a board", realm, src(`import "gno.land/r/x/leaderboard"`), Social, false, ""},
		{"access-control: ownable", realm, src(`import "gno.land/p/nt/ownable/v0"`), AccessControl, true, "ownable"},
		{"access-control: authorizable", realm, src(`import "gno.land/p/nt/ownable/exts/authorizable/v0"`), AccessControl, true, "authorizable"},
		{"access-control: rbac", realm, src(`import "gno.land/p/gnoswap/rbac/v1"`), AccessControl, true, "rbac"},
		{"access-control: an Owner variable", realm, src("var Owner = address(\"g1\")"), AccessControl, false, ""},
		{"events: chain.Emit", realm, src("import \"chain\"\nfunc F() { chain.Emit(\"X\") }"), Events, true, "calls chain.Emit (a.gno:4)"},
		{"events: a method called Emit", realm, src("type vm struct{}\nfunc (v vm) Emit() {}\nfunc F(v vm) { v.Emit() }"), Events, false, ""},
		{"render: a realm's Render", realm, src("func Render(path string) string { return \"\" }"), Render, true, "defines Render (a.gno:3)"},
		{"render: a method Render is not the page", realm, src("type t struct{}\nfunc (t) Render(path string) string { return \"\" }"), Render, false, ""},
		{"render: a pure package has no page", pure, src("func Render(path string) string { return \"\" }"), Render, false, ""},
		{"render: in a test file only", realm, []File{
			{Name: "a.gno", Body: "package a"},
			{Name: "a_test.gno", Body: "package a\nfunc Render(path string) string { return \"\" }"},
		}, Render, false, ""},
		// Rule set 2.
		{"governed: builds a GovDAO proposal", realm, src("import \"gno.land/r/gov/dao\"\nfunc F() { _ = dao.NewProposalRequest(\"t\", \"d\", nil) }"),
			Governed, true, "calls dao.NewProposalRequest (a.gno:4)"},
		{"governed: a variant of the constructor", realm, src("import \"gno.land/r/gov/dao\"\nfunc F() { _ = dao.NewProposalRequestWithFilter(\"t\", \"d\", nil, nil) }"),
			Governed, true, "NewProposalRequestWithFilter"},
		{"governed: a sys/params request", realm, src("import sysparams \"gno.land/r/sys/params\"\nfunc F() { _ = sysparams.NewSysParamUint64PropRequest(\"a\", \"b\", \"c\", 1) }"),
			Governed, true, "calls sysparams.NewSysParamUint64PropRequest"},
		{"governed: reading a proposal id is not building one", realm, src("import \"gno.land/r/gov/dao\"\nfunc F(i int64) { _ = dao.ProposalID(i) }"), Governed, false, ""},
		{"governed: its own NewProposalRequest", realm, src("func NewProposalRequest() {}\nfunc F() { NewProposalRequest() }"), Governed, false, ""},
		{"voting: a realm's Vote", realm, src("func Vote(cur realm, id int, yes bool) {}"), Voting, true, "defines Vote (a.gno:3)"},
		{"voting: a method Vote on a library type", realm, src("type b struct{}\nfunc (b) Vote() {}"), Voting, false, ""},
		{"voting: a pure package's Vote", pure, src("func Vote() {}"), Voting, false, ""},
		{"claims: a realm's Claim", realm, src("func Claim(cur realm) {}"), Claims, true, "defines Claim (a.gno:3)"},
		{"claims: ClaimRefund", realm, src("func ClaimRefund(cur realm, id int) {}"), Claims, true, "defines ClaimRefund"},
		{"claims: a fact-staking claim's title reads, it pays nothing", realm, src("func ClaimTitle(id int) string { return \"\" }"), Claims, false, ""},
		{"identity: r/sys/users", realm, src(`import "gno.land/r/sys/users"`), Identity, true, "imports gno.land/r/sys/users (a.gno:3)"},
		{"identity: r/sys/users/init is a different realm", realm, src(`import "gno.land/r/sys/users/init"`), Identity, false, ""},
		{"identity: a @ in a string", realm, src(`var s = "@moul"`), Identity, false, ""},
		{"access-control: a hand-rolled admin transfer", realm, src("func TransferAdmin(cur realm, to address) {}"), AccessControl, true, "defines TransferAdmin (a.gno:3)"},
		{"access-control: an owner check", realm, src("func assertOwner() {}"), AccessControl, true, "defines assertOwner"},
		{"access-control: an ownership library's method", pure, src("type O struct{}\nfunc (o *O) TransferOwnership(to address) {}"), AccessControl, true, "defines the method TransferOwnership"},
		{"access-control: a token's Transfer", realm, src("func Transfer(cur realm, to address, n int64) {}"), AccessControl, false, ""},
		{"access-control: an Admin getter", realm, src("func Admin() address { return \"\" }"), AccessControl, false, ""},
		{"pausable: defines Pause", realm, src("func Pause(cur realm) {}"), Pausable, true, "defines Pause (a.gno:3)"},
		{"pausable: a not-paused guard", realm, src("func assertNotPaused() {}"), Pausable, true, "defines assertNotPaused"},
		{"pausable: gnoswap's halt", realm, src(`import "gno.land/r/gnoswap/halt/v1"`), Pausable, true, "halt"},
		{"pausable: a paused flag nobody can flip", realm, src("var paused bool\nfunc F() bool { return paused }"), Pausable, false, ""},
		{"pausable: a game's Pause method", realm, src("type g struct{}\nfunc (g) Pause() {}"), Pausable, false, ""},
		{"upgradable: a facade swaps its implementation", realm, []File{
			{Name: "a.gno", Body: "package a\ntype Impl interface{ Greet() string }\nvar live Impl"},
			{Name: "b.gno", Body: "package a\nfunc Register(cur realm, i Impl) {\n\tlive = i\n}"},
		}, Upgradable, true, "Register replaces the implementation live (b.gno:3)"},
		{"upgradable: an upgrade library", realm, src(`import "gno.land/p/gnoswap/version_manager/v1"`), Upgradable, true, "version_manager"},
		{"upgradable: an interface value set once in init", realm, src("type Impl interface{ G() }\nvar live Impl\nfunc init() { live = nil }"), Upgradable, false, ""},
		{"upgradable: an unexported setter", realm, src("type Impl interface{ G() }\nvar live Impl\nfunc set(i Impl) { live = i }"), Upgradable, false, ""},
		{"upgradable: a struct value swapped", realm, src("type cfg struct{}\nvar c cfg\nfunc Set(x cfg) { c = x }"), Upgradable, false, ""},
		{"upgradable: a local shadow is not the package value", realm, src("type Impl interface{ G() }\nvar live Impl\nfunc Set(i Impl) { var x Impl; x = i; _ = x }"), Upgradable, false, ""},
		{"time-based: a deadline on the height", realm, src("import \"chain/runtime\"\nvar end int64\nfunc F() { if runtime.ChainHeight() >= end { panic(\"closed\") } }"),
			TimeBased, true, "compares runtime.ChainHeight (a.gno:5)"},
		{"time-based: through a variable", realm, src("import \"chain/runtime\"\nvar end int64\nfunc F() {\n\tnow := runtime.ChainHeight()\n\tif now > end {\n\t}\n}"),
			TimeBased, true, "compares runtime.ChainHeight (a.gno:7)"},
		{"time-based: the clock, Before", realm, src("import \"time\"\nvar end time.Time\nfunc F() bool { return time.Now().Before(end) }"), TimeBased, true, "compares time.Now"},
		{"time-based: an offset under an alias", realm, src("import rt \"chain/runtime\"\nvar last int64\nfunc F() bool { return int64(rt.ChainHeight())-last < 10 }"), TimeBased, true, "compares rt.ChainHeight"},
		{"time-based: stamping a record is not acting on time", realm, src("import \"chain/runtime\"\ntype rec struct{ h int64 }\nvar rs []rec\nfunc F() {\n\th := runtime.ChainHeight()\n\trs = append(rs, rec{h: h})\n\tif len(rs) > 10 {\n\t}\n}"),
			TimeBased, false, ""},
		{"time-based: a dice roll seeded by the height", realm, src("import \"chain/runtime\"\nvar best int64\nfunc F() {\n\troll := runtime.ChainHeight() % 6\n\tif roll > best {\n\t}\n}"),
			TimeBased, false, ""},
		{"time-based: an overflow guard", pure, src("import (\n\"chain/runtime\"\n\"math\"\n)\nfunc F() bool { return runtime.ChainHeight() >= math.MaxUint32 }"), TimeBased, false, ""},
		{"time-based: another package's ChainHeight", realm, src("import \"gno.land/p/x/runtime\"\nfunc F() bool { return runtime.ChainHeight() > 3 }"), TimeBased, false, ""},
		{"crypto: sha256", realm, src(`import "crypto/sha256"`), Crypto, true, "imports crypto/sha256 (a.gno:3)"},
		{"crypto: a merkle package", realm, src(`import "gno.land/p/moul/x/merkle/v0"`), Crypto, true, "merkle"},
		{"crypto: bech32 is an address encoding", realm, src(`import "crypto/bech32"`), Crypto, false, ""},
		{"crypto: a function named Hash", realm, src("func Hash(s string) int { return len(s) }"), Crypto, false, ""},
		{"math: uint256", realm, src(`import "gno.land/p/gnoswap/uint256/v1"`), Math, true, "uint256"},
		{"math: math/overflow", pure, src(`import "math/overflow"`), Math, true, "imports math/overflow"},
		{"math: plain math", pure, src(`import "math"`), Math, false, ""},
		{"cross-realm: imports a realm", realm, src(`import "gno.land/r/gnoland/wugnot"`), CrossRealm, true, "imports gno.land/r/gnoland/wugnot (a.gno:3)"},
		{"cross-realm: a pure package is not a realm", realm, src(`import "gno.land/p/nt/avl/v0"`), CrossRealm, false, ""},
		{"cross-realm: only in a test", realm, []File{
			{Name: "a.gno", Body: "package a"},
			{Name: "a_test.gno", Body: "package a\nimport \"gno.land/r/x/other\""},
		}, CrossRealm, false, ""},
		{"ui: a markdown package", realm, src(`import "gno.land/p/moul/md/v0"`), UI, true, "imports gno.land/p/moul/md/v0 (a.gno:3)"},
		{"ui: a UI kit", realm, src(`import "gno.land/p/moul/kit/ui/v0"`), UI, true, "kit/ui"},
		{"ui: a pager", realm, src(`import "gno.land/p/nt/avl/pager/v0"`), UI, true, "pager"},
		{"ui: mdx is not md", realm, src(`import "gno.land/p/x/mdx"`), UI, false, ""},
		{"ui: building markdown by hand", realm, src("func Render(string) string { return \"# hi\" }"), UI, false, ""},
		{"interactive: a $help link", realm, src("func Render(string) string { return \"[go](/r/x/app$help&func=Go)\" }"),
			Interactive, true, "draws a $help&func= link (a.gno:3)"},
		{"interactive: txlink", realm, src(`import "gno.land/p/moul/txlink/v0"`), Interactive, true, "txlink"},
		{"interactive: a $help link in a comment", realm, src("// see $help&func=Go\nfunc F() {}"), Interactive, false, ""},
		{"interactive: a library building links for others", pure, src(`import "gno.land/p/moul/txlink/v0"`), Interactive, false, ""},
		{"routing: mux", realm, src(`import "gno.land/p/nt/mux/v0"`), Routing, true, "mux"},
		{"routing: realmpath", realm, src(`import "gno.land/p/moul/realmpath/v0"`), Routing, true, "realmpath"},
		{"routing: splitting the path by hand", realm, src("import \"strings\"\nfunc Render(path string) string { return strings.Split(path, \"/\")[0] }"), Routing, false, ""},
		{"self-contained: the standard library only", pure, src("import \"strings\"\nvar _ = strings.ToUpper"), SelfContained, true, "imports only the standard library (1 source file)"},
		{"self-contained: no import at all", pure, src("func F() {}"), SelfContained, true, "imports nothing (1 source file)"},
		{"self-contained: one gno.land import", pure, src(`import "gno.land/p/nt/avl/v0"`), SelfContained, false, ""},
		{"self-contained: a test may import anything", pure, []File{
			{Name: "a.gno", Body: "package a"},
			{Name: "a_test.gno", Body: "package a\nimport \"gno.land/p/nt/uassert/v0\""},
		}, SelfContained, true, "imports nothing"},
		{"self-contained: only tests", pure, []File{{Name: "a_test.gno", Body: "package a"}}, SelfContained, false, ""},
		{"test-only: every source file is a test", pure, []File{
			{Name: "a_test.gno", Body: "package a"}, {Name: "b_filetest.gno", Body: "package main"}, {Name: "gnomod.toml", Body: ""},
		}, TestOnly, true, "all 2 source files are tests"},
		{"test-only: one source file", pure, []File{
			{Name: "a_test.gno", Body: "package a"}, {Name: "a.gno", Body: "package a"},
		}, TestOnly, false, ""},
		{"test-only: no source at all", pure, []File{{Name: "README.md", Body: ""}}, TestOnly, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tg, ok := has(Derive(c.path, c.files), c.tag)
			if ok != c.want {
				t.Fatalf("%s: got %v (%q), want %v", c.tag, ok, tg.Why, c.want)
			}
			if c.want && !strings.Contains(tg.Why, c.why) {
				t.Errorf("evidence %q, want it to contain %q", tg.Why, c.why)
			}
		})
	}
}

func TestEvidenceCountsTheRest(t *testing.T) {
	ts := Derive("gno.land/r/x/app", []File{
		{Name: "b.gno", Body: "package a\nimport \"chain\"\nfunc G() { chain.Emit(\"Y\") }"},
		{Name: "a.gno", Body: "package a\nimport \"chain\"\nfunc F() { chain.Emit(\"X\"); chain.Emit(\"Z\") }"},
	})
	tg, ok := has(ts, Events)
	if !ok || tg.Why != "calls chain.Emit (a.gno:3) and 2 more" {
		t.Errorf("events: %+v", tg)
	}
}

func TestLibrary(t *testing.T) {
	if _, ok := LibraryTag("gno.land/p/x/lib", nil); ok {
		t.Error("a library nobody imports")
	}
	if _, ok := LibraryTag("gno.land/r/x/app", []string{"gno.land/r/y/z"}); ok {
		t.Error("a realm is not a library")
	}
	tg, ok := LibraryTag("gno.land/p/x/lib", []string{"gno.land/r/a", "gno.land/r/b", "gno.land/r/c"})
	if !ok || tg.Why != "imported by gno.land/r/a and 2 other packages" {
		t.Errorf("library: %+v", tg)
	}
}

// A file Go's parser rejects still has its imports read; its symbols are not
// guessed at.
func TestUnparseableFileKeepsItsImports(t *testing.T) {
	ts := Derive("gno.land/r/x/app", src("import \"gno.land/p/nt/grc20/v0\"\nfunc F( {"))
	if _, ok := has(ts, Token); !ok {
		t.Errorf("token lost on a broken file: %+v", ts)
	}
}

func TestOrderAndKnown(t *testing.T) {
	ts := Derive("gno.land/r/x/app", src("import (\n\"chain\"\n\"gno.land/p/nt/grc20/v0\"\n)\nfunc Render(string) string { chain.Emit(\"x\"); return \"\" }"))
	var got []string
	for _, x := range ts {
		got = append(got, x.Tag)
	}
	if strings.Join(got, ",") != "token,events,render" {
		t.Errorf("order: %v", got)
	}
	for _, r := range Rules {
		if !Known(r.Tag) || Means(r.Tag) == "" {
			t.Errorf("%s: unknown or unexplained", r.Tag)
		}
	}
	if Known("defi ") || Known("") {
		t.Error("Known accepts junk")
	}
}
