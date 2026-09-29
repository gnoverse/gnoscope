package httpapi

import "testing"

func TestCategoryFromImports(t *testing.T) {
	// wbubble's real import list, read from /api/realm on mainnet 2026-09-29.
	// It is the case that motivated this: nothing in the path says defi, and
	// the chain records that it wraps a token.
	cat, why := categoryFromImports([]string{
		"gno.land/p/nt/grc20/v0",
		"gno.land/r/g1leu8d2vsplhehcfkjg50mwgdpxdkt8tztu95wr/bubble",
		"gno.land/r/nt/grc20reg/v0",
	})
	if cat != catDefi {
		t.Errorf("wbubble: category = %q, want %q", cat, catDefi)
	}
	if why == "" {
		t.Error("an inferred category must carry its evidence")
	}
	if cat, _ := categoryFromImports([]string{"gno.land/p/demo/ufmt", "gno.land/p/demo/avl"}); cat != "" {
		t.Errorf("ufmt and avl prove nothing about a category, got %q", cat)
	}
	if cat, _ := categoryFromImports(nil); cat != "" {
		t.Errorf("no imports, no claim, got %q", cat)
	}
}

func TestCategoryFromWords(t *testing.T) {
	tests := []struct {
		name, path, cardName, desc string
		want                       string
	}{
		{"the defi folder says so", "gno.land/r/demo/defi/grc20factory", "grc20factory", "", catDefi},
		{"a version suffix does not hide the word", "gno.land/r/g1n4pl/gnomi/padv3", "gnomi/padv3", "", catContent},
		{"and neither does its unversioned twin", "gno.land/r/g1n4pl/gnomi/pad", "gnomi/pad", "", catContent},
		{"a game by its name", "gno.land/r/g1leu8/bubblerumble5", "bubblerumble5", "", catGames},
		{"a mint is defi", "gno.land/r/g1wx60/trialmint/stable", "trialmint/stable", "", catDefi},
		{"a feed is content", "gno.land/r/samcrew/memba_feed_v1", "memba_feed_v1", "", catContent},
		{"a faucet is plumbing", "gno.land/r/moul/faucet/v0", "faucet", "", catInfra},
		{"an off-chain wallet, named only", "", "Adena Wallet", "Friendly wallet that simplifies sending tokens.", catInfra},
		{"nothing to go on", "gno.land/r/g1n4pl/hearth/v1", "hearth", "", ""},
	}
	for _, tt := range tests {
		got, why := categoryFromWords(tt.path, tt.cardName, tt.desc)
		if got != tt.want {
			t.Errorf("%s: categoryFromWords(%q,%q,%q) = %q, want %q",
				tt.name, tt.path, tt.cardName, tt.desc, got, tt.want)
		}
		if got != "" && why == "" {
			t.Errorf("%s: inferred %q with no evidence", tt.name, got)
		}
	}
}

func TestCategoryWordsAreNotSubstringTraps(t *testing.T) {
	// The short words are the dangerous ones, and they are matched whole.
	// `bet` inside `alphabet` is how a keyword list quietly mislabels a page.
	for _, path := range []string{
		"gno.land/r/demo/alphabet",
		"gno.land/r/demo/keypad",
		"gno.land/r/demo/amman",
	} {
		if got, why := categoryFromWords(path, "", ""); got != "" {
			t.Errorf("%s: got %q (%s), want no category", path, got, why)
		}
	}
}

func TestTokenizeKeepsBothSpellings(t *testing.T) {
	got := tokenize("gno.land/r/ns/padv3")
	want := map[string]bool{"ns": true, "padv3": true, "pad": true}
	for _, tok := range got {
		delete(want, tok)
	}
	if len(want) > 0 {
		t.Errorf("tokenize dropped %v (got %v)", want, got)
	}
	// A standard's number is part of its name, not a generation.
	hasGRC20 := false
	for _, tok := range tokenize("gno.land/r/demo/defi/grc20factory") {
		if tok == "grc20factory" {
			hasGRC20 = true
		}
	}
	if !hasGRC20 {
		t.Error("grc20factory must survive tokenizing with its number")
	}
}

func TestCategoryNameBeatsDescription(t *testing.T) {
	// Both of these came off the live page on 2026-09-29 and both were filed
	// wrong before the rule they pin.
	tests := []struct {
		name, cardName, desc, want string
	}{
		{
			// A wallet's blurb mentions tokens, staking and NFTs; the thing is
			// still a wallet. The name decides and the sentence does not vote.
			name:     "a wallet is not defi because its blurb sells tokens",
			cardName: "Adena Wallet",
			desc:     "Friendly wallet that simplifies sending & receiving tokens, staking, NFT storage, and dapp connections.",
			want:     catInfra,
		},
		{
			// `board` inside `leaderboard` filed a contributions leaderboard
			// under content.
			name:     "leaderboard is not a board",
			cardName: "Gnolove",
			desc:     "Community leaderboard and contributions analytics for builders of the Gnoland ecosystem.",
			want:     "",
		},
		{
			// Nothing in the name, so the sentence gets to answer.
			name:     "the description decides when the name says nothing",
			cardName: "GnoScan",
			desc:     "A gno.land block explorer, making on-chain data legible and intuitive for everyone.",
			want:     catInfra,
		},
	}
	for _, tt := range tests {
		if got, why := categoryFromWords("", tt.cardName, tt.desc); got != tt.want {
			t.Errorf("%s: got %q (%s), want %q", tt.name, got, why, tt.want)
		}
	}
}
