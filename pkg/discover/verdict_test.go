package discover

import (
	"encoding/json"
	"strings"
	"testing"
)

// This is the one line of the design worth protecting with a test whose name
// says why it exists.
//
// Six of the eight realms with the most unique callers on mainnet belong to one
// other team, and not one of the eight is ours (measured 2026-09-21). A
// one-dimensional score marks those the eight most shareable things on the
// chain, and the person asking the machine what to share is asking precisely
// because they cannot judge it themselves. So: no clearance, no share, at any
// interest level, by any route.
func TestShareRequiresClearanceOursAndThereIsNoOtherPathToIt(t *testing.T) {
	for _, level := range []Clearance{ClearanceUnclear, ClearanceTheirs, ClearanceSensitive} {
		for _, interest := range []Interest{InterestLow, InterestMedium, InterestHigh} {
			v, _ := Decide(interest, Axis{string(level), "because"}, "very interesting")
			if v == VerdictShare {
				t.Errorf("interest=%s clearance=%s produced share", interest, level)
			}
		}
	}
	// And the one cell that does reach it, so the test cannot pass by making
	// share unreachable altogether.
	if v, _ := Decide(InterestHigh, Axis{string(ClearanceOurs), "ours"}, "why"); v != VerdictShare {
		t.Errorf("high interest on our own namespace = %q, want share", v)
	}
}

// The matrix, cell by cell.
func TestTheMatrix(t *testing.T) {
	want := map[Clearance]map[Interest]Verdict{
		ClearanceOurs:      {InterestHigh: VerdictShare, InterestMedium: VerdictMaybe, InterestLow: VerdictHold},
		ClearanceUnclear:   {InterestHigh: VerdictMaybe, InterestMedium: VerdictMaybe, InterestLow: VerdictHold},
		ClearanceTheirs:    {InterestHigh: VerdictHold, InterestMedium: VerdictHold, InterestLow: VerdictHold},
		ClearanceSensitive: {InterestHigh: VerdictHold, InterestMedium: VerdictHold, InterestLow: VerdictHold},
	}
	for level, row := range want {
		for interest, expect := range row {
			got, _ := Decide(interest, Axis{string(level), "why"}, "why")
			if got != expect {
				t.Errorf("%s / %s = %q, want %q", interest, level, got, expect)
			}
		}
	}
}

// A maybe owes the reader the one thing that would settle it. A maybe with no
// blocking line is a shrug.
func TestEveryMaybeSaysWhatWouldSettleIt(t *testing.T) {
	for _, level := range []Clearance{ClearanceOurs, ClearanceUnclear} {
		for _, interest := range []Interest{InterestHigh, InterestMedium} {
			v, j := Decide(interest, Axis{string(level), "no clearance is configured"}, "why")
			if v != VerdictMaybe {
				continue
			}
			if j.Blocking == "" {
				t.Errorf("%s / %s is a maybe with nothing blocking it", interest, level)
			}
		}
	}
	// And a share or a hold carries none, because there is nothing to settle.
	if _, j := Decide(InterestHigh, Axis{string(ClearanceOurs), "ours"}, "why"); j.Blocking != "" {
		t.Errorf("a share carries a blocking line: %q", j.Blocking)
	}
	if _, j := Decide(InterestLow, Axis{string(ClearanceOurs), "ours"}, "why"); j.Blocking != "" {
		t.Errorf("a hold carries a blocking line: %q", j.Blocking)
	}
}

// The config ships empty and the system fails closed: a fresh deployment
// recommends nothing it has not been told it may recommend.
func TestAnEmptyConfigCanNeverReachShare(t *testing.T) {
	var empty ClearanceConfig

	attributable := Subject{Kind: "package.deployed", Namespace: "gnoswap", Actor: "g1abc"}
	axis := empty.Clear(attributable)
	if Clearance(axis.Level) != ClearanceUnclear {
		t.Fatalf("clearance = %q with no config, want unclear", axis.Level)
	}
	if v, _ := Decide(InterestHigh, axis, "very interesting"); v != VerdictMaybe {
		t.Errorf("the best an attributable event reaches with no config is %q, want maybe", v)
	}

	// The reason has to explain the gap, because it is also how an operator
	// discovers the config file exists.
	_, j := Decide(InterestHigh, axis, "very interesting")
	if !strings.Contains(Reason(VerdictMaybe, j), "clearance") {
		t.Errorf("the reason does not mention clearance: %q", Reason(VerdictMaybe, j))
	}
}

// Nobody privately owns how many wallets joined this week, so chain-level
// events are ours with no configuration at all. Without this the feed would
// have nothing to recommend on an unconfigured deployment, ever.
func TestChainLevelEventsAreOursWithNoConfig(t *testing.T) {
	var empty ClearanceConfig
	for _, kind := range []string{"chain.spike", "proposal.opened", "proposal.closed", "validator.registered"} {
		axis := empty.Clear(Subject{Kind: kind})
		if Clearance(axis.Level) != ClearanceOurs {
			t.Errorf("%s = %q, want ours", kind, axis.Level)
		}
		if v, _ := Decide(InterestHigh, axis, "13 times a normal day"); v != VerdictShare {
			t.Errorf("%s at high interest = %q, want share", kind, v)
		}
	}
}

