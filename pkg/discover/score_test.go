package discover

import (
	"math"
	"testing"
)

func about(t *testing.T, got, want, tol float64, what string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f", what, got, want)
	}
}

// The numbers the design argues from. If these drift, the arguments in §6 stop
// describing the code.
func TestTheWorkedNumbers(t *testing.T) {
	// A bot making thousands of calls from one address against twenty humans
	// calling once each. The humans must win, and the bot must not be able to
	// improve its position by calling more.
	bot := Reach(1)
	humans := Reach(20)
	about(t, bot, 1.30, 0.01, "reach(1 actor)")
	about(t, humans, 2.32, 0.01, "reach(20 actors)")
	if humans/bot < 1.7 {
		t.Errorf("twenty humans beat one bot by only %.2fx", humans/bot)
	}

	// Size matters, and not proportionally. Against a 100,000 GNOT reference
	// the 681,635 GNOT record is 6.8x larger and 1.45x more interesting.
	atFloor := Magnitude("transfer.large", TransferLargeThresholdGNOT)
	record := Magnitude("transfer.large", 681_635)
	about(t, atFloor, 1.30, 0.01, "magnitude at the floor")
	about(t, record, 1.89, 0.01, "magnitude of the record transfer")
	about(t, record/atFloor, 1.45, 0.02, "the record over the floor")

	// 72-hour half-life. The values are the formula's, computed here rather
	// than copied from the design's prose: that text says 0.19 at a week and
	// 0.037 at two, and 0.5^(336/72) is 0.0394. The formula is the contract and
	// the rounded figures beside it are illustration, so the test asserts the
	// formula. An assertion that matched the prose would be asserting a typo.
	about(t, Recency(0), 1.0, 0.0001, "recency now")
	about(t, Recency(72), 0.5, 0.0001, "recency at 3 days")
	about(t, Recency(24*7), 0.1984, 0.0001, "recency at a week")
	about(t, Recency(24*14), 0.0394, 0.0001, "recency at two weeks")
	// And the defining property, which no rounding can blur.
	about(t, Recency(2*RecencyHalfLifeHours), 0.25, 0.0001, "two half-lives")

	// The damping table.
	for rank, want := range map[int]float64{1: 1.000, 2: 0.667, 3: 0.500, 10: 0.182, 40: 0.049} {
		about(t, Damp(rank), want, 0.001, "damp at rank")
	}
}

// Novelty is what separates one account's 179th package from somebody else's
// first, which is the single comparison this term exists for.

func TestANewcomersFirstBeatsAVeteransNth(t *testing.T) {
	newcomer := ScoreBase("package.deployed", true, 0, 1)
	veteran := ScoreBase("package.deployed", false, 0, 1)
	if newcomer <= veteran {
		t.Errorf("a first deploy scores %.2f against a repeat's %.2f", newcomer, veteran)
	}
	if newcomer/veteran != NoveltyFirst {
		t.Errorf("novelty multiplier is %.2f, want %.1f", newcomer/veteran, NoveltyFirst)
	}
}

// An unlisted kind should appear somewhere while somebody decides what it is
// worth, rather than scoring zero and vanishing.
func TestAnUnknownKindIsLowButNotInvisible(t *testing.T) {
	s := ScoreBase("something.new", false, 0, 0)
	if s <= 0 {
		t.Fatalf("an unknown kind scores %v", s)
	}
	if s >= ScoreBase("package.deployed", false, 0, 0) {
		t.Errorf("an unknown kind (%.1f) outranks the bread and butter", s)
	}
}

// The editorial table is the one place a human opinion lives, so its order is
// the claim. A deployer's debut must outrank an ordinary deploy by a lot.
func TestTheEditorialPriorOrdersTheKinds(t *testing.T) {
	order := []string{
		"deployer.first", "chain.spike", "package.first_call", "package.enabled",
		"package.deployed", "transfer.large",
	}
	for i := 1; i < len(order); i++ {
		if ScoreBases[order[i-1]] <= ScoreBases[order[i]] {
			t.Errorf("%s (%v) does not outrank %s (%v)",
				order[i-1], ScoreBases[order[i-1]], order[i], ScoreBases[order[i]])
		}
	}
}

// Neutral rather than zero everywhere a quantity is missing: a multiplicative
// score with a zero term is zero, and one absent field would silently bury an
// event rather than leaving it mid-table.
func TestMissingTermsAreNeutralNotZero(t *testing.T) {
	if got := Reach(0); got != 1.0 {
		t.Errorf("reach with no population = %v, want 1", got)
	}
	if got := Magnitude("package.deployed", 0); got != 1.0 {
		t.Errorf("magnitude with no quantity = %v, want 1", got)
	}
	if got := Magnitude("no.such.kind", 500); got != 1.0 {
		t.Errorf("magnitude with no reference = %v, want 1", got)
	}
	if got := ScoreBase("package.deployed", false, 0, 0); got != ScoreBases["package.deployed"] {
		t.Errorf("a deploy with nothing else known = %v, want its base %v",
			got, ScoreBases["package.deployed"])
	}
}

// The magnitude references are the spike floors, not a second set of numbers
// that can drift out of step with them.
func TestMagnitudeReferencesReuseTheFloors(t *testing.T) {
	if magnitudeRefs["chain.spike"] != SpikeFloors["chain.spike"] {
		t.Error("chain.spike's magnitude reference is not its detector floor")
	}
	if magnitudeRefs["package.spike"] != SpikeFloors["package.spike"] {
		t.Error("package.spike's magnitude reference is not its detector floor")
	}
}
