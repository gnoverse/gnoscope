package discover

import "math"

// The ranking score.
//
// Internal, always. It orders the page and it feeds the interest axis of the
// verdict, and it is never shown to a reader, because "0.82" answers no
// question anybody has.
//
//	score = base(kind) * novelty * reach * magnitude * recency * damp
//
// Multiplicative rather than additive, because an additive score lets one
// enormous term swamp every other consideration: a 681,635 GNOT transfer would
// outrank the first time a human ever published on the chain. Every term here
// is naturally a multiplier of an editorial prior.
//
// The first four are pure functions of the chain and are stored as ScoreBase.
// The last two are computed at read time: recency changes every second, and
// damp depends on the filtered result set. That split is what lets a feed
// ignore scoring entirely and order by time, and what keeps two readers thirty
// seconds apart seeing the same ranking.

// ScoreBases is the editorial prior per kind.
//
// The only place a human opinion lives, deliberately in one table a
// non-programmer can read and argue with rather than spread through the code.
// A kind absent here scores 10, low but not zero: a new kind should appear
// somewhere while somebody decides what it is worth, not vanish.
var ScoreBases = map[string]float64{
	"deployer.first":       60, // the rarest thing on chain, and the best row there is
	"chain.spike":          50, // the thing this page exists for
	"package.first_call":   45, // "somebody actually used it" is the story gno most needs told
	"package.enabled":      35, // a real state change with a wait a reader can picture
	"proposal.closed":      35, // a decision
	"proposal.opened":      30,
	"namespace.registered": 30, // a human name appearing is a person arriving
	"package.deployed":     25, // the bread and butter
	"package.spike":        25,
	"validator.registered": 20,
	"transfer.large":       15, // true, rarely a story
	"package.rejected":     15,
}

// ScoreBaseDefault is what an unlisted kind gets.
const ScoreBaseDefault = 10

// NoveltyFirst is the multiplier for the first of its kind for a subject or an
// actor, ever, on this network.
//
// Defends against a namespace's tenth deploy outranking a newcomer's first. On
// mainnet this is what separates one account's 179th package from somebody
// else's 1st.
const NoveltyFirst = 2.0

// ScoreBase is the stored half: everything that never changes once written.
//
// reach is unique actors and never the call count. A bot making 5,000 calls
// from one address has one unique actor and scores 1.30; twenty humans calling
// once each score 2.32. The humans beat the bot by 1.8x, and the bot cannot
// improve its position by calling more, which is the whole point.
//
// magnitude is the kind's natural quantity over that kind's floor, so it is
// 1.30 at exactly the floor and grows logarithmically from there. Size matters
// and it does not matter proportionally: against a 100,000 GNOT reference, the
// 681,635 GNOT record scores 1.89, so a 6.8x larger transfer is 1.45x more
// interesting rather than 6.8x.
func ScoreBase(kind string, firstEver bool, uniqueActors int64, magnitudeX float64) float64 {
	base, ok := ScoreBases[kind]
	if !ok {
		base = ScoreBaseDefault
	}
	novelty := 1.0
	if firstEver {
		novelty = NoveltyFirst
	}
	return base * novelty * Reach(uniqueActors) * Magnitude(kind, magnitudeX)
}

// Reach is 1 + log10(1 + unique actors). A kind with no natural population
// scores 1.0, which is the same as one actor and is the correct neutral.
func Reach(uniqueActors int64) float64 {
	if uniqueActors <= 0 {
		return 1.0
	}
	return 1 + math.Log10(1+float64(uniqueActors))
}

// Magnitude is 1 + log10(1 + x / x_ref(kind)).
//
// x_ref is the kind's own floor, so the number means "how far past the bar this
// one is" rather than an absolute that would have to be retuned per kind. A
// kind with no floor, or a non-positive quantity, scores a neutral 1.0.
func Magnitude(kind string, x float64) float64 {
	ref, ok := magnitudeRefs[kind]
	if !ok || ref <= 0 || x <= 0 {
		return 1.0
	}
	return 1 + math.Log10(1+x/ref)
}

// magnitudeRefs is x_ref per kind, each one a floor that already exists
// elsewhere in this package rather than a second set of numbers to keep in
// step.
var magnitudeRefs = map[string]float64{
	"chain.spike":   SpikeFloors["chain.spike"],
	"package.spike": SpikeFloors["package.spike"],
	// In GNOT, matching the threshold the kind is defined by.
	"transfer.large": TransferLargeThresholdGNOT,
	// A deploy's natural quantity is its file count, and a package is
	// interesting at about ten files.
	"package.deployed": 10,
}

// RecencyHalfLifeHours is 72, not 24.
//
// mainnet produces 0 to 17 deploys on a normal day, so a 24-hour half-life
// empties the page on a quiet Tuesday and the reader concludes the site is
// broken. Dated claim, 2026-09-21: re-tune toward 24 when the feed sustains
// more than 100 scored events a day.
const RecencyHalfLifeHours = 72.0

// Recency is 0.5 ^ (age_hours / half-life). Half at three days, 0.19 at a week,
// 0.037 at two.
func Recency(ageHours float64) float64 {
	if ageHours <= 0 {
		return 1.0
	}
	return math.Pow(0.5, ageHours/RecencyHalfLifeHours)
}

// Damp is 1 / (1 + 0.5 * (rank - 1)), where rank is this event's position among
// the same actor's events in the same result set.
//
// Defends against one deployer spamming the page, which is not hypothetical: on
// 2026-09-13 a single actor published 179 packages in one day, and undamped
// that day's page is 179 rows about one person. The top slot keeps that actor's
// best row and their fifth sinks below a newcomer's single deploy.
func Damp(rankWithinActor int) float64 {
	if rankWithinActor < 1 {
		rankWithinActor = 1
	}
	return 1 / (1 + 0.5*float64(rankWithinActor-1))
}
