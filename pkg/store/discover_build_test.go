package store

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/moul/mygnoscan/pkg/glossary"
)

// The server's root package loads the glossary at init; a test binary for this
// package does not link that, so the shipped file is loaded from disk. Same
// bytes either way.
func withGlossary(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile("../../docs/glossary.md")
	if err != nil {
		t.Fatalf("read docs/glossary.md: %v", err)
	}
	prev := glossary.Default
	glossary.MustLoad(raw)
	t.Cleanup(func() { glossary.Default = prev })
}

func recent(daysAgo int) time.Time {
	return time.Now().UTC().AddDate(0, 0, -daysAgo).Truncate(time.Hour)
}

func factsOf(t *testing.T, e DiscoverEvent) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(e.Facts, &out); err != nil {
		t.Fatalf("facts: %v", err)
	}
	return out
}

func kindsOf(events []DiscoverEvent) map[string]int {
	out := map[string]int{}
	for _, e := range events {
		out[e.Kind]++
	}
	return out
}

func buildAndRead(t *testing.T, db *DB, network string) []DiscoverEvent {
	t.Helper()
	res, err := db.RefreshDiscoverEvents([]string{network})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %d, want 1", len(res))
	}
	if res[0].Rejected > 0 {
		t.Fatalf("the gate rejected %d generated events, which is an emitter bug: %v",
			res[0].Rejected, res[0].Violations)
	}
	events, _, err := db.DiscoverEvents(DiscoverQuery{Network: network, Limit: DiscoverLimitMax})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// Every generated sentence in the feed passes the gate. This is the assertion
// the whole pipeline exists to make true, and it runs over real source rows
// rather than fixtures, so an emitter fed a field the SQL actually produces is
// what is being checked.
func TestEverythingBuiltPassesTheGate(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/moul/hello", "g1moul", 100, recent(3), true, 3)
	MustPackageSubmission(t, db, "alpha", "TX2", "gno.land/r/other/world", "g1other", 110, recent(2), true, 1)
	MustCall(t, db, "alpha", "TXC1", 120, recent(2), "g1caller", "gno.land/r/moul/hello", "Fn")
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}

	events := buildAndRead(t, db, "alpha")
	if len(events) == 0 {
		t.Fatal("no events built from two deploys and a call")
	}
	for _, e := range events {
		if len(e.Layers) < 3 || len(e.Facts) < 3 {
			t.Errorf("%s: facts=%s layers=%s", e.ID, e.Facts, e.Layers)
		}
	}
}

// A package resubmitted is still one deploy event. 77 of mainnet's paths have
// been resubmitted, and a reader should be told once that a package was
// published, not once per attempt.
func TestAResubmittedPackageIsOneEvent(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/moul/hello", "g1moul", 100, recent(5), true, 1)
	MustPackageSubmission(t, db, "alpha", "TX2", "gno.land/r/moul/hello", "g1moul", 200, recent(2), true, 4)
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}

	events := buildAndRead(t, db, "alpha")
	if n := kindsOf(events)["package.deployed"]; n != 1 {
		t.Fatalf("%d deploy events for one resubmitted path, want 1", n)
	}
	// And it is the *first* submission that is reported, not the latest.
	for _, e := range events {
		if e.Kind == "package.deployed" && e.Height != 100 {
			t.Errorf("reported height %d, want the earliest submission at 100", e.Height)
		}
	}
}

// block_height > 0 is the load-bearing filter, not a tidiness one. Without it
// day one of the feed is 89 genesis packages all stamped with the same
// timestamp: rows that say nothing happened.
func TestGenesisPackagesAreNotEvents(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TXGEN", "gno.land/r/gno/genesis", "g1gen", 0, recent(4), true, 1)
	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/moul/hello", "g1moul", 100, recent(3), true, 1)
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}

	for _, e := range buildAndRead(t, db, "alpha") {
		if e.Target == "gno.land/r/gno/genesis" {
			t.Errorf("a genesis package became an event: %s", e.ID)
		}
	}
}

// Layer 3 may say how many people have ever published only because the count is
// in the facts. If the source stopped supplying it the gate would reject the
// event, so this asserts the fact is there and is the real count.
func TestDeployerFirstCarriesTheAllTimeCount(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/a/one", "g1a", 100, recent(4), true, 1)
	MustPackageSubmission(t, db, "alpha", "TX2", "gno.land/r/b/two", "g1b", 110, recent(3), true, 1)
	MustPackageSubmission(t, db, "alpha", "TX3", "gno.land/r/c/three", "g1c", 120, recent(2), true, 1)
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}

	var seen int
	for _, e := range buildAndRead(t, db, "alpha") {
		if e.Kind != "deployer.first" {
			continue
		}
		seen++
		f := factsOf(t, e)
		if f["distinct_deployers"] != float64(3) {
			t.Errorf("%s: distinct_deployers = %v, want 3", e.ID, f["distinct_deployers"])
		}
	}
	if seen != 3 {
		t.Errorf("%d deployer.first events, want one per new deployer", seen)
	}
}

