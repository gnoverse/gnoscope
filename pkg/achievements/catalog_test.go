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