// Sensitive outranks ours: an event we must not narrate is not made safe by
// belonging to us.
func TestSensitiveOutranksOwnership(t *testing.T) {
	cfg := ClearanceConfig{
		Accounts: []string{"moul"},
		Treasury: []string{"g1treasury"},
		Deny:     []string{"g1denied"},
	}

	// A rejected submission, in our own namespace, is still sensitive.
	axis := cfg.Clear(Subject{Kind: "package.rejected", Namespace: "moul"})
	if Clearance(axis.Level) != ClearanceSensitive {
		t.Errorf("package.rejected in our namespace = %q, want sensitive", axis.Level)
	}
	if v, _ := Decide(InterestHigh, axis, "why"); v != VerdictHold {
		t.Errorf("verdict = %q, want hold", v)
	}

	// A treasury movement, likewise.
	axis = cfg.Clear(Subject{Kind: "transfer.large", Namespace: "moul", Parties: []string{"g1treasury", "g1other"}})
	if Clearance(axis.Level) != ClearanceSensitive {
		t.Errorf("a treasury transfer = %q, want sensitive", axis.Level)
	}

	// And the denylist, which needs no reason recorded.
	axis = cfg.Clear(Subject{Kind: "package.deployed", Namespace: "moul", Parties: []string{"g1denied"}})
	if Clearance(axis.Level) != ClearanceSensitive {
		t.Errorf("a denied address = %q, want sensitive", axis.Level)
	}

	// The same kind without the sensitive trigger is ours again, so the rules
	// above are doing the work rather than the namespace.
	axis = cfg.Clear(Subject{Kind: "package.deployed", Namespace: "moul"})
	if Clearance(axis.Level) != ClearanceOurs {
		t.Errorf("our own deploy = %q, want ours", axis.Level)
	}
}

func TestOtherTeamsAreNamedNotGuessed(t *testing.T) {
	cfg := ClearanceConfig{OtherTeams: []string{"gnoswap"}}
	if axis := cfg.Clear(Subject{Kind: "package.deployed", Namespace: "gnoswap"}); Clearance(axis.Level) != ClearanceTheirs {
		t.Errorf("a configured other team = %q, want theirs", axis.Level)
	}
	// An unconfigured namespace is unclear, never theirs: guessing that
	// somebody else owns something is the same overreach as guessing we do.
	if axis := cfg.Clear(Subject{Kind: "package.deployed", Namespace: "somebodyelse"}); Clearance(axis.Level) != ClearanceUnclear {
		t.Errorf("an unconfigured namespace = %q, want unclear", axis.Level)
	}
}

// The card builds a CSS class from this value, so an open string lets a crafted
// one paint a hold as a share before any script runs.
func TestTheEnumIsClosedInBothDirections(t *testing.T) {
	// Writing: a bad value is our bug and must surface, not be papered over.
	if _, err := json.Marshal(Verdict("share ok")); err == nil {
		t.Error("marshalled a verdict outside the three")
	}
	if _, err := json.Marshal(Verdict("")); err == nil {
		t.Error("marshalled an empty verdict")
	}
	for _, v := range []Verdict{VerdictShare, VerdictMaybe, VerdictHold} {
		if _, err := json.Marshal(v); err != nil {
			t.Errorf("%q did not marshal: %v", v, err)
		}
	}

	// Reading: somebody else's output, so the only safe reading of a value we
	// do not understand is the one that recommends nothing.
	for _, raw := range []string{
		`"share x"`, `"SHARE"`, `""`, `"promote"`, `null`, `42`, `{"a":1}`,
		`"share\" class=\"verdict-share"`,
	} {
		var v Verdict
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Errorf("%s errored instead of failing closed: %v", raw, err)
		}
		if v != VerdictHold {
			t.Errorf("%s unmarshalled to %q, want hold", raw, v)
		}
	}
	// The three real ones survive the round trip.
	for _, want := range []Verdict{VerdictShare, VerdictMaybe, VerdictHold} {
		b, _ := json.Marshal(want)
		var got Verdict
		if err := json.Unmarshal(b, &got); err != nil || got != want {
			t.Errorf("round trip of %q gave %q (%v)", want, got, err)
		}
	}
}

// Relative boundaries, never absolute: a threshold set today quietly marks
// everything high the month the chain doubles.
func TestInterestBucketsAreRelative(t *testing.T) {
	p90, p60 := 100.0, 40.0
	for _, tt := range []struct {
		score float64
		want  Interest
	}{
		{150, InterestHigh}, {100, InterestHigh},
		{99, InterestMedium}, {40, InterestMedium},
		{39, InterestLow}, {0, InterestLow},
	} {
		if got := Bucket(tt.score, p90, p60); got != tt.want {
			t.Errorf("score %v against (%v, %v) = %q, want %q", tt.score, p90, p60, got, tt.want)
		}
	}
	// The same score lands differently once the chain is busier, which is the
	// whole reason the boundaries are passed in.
	if Bucket(100, 1000, 400) != InterestLow {
		t.Error("a score that was high on a quiet chain is not low on a busy one")
	}
}

// The reason may be the only thing anybody reads, so it is always present and
// always fits.
func TestTheReasonIsAlwaysPresentAndFits(t *testing.T) {
	long := strings.Repeat("a very long clause indeed ", 20)
	for _, v := range []Verdict{VerdictShare, VerdictMaybe, VerdictHold} {
		for _, j := range []Judgement{
			{Interest: Axis{"high", "13 times a normal day"}, Clearance: Axis{"ours", "a chain-wide fact belongs to nobody in particular"}},
			{Interest: Axis{"low", long}, Clearance: Axis{"sensitive", long}},
			{Interest: Axis{"medium", ""}, Clearance: Axis{"unclear", ""}},
		} {
			got := Reason(v, j)
			if n := len([]rune(got)); n > MaxVerdictReason {
				t.Errorf("%d characters, budget %d: %q", n, MaxVerdictReason, got)
			}
			if strings.TrimSpace(strings.Trim(got, ".")) == "" && j.Interest.Why != "" {
				t.Errorf("empty reason for %q from %+v", v, j)
			}
		}
	}
}
