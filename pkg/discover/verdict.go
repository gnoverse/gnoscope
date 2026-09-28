package discover

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The verdict: should this be shared, and if not, what would settle it.
//
// Two axes, deliberately, because they come apart constantly. Measured on
// mainnet 2026-09-21, six of the eight realms with the most unique callers
// belong to one other team and not one of the eight is ours. A single
// "interestingness" score marks those the eight most shareable things on the
// chain, and a reader who is asking the machine what to share precisely because
// they cannot judge it themselves will then share them. The feed has to be able
// to say "genuinely interesting, and not ours to announce", and one number
// cannot say that.
//
// So: interest, which is about the event, and clearance, which is about whose
// it is. The matrix combines them, and one rule outranks the matrix.

// Verdict is a closed enum. Exactly three values, ever.
//
// Closed because it is a security property rather than a style preference: the
// card builds a CSS class from this value, so an open string lets a crafted
// value pick an arbitrary class list and paint a "hold" as a "share" before any
// script runs. Marshalling rejects anything else, and unmarshalling fails
// closed.
type Verdict string

const (
	// VerdictShare means post it.
	VerdictShare Verdict = "share"
	// VerdictMaybe means something specific would settle it, named in
	// Judgement.Blocking.
	VerdictMaybe Verdict = "maybe"
	// VerdictHold means do not, and VerdictReason says what rules it out.
	// It is also the value anything unrecognised becomes.
	VerdictHold Verdict = "hold"
)

func (v Verdict) valid() bool {
	return v == VerdictShare || v == VerdictMaybe || v == VerdictHold
}

// MarshalJSON refuses to emit a verdict outside the three.
//
// An error rather than a fallback: a malformed verdict reaching this point is a
// bug in the emitter, and a 500 from the server is a bug report. Silently
// writing "hold" would hide it, and the next reader of the feed would simply
// see fewer recommendations than they should with nothing to explain why.
func (v Verdict) MarshalJSON() ([]byte, error) {
	if !v.valid() {
		return nil, fmt.Errorf("verdict %q is not one of share, maybe, hold", string(v))
	}
	return json.Marshal(string(v))
}

// UnmarshalJSON fails closed to hold.
//
// The asymmetry with MarshalJSON is deliberate and is the whole design. Writing
// is ours and a bad value is our bug, so it errors. Reading is somebody else's
// output, possibly an older version of this software or a hand-edited file, and
// there the only safe reading of a value we do not understand is the one that
// recommends nothing. An unknown verdict is never permission.
func (v *Verdict) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		*v = VerdictHold
		return nil
	}
	if parsed := Verdict(s); parsed.valid() {
		*v = parsed
		return nil
	}
	*v = VerdictHold
	return nil
}

// Interest is how much this event is worth somebody's attention.
type Interest string

const (
	InterestLow    Interest = "low"
	InterestMedium Interest = "medium"
	InterestHigh   Interest = "high"
)

// Clearance is whose event this is.
type Clearance string

const (
	// ClearanceOurs is the operator's own namespace, or a fact belonging to
	// nobody in particular.
	ClearanceOurs Clearance = "ours"
	// ClearanceUnclear is the default and the common case.
	ClearanceUnclear Clearance = "unclear"
	// ClearanceTheirs is a namespace on the configured other-teams list.
	ClearanceTheirs Clearance = "theirs"
	// ClearanceSensitive is the denylist, and it outranks everything.
	ClearanceSensitive Clearance = "sensitive"
)

// Axis is one judgement with the reason it was reached.
//
// Why is not decoration. It is what lets a reader disagree with the machine on
// the day it is wrong, and what teaches them the judgement over time.
type Axis struct {
	Level string `json:"level"`
	Why   string `json:"why"`
}

// Judgement is the audit trail behind the verdict.
type Judgement struct {
	Interest  Axis   `json:"interest"`
	Clearance Axis   `json:"clearance"`
	Blocking  string `json:"blocking,omitempty"`
}

// ClearanceConfig is the operator's opinion about who owns what.
//
// It ships empty, and that is load-bearing in two ways. A public explorer
// asserting "these namespaces are ours and those are theirs" is asserting an
// affiliation on somebody else's behalf, which is not its job. And with an
// empty config every attributable event is unclear, so the best verdict any of
// them can reach is maybe: a fresh deployment recommends nothing it has not
// been told it may recommend, and the verdict_reason explaining the gap is also
// how an operator discovers the file exists.
type ClearanceConfig struct {
	// Accounts are namespaces the operator speaks for.
	Accounts []string `json:"accounts"`
	// OtherTeams are namespaces belonging to somebody else, named so the feed
	// can say so rather than guess.
	OtherTeams []string `json:"other_teams"`
	// Treasury are addresses whose fund movements are not neutral to narrate.
	Treasury []string `json:"treasury"`
	// Deny needs no reason recorded, because the reason itself may be the thing
	// that cannot be said.
	Deny []string `json:"deny"`
}

// Configured reports whether the operator has supplied any opinion at all.
//
// Surfaced in the response so a reader can tell "nothing here is ours" from
// "nobody has said what is ours", which are very different reasons to see no
// recommendations.
func (cfg ClearanceConfig) Configured() bool {
	return len(cfg.Accounts) > 0 || len(cfg.OtherTeams) > 0 ||
		len(cfg.Treasury) > 0 || len(cfg.Deny) > 0
}

// chainLevelKinds belong to nobody in particular, so they are ours with no
// configuration at all. Nobody privately owns how many wallets joined this week,
// what the chain's own governance decided, or who validates it.
var chainLevelKinds = map[string]bool{
	"chain.spike":          true,
	"chain.milestone":      true,
	"proposal.opened":      true,
	"proposal.closed":      true,
	"validator.registered": true,
}

