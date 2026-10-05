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
