package achievements

import (
	"fmt"
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

			// Every badge has to be decidable by something. Live is a
			// supplement rather than an alternative: it ORs a chain read into
			// an answer the index already has, so a badge carrying both is
			// fine and a badge carrying neither can never be awarded at all.
			if d.SQL == "" {
				t.Error("no SQL: nothing can ever award this")
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

// Nothing in the catalog is unearnable.
//
// session-key used to be: it marked a delegated signing address, which is
// something an account *is* rather than something it did, and no master account
// could ever become one. It still sat in the denominator, so moul's page read
// "21 of 26" against a real ceiling of 25 with nothing saying which badge was
// the impossible one. It was deleted rather than excluded, because nobody
// browses a session address: a session is a signing key, and the page anyone
// opens is the master it was granted by.
func TestNothingInTheCatalogIsUnearnable(t *testing.T) {
	if Lookup("session-key") != nil {
		t.Error("session-key is back. It awards a badge to a delegated address nobody looks up, and it cannot be earned by the account that granted it")
	}
	// The three session badges all describe what the MASTER did, which is the
	// thing a reader can go and do something about.
	for _, slug := range []string{"session-created", "session-used", "session-revoked"} {
		d := Lookup(slug)
		if d == nil {
			t.Fatalf("%s is missing; creating, using and revoking a session are the three deeds worth a badge", slug)
		}
		if strings.Contains(strings.ToLower(d.How), "not earned") {
			t.Errorf("%s reads as a marker rather than an instruction: %q", slug, d.How)
		}
	}
	// session-used is awarded to the master, not to the key. The query is the
	// only place that can get this wrong, and getting it wrong means awarding
	// a badge to an address with no page and no owner reading it.
	if d := Lookup("session-used"); d != nil && !strings.Contains(d.SQL, "g.master AS address") {
		t.Errorf("session-used does not award to the master: %s", d.SQL)
	}
}

// A tier names the rung below it and raises the bar.
//
// Of is what lets a page draw "first transaction -> ten -> a hundred -> a
// thousand" as one ladder instead of four unrelated badges, so a dangling Of is
// a rung that renders detached, and a threshold that does not increase is a
// ladder that reads as going backwards.
func TestTiersFormALadder(t *testing.T) {
	tiers := 0
	for _, d := range Catalog {
		if d.Of == "" && d.Threshold == 0 {
			continue
		}
		tiers++
		if d.Of == "" || d.Threshold == 0 {
			t.Errorf("%s sets one of Of/Threshold and not the other: %q / %d", d.Slug, d.Of, d.Threshold)
			continue
		}
		parent := Lookup(d.Of)
		if parent == nil {
			t.Errorf("%s is a tier of %q, which is not in the catalog", d.Slug, d.Of)
			continue
		}
		if parent.Threshold >= d.Threshold {
			t.Errorf("%s needs %d but its parent %s needs %d; a rung has to be higher than the one below it",
				d.Slug, d.Threshold, parent.Slug, parent.Threshold)
		}
		// The rung's query has to actually count to its own threshold. A
		// copy-pasted tier that still says 100 in its SQL is awarded to
		// everybody the rung below already covers, and nothing else would
		// notice.
		if !strings.Contains(d.SQL, fmt.Sprintf("rn = %d", d.Threshold)) {
			t.Errorf("%s claims a threshold of %d but its SQL does not select the %dth event: %s",
				d.Slug, d.Threshold, d.Threshold, d.SQL)
		}
	}
	if tiers == 0 {
		t.Error("no tiers in the catalog, so this test proves nothing")
	}
}

// A tool badge reads a memo, and a memo is free text the signer chose.
//
// The What line is the only place that honesty is recorded, so it has to quote
// the string being matched: "signed a transaction stamped X" is a claim about
// the memo, and "used X" would be a claim about the client, which the chain
// does not record and nobody can check.
func TestToolBadgesReadTheMemoAndSaySo(t *testing.T) {
	tools := 0
	for _, d := range Catalog {
		if d.Group != GroupTools {
			continue
		}
		tools++
		if !strings.Contains(d.SQL, "tx_memos") {
			t.Errorf("%s is a tool badge that does not read tx_memos: %s", d.Slug, d.SQL)
		}
		if !strings.Contains(d.What, "memo") && !strings.Contains(d.What, "stamped") {
			t.Errorf("%s: What claims the tool was used rather than that the memo says so: %q", d.Slug, d.What)
		}
	}
	if tools == 0 {
		t.Error("no tool badges in the catalog, so this test proves nothing")
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
	if len(indexed) != len(Catalog) {
		t.Errorf("Indexed returned %d of %d definitions; every badge carries SQL now that session-used is indexed from session_txs",
			len(indexed), len(Catalog))
	}
	for _, d := range indexed {
		if d.SQL == "" {
			t.Errorf("%s has no SQL but Indexed returned it", d.Slug)
		}
	}
}