// Subject is what a clearance decision is made about.
type Subject struct {
	Kind      string
	Namespace string
	Actor     string
	// Parties are every address the event touches, for the treasury rule.
	Parties []string
}

// Clear decides whose event this is.
func (cfg ClearanceConfig) Clear(s Subject) Axis {
	// Sensitive first, because it outranks everything including ours: an event
	// we must not narrate is not made safe by belonging to us.
	//
	// package.rejected always, and this is not a judgement call the machine
	// gets to make. Naming a rejected submission is publicly criticising
	// somebody's code before anyone has spoken to them.
	if s.Kind == "package.rejected" {
		return Axis{string(ClearanceSensitive),
			"naming a rejected submission is criticising somebody's code in public before speaking to them"}
	}
	for _, addr := range s.Parties {
		if contains(cfg.Deny, addr) {
			return Axis{string(ClearanceSensitive), "an address here is on the operator's denylist"}
		}
		if s.Kind == "transfer.large" && contains(cfg.Treasury, addr) {
			return Axis{string(ClearanceSensitive),
				"fund movements are legible to anyone watching, and narrating them is not neutral"}
		}
	}
	if contains(cfg.Deny, s.Namespace) || contains(cfg.Deny, s.Actor) {
		return Axis{string(ClearanceSensitive), "this namespace is on the operator's denylist"}
	}

	if chainLevelKinds[s.Kind] {
		return Axis{string(ClearanceOurs), "a chain-wide fact belongs to nobody in particular"}
	}
	if s.Namespace != "" && contains(cfg.Accounts, s.Namespace) {
		return Axis{string(ClearanceOurs), "the namespace is on the operator's accounts list"}
	}
	if s.Namespace != "" && contains(cfg.OtherTeams, s.Namespace) {
		return Axis{string(ClearanceTheirs), "the namespace belongs to another team"}
	}
	if s.Namespace == "" || looksLikeBareAddress(s.Namespace) {
		return Axis{string(ClearanceUnclear), "the namespace is a bare address with no registered name"}
	}
	return Axis{string(ClearanceUnclear), "no clearance is configured for this namespace"}
}

// Bucket places a score on the interest axis.
//
// The boundaries are relative, never absolute, so they survive the chain
// getting busier: high is the top decile of the trailing window for that
// network, medium the next three, low the rest. An absolute threshold set today
// would quietly mark everything high the month the chain doubles.
//
// The thresholds are passed in rather than computed here because they are a
// property of the stored distribution, and this package does not read the
// database. A caller with no distribution yet passes zeroes, which makes
// everything high; that is why the caller is expected to have one.
func Bucket(score, p90, p60 float64) Interest {
	switch {
	case score >= p90:
		return InterestHigh
	case score >= p60:
		return InterestMedium
	default:
		return InterestLow
	}
}

// Decide combines the two axes.
//
// The matrix is the ordinary path. The rule above it is the point of the whole
// design: share requires clearance ours, and there is no other route to it at
// any interest level.
func Decide(interest Interest, clearance Axis, reasonInterest string) (Verdict, Judgement) {
	j := Judgement{
		Interest:  Axis{string(interest), reasonInterest},
		Clearance: clearance,
	}

	level := Clearance(clearance.Level)
	switch {
	case level == ClearanceSensitive || level == ClearanceTheirs:
		return VerdictHold, j
	case interest == InterestLow:
		return VerdictHold, j
	case interest == InterestHigh && level == ClearanceOurs:
		return VerdictShare, j
	}
	// Everything left is maybe, and a maybe owes the reader the one thing that
	// would settle it. A maybe with no blocking line is a shrug.
	j.Blocking = blockingFor(level)
	return VerdictMaybe, j
}

func blockingFor(level Clearance) string {
	if level == ClearanceOurs {
		return "interesting enough to mention, not big enough to lead with"
	}
	return "confirm with whoever owns this namespace before posting"
}

// MaxVerdictReason is the budget for the one line that may be all anybody reads.
const MaxVerdictReason = 160

// Reason writes verdict_reason: one sentence, always present, written for the
// case where it is the only thing the reader sees.
//
// Truncated rather than dropped, unlike every other budget in this package,
// and the asymmetry is deliberate: the other budgets protect values whose
// meaning changes when cut, a path or a package name. This is a sentence, and
// half a sentence that ends in an ellipsis is visibly half a sentence, whereas
// an absent reason reads as "no reason", which is a different and worse claim.
func Reason(v Verdict, j Judgement) string {
	var s string
	switch v {
	case VerdictShare:
		s = capitalise(j.Interest.Why) + ", and it is ours to talk about."
	case VerdictMaybe:
		s = capitalise(j.Interest.Why) + ", but " + j.Clearance.Why + "."
	default:
		if Clearance(j.Clearance.Level) == ClearanceSensitive || Clearance(j.Clearance.Level) == ClearanceTheirs {
			s = capitalise(j.Clearance.Why) + "."
		} else {
			s = capitalise(j.Interest.Why) + "."
		}
	}
	s = strings.ReplaceAll(s, "..", ".")
	if len([]rune(s)) > MaxVerdictReason {
		r := []rune(s)
		s = string(r[:MaxVerdictReason-1]) + "…"
	}
	return s
}

func capitalise(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - 'a' + 'A'
	}
	return string(r)
}

func contains(list []string, want string) bool {
	if want == "" {
		return false
	}
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

func looksLikeBareAddress(s string) bool {
	return strings.HasPrefix(s, "g1") && len(s) >= 38
}
