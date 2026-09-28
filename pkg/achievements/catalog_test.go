package achievements

import (
	"regexp"
	"strings"
	"testing"
)

// The catalog is data, and every one of these is a property that breaks
// something specific and quietly when it stops holding.
func TestCatalogInvariants(t *testing.T) {
	seen := map[string]bool{}
	groups := map[Group]bool{}
	for _, g := range GroupOrder {
		groups[g] = true
	}

	for _, d := range Catalog {
		t.Run(d.Slug, func(t *testing.T) {
			// A duplicate slug is a silently lost badge: the second definition
			// overwrites the first in the table's primary key and in bySlug.
			if seen[d.Slug] {
				t.Fatalf("duplicate slug %q", d.Slug)
			}
			seen[d.Slug] = true

			if d.Slug != strings.ToLower(d.Slug) || strings.ContainsAny(d.Slug, " _") {
				t.Errorf("slug %q is not kebab-case; it ends up in a URL", d.Slug)
			}
			for _, f := range []struct{ name, val string }{
				{"Name", d.Name}, {"Emoji", d.Emoji}, {"What", d.What}, {"How", d.How},
			} {
				if strings.TrimSpace(f.val) == "" {
					t.Errorf("%s is empty", f.name)
				}
			}
			if !groups[d.Group] {
				t.Errorf("group %q is not in GroupOrder, so this badge renders in no bucket", d.Group)
			}
			if GroupLabel[d.Group] == "" {
				t.Errorf("group %q has no label", d.Group)
			}

			// SQL and Live are the two ways a badge can be decided, and it has
			// to be exactly one. Neither means a badge nobody can ever earn;
			// both means the rollup and the live read would disagree about the
			// same slug, and the one that wrote last would win.
			switch {
			case d.SQL == "" && !d.Live:
				t.Error("no SQL and not marked Live: nothing can ever award this")
			case d.SQL != "" && d.Live:
				t.Error("both SQL and Live: two sources for one badge")
			}

			if d.SQL != "" {
				// Every table here is network-scoped (AGENTS.md's first
				// invariant). A definition that forgets the filter merges two
				// chains' histories, which for a badge means awarding it on
				// mainnet for something done on a testnet.
				if !strings.Contains(d.SQL, "@net") {
					t.Error("SQL does not bind @net, so it is not network-scoped")
				}
				// The four columns the rollup selects out of it, by name.
				for _, col := range []string{"address", "block_height", "block_time", "tx_hash"} {
					if !strings.Contains(d.SQL, col) {
						t.Errorf("SQL never mentions %q, which the rollup selects by name", col)
					}
				}
			}
		})
	}
}

// A `how` line must never carry a gas figure.
//
// Four of them shipped with one, each invented to look plausible and each wrong:
// `-gas-wanted 200000` on a send that costs 1,238,665 when it has to create the
// recipient's account, `2000000` on a call that measures 11,732,203. gno prices a
// transaction on the work its code does, so the number is not derivable from the
// shape of the command, and one written here is one a stranger pastes and watches
// bounce. `-simulate only` is how the chain answers it, and pointing at that is
// the instruction worth giving.
func TestNoHowLineQuotesAGasFigure(t *testing.T) {
	// The flag followed by digits. The bare flag name is allowed and used: three
	// entries tell a reader to go and find the right value for themselves.
	quoted := regexp.MustCompile(`-gas-wanted[= ]+[0-9]`)
	feeQuoted := regexp.MustCompile(`-gas-fee[= ]+[0-9]`)
	for _, d := range Catalog {
		if m := quoted.FindString(d.How); m != "" {
			t.Errorf("%s: how line quotes a gas ceiling (%q). Tell the reader to measure with -simulate only instead", d.Slug, m)
		}
		if m := feeQuoted.FindString(d.How); m != "" {
			t.Errorf("%s: how line quotes a gas fee (%q). The fee is a ratio against the ceiling, so a flat figure is wrong at any other ceiling", d.Slug, m)
		}
	}
}

// A marker is not earnable, and every consumer has to know which kind it is
// looking at. moul's page read "21 of 26" against a real ceiling of 25 because
// session-key sat in the denominator: a badge in a score that nobody reading
// their own page can ever move.
func TestMarkersAreNotEarnableAndSayNotToTry(t *testing.T) {
	markers := 0
	for _, d := range Catalog {
		if !d.Marker {
			continue
		}
		markers++
		// A marker still has to be decidable, or it would draw on nobody's page
		// including the addresses it describes.
		if d.SQL == "" {
			t.Errorf("%s is a marker with no SQL, so nothing can ever show it", d.Slug)
		}
		// And it has to say it is not a thing to go and do, because the grid
		// draws it next to badges that are.
		if !strings.Contains(strings.ToLower(d.How), "not earned") {
			t.Errorf("%s is a marker but its how line reads like an instruction: %q", d.Slug, d.How)
		}
	}
	if markers == 0 {
		t.Error("no markers in the catalog, so this test proves nothing")
	}
	if markers >= len(Catalog)/2 {
		t.Errorf("%d of %d entries are markers; the score would be mostly unearnable", markers, len(Catalog))
	}
}

// The two "somebody gave you something" badges have to mean somebody else.
// Their How lines promise it ("not something you do to yourself") and the
// queries did not enforce it, so a send to your own address awarded one.
func TestReceivedBadgesExcludeSelf(t *testing.T) {
	for _, slug := range []string{"first-gnot-received", "grc20-received"} {
		d := Lookup(slug)
		if d == nil {
			t.Fatalf("%s is not in the catalog", slug)
		}
		if !strings.Contains(d.SQL, "from_address <> to_address") &&
			!strings.Contains(d.SQL, "from_addr <> to_addr") {
			t.Errorf("%s does not exclude a self-send, so it can be awarded to yourself: %s", slug, d.SQL)
		}
	}
}

// A how line naming a realm has to name one that is there. `validator` pointed
// at r/gnoland/valopers, which is not deployed on mainnet; the registry is
// r/gnops/valopers.
func TestValidatorNamesTheRegistryThatExists(t *testing.T) {
	d := Lookup("validator")
	if d == nil {
		t.Fatal("validator is not in the catalog")
	}
	if strings.Contains(d.How, "r/gnoland/valopers") {
		t.Error("how line names r/gnoland/valopers, which is not deployed on mainnet")
	}
	if !strings.Contains(d.How, "r/gnops/valopers") {
		t.Error("how line does not name the registry that is actually there, r/gnops/valopers")
	}
	// The guard that rejects exactly the readers most likely to try.
	if !strings.Contains(d.How, "ErrFrontrunValidator") {
		t.Error("how line does not warn that Register rejects a key already in the valset")
	}
}

func TestLookupAndIndexed(t *testing.T) {
	if Lookup("first-realm") == nil {
		t.Error("Lookup missed a slug that is in the catalog")
	}
	if Lookup("no-such-badge") != nil {
		t.Error("Lookup invented a badge")
	}
	indexed := Indexed()
	if len(indexed) == 0 || len(indexed) >= len(Catalog) {
		t.Errorf("Indexed returned %d of %d definitions; it should be every one carrying SQL, and session-used carries none",
			len(indexed), len(Catalog))
	}
	for _, d := range indexed {
		if d.SQL == "" {
			t.Errorf("%s has no SQL but Indexed returned it", d.Slug)
		}
	}
}
