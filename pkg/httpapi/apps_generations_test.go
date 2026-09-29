package httpapi

import (
	"fmt"
	"testing"
	"time"
)

const brNS = "gno.land/r/g1leu8d2vsplhehcfkjg50mwgdpxdkt8tztu95wr/"

func TestDeriveGenerationsChainsBubbleRumble(t *testing.T) {
	// The live mainnet shape on 2026-09-29: five generations, one of them
	// carrying the curated blurb, and three unrelated realms by the same
	// deployer that must not be folded into them.
	cards := []*AppCard{
		{Path: brNS + "bubblerumble"},
		{Path: brNS + "bubblerumble2"},
		{Path: brNS + "bubblerumble3"},
		{Path: brNS + "bubblerumble4"},
		{Path: brNS + "bubblerumble5"},
		{Path: brNS + "bubble"},
		{Path: brNS + "wbubble"},
		{Path: brNS + "kourtv3"},
	}
	deriveGenerations(cards)

	byPath := map[string]*AppCard{}
	for _, c := range cards {
		byPath[c.Path] = c
	}
	for _, tt := range []struct{ card, supersedes string }{
		{brNS + "bubblerumble5", brNS + "bubblerumble4"},
		{brNS + "bubblerumble4", brNS + "bubblerumble3"},
		{brNS + "bubblerumble3", brNS + "bubblerumble2"},
		{brNS + "bubblerumble2", brNS + "bubblerumble"},
	} {
		got := byPath[tt.card].Supersedes
		if len(got) != 1 || got[0] != tt.supersedes {
			t.Errorf("%s supersedes %v, want [%s]", tt.card, got, tt.supersedes)
		}
	}
	for _, p := range []string{brNS + "bubble", brNS + "wbubble", brNS + "kourtv3"} {
		if got := byPath[p].Supersedes; len(got) != 0 {
			t.Errorf("%s should stand alone, got supersedes %v", p, got)
		}
	}
	// The whole chain collapses to one card.
	kept := collapseSuperseded(cards)
	if len(kept) != 4 {
		t.Fatalf("kept %d cards, want 4 (rumble5, bubble, wbubble, kourtv3)", len(kept))
	}
	if kept[0].Path != brNS+"bubblerumble5" {
		t.Errorf("survivor is %s, want bubblerumble5", kept[0].Path)
	}
	if n := len(kept[0].Previous); n != 4 {
		t.Errorf("bubblerumble5 carries %d previous generations, want 4", n)
	}
	if kept[0].SupersedesFrom != fromDerived {
		t.Errorf("SupersedesFrom = %q, want %q", kept[0].SupersedesFrom, fromDerived)
	}
}

func TestDeriveGenerationsLeavesCuratedEdgesAlone(t *testing.T) {
	// Curation already says 4 replaces 3 and 2. Derivation must not add a
	// second answer to "what replaced bubblerumble3", and must still reach 5.
	cards := []*AppCard{
		{Path: brNS + "bubblerumble2"},
		{Path: brNS + "bubblerumble3"},
		{Path: brNS + "bubblerumble4", Supersedes: []string{brNS + "bubblerumble3", brNS + "bubblerumble2"}},
		{Path: brNS + "bubblerumble5"},
	}
	deriveGenerations(cards)
	if got := cards[2].Supersedes; len(got) != 2 {
		t.Errorf("curated edges changed: %v", got)
	}
	if got := cards[3].Supersedes; len(got) != 1 || got[0] != brNS+"bubblerumble4" {
		t.Errorf("bubblerumble5 supersedes %v, want [bubblerumble4]", got)
	}
	if cards[2].SupersedesFrom != fromCurated {
		t.Errorf("curated card SupersedesFrom = %q", cards[2].SupersedesFrom)
	}
}

func TestDeriveGenerationsRefusesABackwardsDeploy(t *testing.T) {
	// A higher number deployed earlier is a numbering scheme this rule does
	// not understand, and guessing there reorders somebody else's realms.
	cards := []*AppCard{
		{Path: "gno.land/r/ns/thing", DeployedAt: "2026-09-20T00:00:00Z"},
		{Path: "gno.land/r/ns/thing2", DeployedAt: "2026-01-01T00:00:00Z"},
	}
	deriveGenerations(cards)
	if got := cards[1].Supersedes; len(got) != 0 {
		t.Errorf("derived %v across a backwards deploy", got)
	}
}

func TestInheritCarriesTheCuratedSentenceForward(t *testing.T) {
	// This is the half that makes deriving the edge an improvement rather than
	// a regression: without it, bubblerumble5 ranks correctly and then says
	// "bubblerumble5" with no description.
	old := &AppCard{
		Path: brNS + "bubblerumble4",
		Name: "Bubble Rumble", NameFrom: fromCurated,
		Description: "Last bidder wins.", DescriptionFrom: fromCurated, Checked: "2026-09-24",
		Website: "https://bubblerumble.net", WebsiteFrom: fromCurated,
		Category: catGames, CategoryFrom: fromCurated,
	}
	newer := &AppCard{Path: brNS + "bubblerumble5", Name: "bubblerumble5", NameFrom: fromPath}
	cards := []*AppCard{old, newer}
	deriveGenerations(cards)
	inheritAcrossGenerations(cards)

	if newer.Name != "Bubble Rumble" || newer.NameFrom != fromCurated {
		t.Errorf("name = %q (%s), want the curated one", newer.Name, newer.NameFrom)
	}
	if newer.Description != "Last bidder wins." || newer.Checked != "2026-09-24" {
		t.Errorf("description = %q checked %q", newer.Description, newer.Checked)
	}
	if newer.Website != "https://bubblerumble.net" || newer.Category != catGames {
		t.Errorf("website = %q category = %q", newer.Website, newer.Category)
	}
	if newer.InheritedFrom != old.Path {
		t.Errorf("InheritedFrom = %q, want %q: a reader has to be able to see that nobody wrote this sentence about this realm", newer.InheritedFrom, old.Path)
	}
}