// Rebuilding produces the same ids, so a tick that runs every five minutes
// inserts nothing after the first. Without it every subscriber re-notifies on
// every tick.
func TestATickRebuildInsertsNothingTheSecondTime(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/moul/hello", "g1moul", 100, recent(3), true, 2)
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}

	first, err := db.RefreshDiscoverEvents([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Inserted == 0 {
		t.Fatal("first build inserted nothing")
	}
	second, err := db.RefreshDiscoverEvents([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Inserted != 0 {
		t.Errorf("second build inserted %d rows, want 0", second[0].Inserted)
	}
	if second[0].Built != first[0].Built {
		t.Errorf("built %d then %d: the same sources produced different events",
			first[0].Built, second[0].Built)
	}
}

// Two chains, one path. The feed is per chain and the sources must not blend.
func TestBuildIsScopedToOneChain(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/moul/hello", "g1moul", 100, recent(3), true, 1)
	MustPackageSubmission(t, db, "beta", "TX1", "gno.land/r/moul/hello", "g1moul", 100, recent(3), true, 1)
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}

	res, err := db.RefreshDiscoverEvents([]string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("results = %d, want one per network", len(res))
	}
	for _, r := range res {
		if r.Built == 0 {
			t.Errorf("%s built nothing", r.Network)
		}
	}
	for _, net := range []string{"alpha", "beta"} {
		events, _, err := db.DiscoverEvents(DiscoverQuery{Network: net})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Network != net {
				t.Errorf("%s feed carries an event from %s", net, e.Network)
			}
		}
	}
}

// The gate cannot run without the headword list, and a build that quietly
// skipped a check would produce exactly the output the checks exist to catch.
func TestABuildWithoutTheGlossaryRefuses(t *testing.T) {
	prev := glossary.Default
	glossary.Default = nil
	t.Cleanup(func() { glossary.Default = prev })

	db := NewTestDB(t)
	if _, err := db.RefreshDiscoverEvents([]string{"alpha"}); err == nil {
		t.Fatal("built events with no glossary loaded")
	}
}

// Nothing outside the window, so a chain older than thirty days does not
// republish its whole history on the tick that crosses the boundary.
func TestEventsOlderThanTheWindowAreNotBuilt(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	old := time.Now().UTC().AddDate(0, 0, -60)
	MustPackageSubmission(t, db, "alpha", "TXOLD", "gno.land/r/old/thing", "g1old", 50, old, true, 1)
	MustPackageSubmission(t, db, "alpha", "TXNEW", "gno.land/r/new/thing", "g1new", 100, recent(2), true, 1)
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}

	for _, e := range buildAndRead(t, db, "alpha") {
		if e.Target == "gno.land/r/old/thing" {
			t.Errorf("an event from %s is outside the %v window: %s", old.Format(time.RFC3339), DiscoverWindow, e.ID)
		}
	}
}

func TestSplitGnoPath(t *testing.T) {
	for _, tt := range []struct{ path, ns, name string }{
		{"gno.land/r/moul/hello", "moul", "hello"},
		{"gno.land/p/demo/avl", "demo", "avl"},
		{"gno.land/r/moul/home/sub", "moul", "sub"},
		{"", "", ""},
		{"nonsense", "", "nonsense"},
		// Pins the delegation to discover.SplitPath rather than the rule
		// itself, which is tested there. This package held a second copy that
		// said "v0" while the emitter's said "riscv", so the fact stored on the
		// event and the sentence built from it named different things. If
		// somebody re-inlines a copy here, this is what catches it.
		{"gno.land/p/moul/x/vm/riscv/v0", "moul", "riscv"},
	} {
		ns, name := splitGnoPath(tt.path)
		if ns != tt.ns || name != tt.name {
			t.Errorf("%q -> (%q, %q), want (%q, %q)", tt.path, ns, name, tt.ns, tt.name)
		}
	}
}

func TestAtLabel(t *testing.T) {
	if got := atLabel(""); got != "" {
		t.Errorf("an unregistered address rendered as %q, want empty so the template says somebody", got)
	}
	if got := atLabel("moul"); got != "@moul" {
		t.Errorf("atLabel(moul) = %q", got)
	}
}

// Every stored event carries a score, because the endpoint ranks on it and a
// zero there sorts the event off the bottom of the page rather than failing
// visibly. The first build of this table shipped without it and every row
// scored 0; nothing said so, because nothing read the column yet.
func TestEveryBuiltEventCarriesAScore(t *testing.T) {
	withGlossary(t)
	db := NewTestDB(t)

	MustPackageSubmission(t, db, "alpha", "TX1", "gno.land/r/moul/hello", "g1moul", 100, recent(3), true, 3)
	MustPackageSubmission(t, db, "alpha", "TX2", "gno.land/r/moul/second", "g1moul", 110, recent(2), true, 1)
	if err := db.RefreshFirstSeen(); err != nil {
		t.Fatal(err)
	}

	events := buildAndRead(t, db, "alpha")
	if len(events) == 0 {
		t.Fatal("nothing built")
	}
	for _, e := range events {
		if e.ScoreBase <= 0 {
			t.Errorf("%s scored %v", e.ID, e.ScoreBase)
		}
	}

	// And the editorial prior is actually applied: a debut outranks an ordinary
	// deploy, which is the comparison the whole table of bases exists for.
	var debut, deploy float64
	for _, e := range events {
		switch e.Kind {
		case "deployer.first":
			debut = e.ScoreBase
		case "package.deployed":
			if e.ScoreBase > deploy {
				deploy = e.ScoreBase
			}
		}
	}
	if debut <= deploy {
		t.Errorf("a first-ever deployer scores %.1f against a deploy's %.1f", debut, deploy)
	}
}
