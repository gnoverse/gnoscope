package healthlog

import (
	"sort"
	"time"
)

// The chain states this package stores and ranks.
//
// Declared here rather than imported from pkg/httpapi because the dependency
// runs the other way: httpapi writes these rows. They are the same strings as
// the Diag* constants in pkg/httpapi/chainhealth.go, and
// TestDiagStatesMatchHealthlog there keeps the two vocabularies identical. Left
// to drift it fails quietly: an unranked state sorts as the worst thing in its
// bucket, so one renamed constant would paint a healthy week red.
const (
	StateAlive           = "alive"
	StateSyncing         = "syncing"
	StateStale           = "stale"
	StateNodeUnreachable = "node_unreachable"
	StateIndexerDown     = "indexer_down"
	StateWedged          = "wedged"
	StateUnknown         = "unknown"

	// StateNoData is never sampled. It is what the derivation emits for a
	// stretch nobody was watching: this process was down, or the sampler was,
	// and the honest answer is that we do not know rather than that the chain
	// was fine. A strip that draws a restart as green is a strip that lies on
	// exactly the day somebody reads it.
	StateNoData = "no_data"
)

// severity ranks the states worst-last, for "what was the worst thing that
// happened in this bucket".
//
// The order is the one /cockpit already grades by: alive is the only good one,
// the two "we cannot see" states sit in the middle, and wedged outranks them
// because a stopped chain is worse than a lost view of a running one.
var severity = map[string]int{
	StateAlive:           0,
	StateSyncing:         1,
	StateStale:           2,
	StateNodeUnreachable: 3,
	StateIndexerDown:     4,
	StateWedged:          5,
	StateUnknown:         6,
}

// RankedStates is the stored vocabulary, for the test in pkg/httpapi that keeps
// it identical to the diagnosis states.
func RankedStates() []string {
	out := make([]string, 0, len(severity))
	for s := range severity {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return severity[out[i]] < severity[out[j]] })
	return out
}

// Severity ranks one state. An unrecognised state ranks worst, so a state this
// build has never heard of shows up rather than hiding behind "alive".
func Severity(state string) int {
	if r, ok := severity[state]; ok {
		return r
	}
	return len(severity)
}

// Healthy says whether a state is one nobody has to act on. Only alive is.
func Healthy(state string) bool { return state == StateAlive }

// Transition is one maximal run of consecutive samples sharing a state.
//
// This is the record worth reading. Ten thousand rows saying "alive" are one
// sentence, and "onyx was unreachable from 07:44 to 07:49, 5 minutes" is the
// thing a reader came for. Derived rather than stored: the samples are the
// facts, and a stored transition would be a second copy of them that has to be
// corrected whenever a late or out-of-order sample lands.
type Transition struct {
	State   string
	Healthy bool
	Start   time.Time
	// End is when the next run was first observed, not when the last sample of
	// this one landed. Within one sampling interval those are the same claim,
	// and the first one does not report a blip seen exactly once as lasting
	// zero seconds.
	End time.Time
	// Samples is how many rows back this run. Zero for a no_data run, which is
	// backed by no rows at all, and the reason uptime is computed from sample
	// counts rather than from durations.
	Samples int
	// Detail is the sentence the first sample of the run carried, which is the
	// one naming what broke. Later samples in the same run usually repeat it.
	Detail string
	// Ongoing marks the run that reaches `now`.
	Ongoing bool
}

// Duration is how long the run lasted.
func (t Transition) Duration() time.Duration { return t.End.Sub(t.Start) }