func TestInheritNeverDowngrades(t *testing.T) {
	// A card with its own curated sentence keeps it, and an ancestor's
	// path-derived name never overwrites anything.
	old := &AppCard{
		Path: brNS + "bubblerumble4",
		Name: "bubblerumble4", NameFrom: fromPath,
		Description: "The realm's own doc comment.", DescriptionFrom: fromChain,
	}
	newer := &AppCard{
		Path: brNS + "bubblerumble5",
		Name: "Bubble Rumble", NameFrom: fromCurated,
		Description: "Last bidder wins.", DescriptionFrom: fromCurated,
	}
	cards := []*AppCard{old, newer}
	deriveGenerations(cards)
	inheritAcrossGenerations(cards)
	if newer.Name != "Bubble Rumble" || newer.Description != "Last bidder wins." {
		t.Errorf("curated fields were overwritten: %q / %q", newer.Name, newer.Description)
	}
	if newer.InheritedFrom != "" {
		t.Errorf("InheritedFrom = %q, want empty: nothing was inherited", newer.InheritedFrom)
	}
}

func TestInheritReachesPastAGenerationWithNothingToGive(t *testing.T) {
	// The curated blurb is on generation 3, generation 4 was never described,
	// and 5 is the survivor. Walking one hop would lose it.
	cards := []*AppCard{
		{Path: brNS + "bubblerumble3", Name: "Bubble Rumble", NameFrom: fromCurated},
		{Path: brNS + "bubblerumble4", Name: "bubblerumble4", NameFrom: fromPath},
		{Path: brNS + "bubblerumble5", Name: "bubblerumble5", NameFrom: fromPath},
	}
	deriveGenerations(cards)
	inheritAcrossGenerations(cards)
	if cards[2].Name != "Bubble Rumble" {
		t.Errorf("name = %q, want it carried two generations", cards[2].Name)
	}
}

// The whole pipeline, over a real database, on the shape that motivated it:
// bubblerumble5 shipped, nobody edited apps.json, and the page has to show one
// game with the sentence a human already wrote.
func TestAppsHubFoldsANewGenerationNobodyCurated(t *testing.T) {
	api, db := newTestAPI(t)
	when := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	seedRealm := func(path string, calls int, deps []string) {
		t.Helper()
		if err := db.UpsertPackage("alpha", path, "pkg", "g1leu8", "tx-"+path, 100, when, true, 2); err != nil {
			t.Fatalf("UpsertPackage %s: %v", path, err)
		}
		if len(deps) > 0 {
			if err := db.SetDependencies("alpha", path, deps); err != nil {
				t.Fatalf("SetDependencies %s: %v", path, err)
			}
		}
		for i := 0; i < calls; i++ {
			if err := db.InsertCall("alpha", fmt.Sprintf("tx-%s-%d", path, i), 100+i, 0, when,
				fmt.Sprintf("g1caller%d", i), path, "Bid", true); err != nil {
				t.Fatalf("InsertCall %s: %v", path, err)
			}
		}
	}
	seedRealm(brNS+"bubblerumble4", 6, nil)
	seedRealm(brNS+"bubblerumble5", 3, nil)
	seedRealm(brNS+"wbubble", 2, []string{"gno.land/p/nt/grc20/v0", "gno.land/r/nt/grc20reg/v0"})

	var resp appsHubResponse
	getJSON(t, api.HandleAppsHub, "/api/apps?network=alpha", &resp)

	by := map[string]AppCard{}
	for _, a := range resp.Apps {
		by[a.Path] = a
	}
	if _, still := by[brNS+"bubblerumble4"]; still {
		t.Error("bubblerumble4 is still its own card, so the generation was not folded")
	}
	game, ok := by[brNS+"bubblerumble5"]
	if !ok {
		t.Fatal("bubblerumble5 is not on the page")
	}
	if game.Name != "Bubble Rumble" || game.NameFrom != fromCurated {
		t.Errorf("name = %q from %q, want the curated one carried forward", game.Name, game.NameFrom)
	}
	if game.Description == "" || game.Category != catGames {
		t.Errorf("description = %q category = %q", game.Description, game.Category)
	}
	if game.InheritedFrom != brNS+"bubblerumble4" {
		t.Errorf("inherited_from = %q, want bubblerumble4", game.InheritedFrom)
	}
	if len(game.Previous) != 1 || game.Previous[0].Path != brNS+"bubblerumble4" {
		t.Errorf("previous = %v, want bubblerumble4", game.Previous)
	}

	w, ok := by[brNS+"wbubble"]
	if !ok {
		t.Fatal("wbubble is not on the page")
	}
	if w.Category != catDefi || w.CategoryFrom != fromInferred {
		t.Errorf("wbubble category = %q from %q, want defi inferred from its imports", w.Category, w.CategoryFrom)
	}
	if w.CategoryWhy == "" {
		t.Error("an inferred category with no evidence is a claim the page cannot support")
	}
	if len(w.Previous) != 0 {
		t.Errorf("wbubble folded %v: it is not a generation of bubblerumble", w.Previous)
	}
}