// Transitions collapses a sample series into runs.
//
// gap is how far apart two consecutive samples may be before the stretch
// between them counts as unobserved rather than as more of the same state. Pass
// a small multiple of the sampling interval; a shorter one invents outages out
// of a slow tick, a longer one hides a restart.
//
// now closes the final run. Passing it in rather than calling time.Now keeps
// this a pure function, which is the whole point of deriving instead of
// storing: the flap and the three-day outage below are both a table of inputs
// and an expected table of outputs.
//
// Samples need not arrive sorted; a copy is sorted here. The query orders them,
// but a caller splicing two sources should not have to know that.
func Transitions(samples []Sample, gap time.Duration, now time.Time) []Transition {
	if len(samples) == 0 {
		return nil
	}
	s := make([]Sample, len(samples))
	copy(s, samples)
	sort.SliceStable(s, func(i, j int) bool { return s[i].At.Before(s[j].At) })

	var out []Transition
	cur := Transition{
		State:   s[0].State,
		Healthy: Healthy(s[0].State),
		Start:   s[0].At,
		Samples: 1,
		Detail:  s[0].Detail,
	}
	prev := s[0].At
	for _, sm := range s[1:] {
		switch {
		case gap > 0 && sm.At.Sub(prev) > gap:
			// Nobody was watching in between. Close the run at its last
			// observation, not at the next one: claiming the state held across
			// a gap is exactly the lie this branch exists to avoid.
			cur.End = prev
			out = append(out, cur)
			out = append(out, Transition{State: StateNoData, Start: prev, End: sm.At})
			cur = Transition{State: sm.State, Healthy: Healthy(sm.State), Start: sm.At, Samples: 1, Detail: sm.Detail}
		case sm.State != cur.State:
			cur.End = sm.At
			out = append(out, cur)
			cur = Transition{State: sm.State, Healthy: Healthy(sm.State), Start: sm.At, Samples: 1, Detail: sm.Detail}
		default:
			cur.Samples++
		}
		prev = sm.At
	}

	// The tail. A sampler that stopped long ago leaves an open no_data run
	// rather than an "ongoing" state it has no evidence for.
	if gap > 0 && now.Sub(prev) > gap {
		cur.End = prev
		out = append(out, cur)
		out = append(out, Transition{State: StateNoData, Start: prev, End: now, Ongoing: true})
		return out
	}
	cur.End = now
	if cur.End.Before(prev) {
		// A clock that went backwards, or a caller passing a `now` older than
		// its newest row. Report the run as zero-length rather than negative.
		cur.End = prev
	}
	cur.Ongoing = true
	out = append(out, cur)
	return out
}

// Summary is what the page says in words, computed from the runs.
type Summary struct {
	Samples        int
	HealthySamples int
	// UptimePercent is healthy samples over observed samples. Deliberately not
	// healthy *time* over wall time: the stretches nobody watched would
	// otherwise have to be counted as up or as down, and they are neither.
	// NoDataSeconds beside it is how a reader sees how much of the window that
	// figure actually covers.
	UptimePercent float64
	Outages       int
	DownSeconds   int
	NoDataSeconds int
	// LastOutage is the most recent unhealthy run, ongoing or not. This is the
	// literal question the health page could not answer: when was it last
	// unreachable, and for how long.
	LastOutage *Transition
	// Current is the run in progress, which may be a no_data run.
	Current *Transition
}

// Summarize reduces the runs to the handful of numbers a reader needs.
func Summarize(trs []Transition) Summary {
	var s Summary
	for i := range trs {
		t := trs[i]
		s.Samples += t.Samples
		switch {
		case t.State == StateNoData:
			s.NoDataSeconds += int(t.Duration().Seconds())
		case t.Healthy:
			s.HealthySamples += t.Samples
		default:
			s.Outages++
			s.DownSeconds += int(t.Duration().Seconds())
			s.LastOutage = &trs[i]
		}
		if t.Ongoing {
			s.Current = &trs[i]
		}
	}
	if s.Samples > 0 {
		s.UptimePercent = 100 * float64(s.HealthySamples) / float64(s.Samples)
	}
	return s
}

// Bucket is one cell of the availability strip.
type Bucket struct {
	Start time.Time
	// State is the worst state observed in the cell, and "" when the cell holds
	// no sample at all. Empty is a third colour on the page, never green and
	// never red: a cell with no evidence must not be drawn as either answer.
	State   string
	Samples int
	Healthy int
}

// Buckets lays samples onto a fixed grid of `cells` cells of `cell` each,
// ending at `now`.
//
// Anchored to now rather than to the newest sample, for the same reason
// bucketTicks is in pkg/httpapi/chainhealth.go: a sampler that stopped an hour
// ago has to show an hour of empty cells, and a grid anchored to its last row
// would show a full strip and look fine.
func Buckets(samples []Sample, now time.Time, cell time.Duration, cells int) []Bucket {
	if cells <= 0 || cell <= 0 {
		return nil
	}
	start := now.Add(-cell * time.Duration(cells))
	out := make([]Bucket, cells)
	for i := range out {
		out[i].Start = start.Add(cell * time.Duration(i))
	}
	for _, sm := range samples {
		idx := int(sm.At.Sub(start) / cell)
		switch {
		case idx < 0:
			continue
		case idx >= cells:
			// At or a shade past now: a sample taken while the response was
			// being assembled, or a clock a second ahead. Within one cell that
			// belongs in the newest cell; beyond it the timestamp is wrong and
			// counting it would invent history.
			if sm.At.Sub(now) >= cell {
				continue
			}
			idx = cells - 1
		}
		b := &out[idx]
		b.Samples++
		if Healthy(sm.State) {
			b.Healthy++
		}
		if b.State == "" || Severity(sm.State) > Severity(b.State) {
			b.State = sm.State
		}
	}
	return out
}
