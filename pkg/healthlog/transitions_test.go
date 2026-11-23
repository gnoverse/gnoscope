package healthlog

import (
	"testing"
	"time"
)

var base = time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)

// series builds a sample run: one sample per minute from `from`, one state per
// entry, repeated `n` times.
type leg struct {
	state string
	n     int
}

func series(from time.Time, step time.Duration, legs ...leg) []Sample {
	var out []Sample
	at := from
	for _, l := range legs {
		for i := 0; i < l.n; i++ {
			out = append(out, Sample{At: at, Network: "onyx", State: l.state, Detail: l.state + " detail"})
			at = at.Add(step)
		}
	}
	return out
}

func TestTransitions(t *testing.T) {
	min := time.Minute
	tests := []struct {
		name    string
		samples []Sample
		now     time.Time
		want    []Transition
	}{
		{
			name:    "no samples at all, which is what a fresh instance looks like",
			samples: nil,
			now:     base,
			want:    nil,
		},
		{
			name:    "one sample is one ongoing run, not a zero-length one",
			samples: series(base, min, leg{StateAlive, 1}),
			now:     base.Add(30 * time.Second),
			want: []Transition{
				{State: StateAlive, Healthy: true, Start: base, End: base.Add(30 * time.Second), Samples: 1, Ongoing: true},
			},
		},
		{
			name:    "a steady week collapses to one run",
			samples: series(base, min, leg{StateAlive, 120}),
			now:     base.Add(120 * min),
			want: []Transition{
				{State: StateAlive, Healthy: true, Start: base, End: base.Add(120 * min), Samples: 120, Ongoing: true},
			},
		},
		{
			name: "the five-minute outage from the issue",
			samples: series(base.Add(-44*min), min,
				leg{StateAlive, 44}, leg{StateIndexerDown, 5}, leg{StateAlive, 10}),
			now: base.Add(15 * min),
			want: []Transition{
				{State: StateAlive, Healthy: true, Start: base.Add(-44 * min), End: base, Samples: 44},
				{State: StateIndexerDown, Start: base, End: base.Add(5 * min), Samples: 5},
				{State: StateAlive, Healthy: true, Start: base.Add(5 * min), End: base.Add(15 * min), Samples: 10, Ongoing: true},
			},
		},
		{
			name: "a flap is four runs, not one average",
			samples: series(base, min,
				leg{StateAlive, 1}, leg{StateIndexerDown, 1}, leg{StateAlive, 1}, leg{StateIndexerDown, 1}),
			now: base.Add(4 * min),
			want: []Transition{
				{State: StateAlive, Healthy: true, Start: base, End: base.Add(min), Samples: 1},
				{State: StateIndexerDown, Start: base.Add(min), End: base.Add(2 * min), Samples: 1},
				{State: StateAlive, Healthy: true, Start: base.Add(2 * min), End: base.Add(3 * min), Samples: 1},
				{State: StateIndexerDown, Start: base.Add(3 * min), End: base.Add(4 * min), Samples: 1, Ongoing: true},
			},
		},
		{
			name: "a three-day outage is one run, still open",
			samples: series(base, min,
				leg{StateAlive, 2}, leg{StateWedged, 3 * 24 * 60}),
			now: base.Add(2*min + 3*24*60*min),
			want: []Transition{
				{State: StateAlive, Healthy: true, Start: base, End: base.Add(2 * min), Samples: 2},
				{State: StateWedged, Start: base.Add(2 * min), End: base.Add(2*min + 3*24*60*min), Samples: 3 * 24 * 60, Ongoing: true},
			},
		},
		{
			name: "a hole in the series is no_data, not more of the same state",
			samples: append(
				series(base, min, leg{StateAlive, 3}),
				series(base.Add(60*min), min, leg{StateAlive, 2})...),
			now: base.Add(62 * min),
			want: []Transition{
				{State: StateAlive, Healthy: true, Start: base, End: base.Add(2 * min), Samples: 3},
				{State: StateNoData, Start: base.Add(2 * min), End: base.Add(60 * min)},
				{State: StateAlive, Healthy: true, Start: base.Add(60 * min), End: base.Add(62 * min), Samples: 2, Ongoing: true},
			},
		},
		{
			name:    "a sampler that stopped leaves an open no_data run, not an ongoing claim",
			samples: series(base, min, leg{StateAlive, 3}),
			now:     base.Add(120 * min),
			want: []Transition{
				{State: StateAlive, Healthy: true, Start: base, End: base.Add(2 * min), Samples: 3},
				{State: StateNoData, Start: base.Add(2 * min), End: base.Add(120 * min), Ongoing: true},
			},
		},
		{
			name: "samples out of order are sorted, not treated as gaps",
			samples: []Sample{
				{At: base.Add(2 * min), State: StateAlive},
				{At: base, State: StateAlive},
				{At: base.Add(min), State: StateAlive},
			},
			now: base.Add(2 * min),
			want: []Transition{
				{State: StateAlive, Healthy: true, Start: base, End: base.Add(2 * min), Samples: 3, Ongoing: true},
			},
		},
		{
			name:    "a now older than the newest sample never yields a negative run",
			samples: series(base, min, leg{StateAlive, 3}),
			now:     base,
			want: []Transition{
				{State: StateAlive, Healthy: true, Start: base, End: base.Add(2 * min), Samples: 3, Ongoing: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Transitions(tt.samples, GapTolerance, tt.now)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d runs, want %d:\n got %+v\nwant %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				g, w := got[i], tt.want[i]
				if g.State != w.State || g.Healthy != w.Healthy || !g.Start.Equal(w.Start) ||
					!g.End.Equal(w.End) || g.Samples != w.Samples || g.Ongoing != w.Ongoing {
					t.Errorf("run %d:\n got %s %v..%v samples=%d ongoing=%v\nwant %s %v..%v samples=%d ongoing=%v",
						i, g.State, g.Start, g.End, g.Samples, g.Ongoing,
						w.State, w.Start, w.End, w.Samples, w.Ongoing)
				}
			}
		})
	}
}

func TestTransitionCarriesTheFirstDetailOfItsRun(t *testing.T) {
	samples := []Sample{
		{At: base, State: StateIndexerDown, Detail: "connection refused"},
		{At: base.Add(time.Minute), State: StateIndexerDown, Detail: "connection refused, again"},
	}
	got := Transitions(samples, GapTolerance, base.Add(time.Minute))
	if len(got) != 1 {
		t.Fatalf("got %d runs, want 1", len(got))
	}
	if got[0].Detail != "connection refused" {
		t.Errorf("detail = %q, want the first sample's", got[0].Detail)
	}
}

func TestSummarize(t *testing.T) {
	min := time.Minute
	tests := []struct {
		name          string
		samples       []Sample
		now           time.Time
		wantUptime    float64
		wantOutages   int
		wantDown      int
		wantNoData    int
		wantLastState string
		wantCurrent   string
	}{
		{
			name:          "a clean window",
			samples:       series(base, min, leg{StateAlive, 100}),
			now:           base.Add(100 * min),
			wantUptime:    100,
			wantLastState: "",
			wantCurrent:   StateAlive,
		},
		{
			name: "one outage, recovered",
			samples: series(base, min,
				leg{StateAlive, 90}, leg{StateIndexerDown, 5}, leg{StateAlive, 5}),
			now:           base.Add(100 * min),
			wantUptime:    95,
			wantOutages:   1,
			wantDown:      300,
			wantLastState: StateIndexerDown,
			wantCurrent:   StateAlive,
		},
		{
			name: "two outages and a hole: the hole is neither up nor down",
			samples: append(
				series(base, min, leg{StateAlive, 8}, leg{StateWedged, 2}),
				series(base.Add(70*min), min, leg{StateAlive, 8}, leg{StateIndexerDown, 2})...),
			now:         base.Add(80 * min),
			wantUptime:  80,
			wantOutages: 2,
			// 60s, not 120s: the wedged run is cut short by the hole that
			// follows it, so it is credited only to its last observation.
			wantDown:      60 + 120,
			wantNoData:    61 * 60,
			wantLastState: StateIndexerDown,
			wantCurrent:   StateIndexerDown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Summarize(Transitions(tt.samples, GapTolerance, tt.now))
			if s.UptimePercent != tt.wantUptime {
				t.Errorf("uptime = %v, want %v", s.UptimePercent, tt.wantUptime)
			}
			if s.Outages != tt.wantOutages {
				t.Errorf("outages = %d, want %d", s.Outages, tt.wantOutages)
			}
			if s.DownSeconds != tt.wantDown {
				t.Errorf("down = %ds, want %ds", s.DownSeconds, tt.wantDown)
			}
			if s.NoDataSeconds != tt.wantNoData {
				t.Errorf("no_data = %ds, want %ds", s.NoDataSeconds, tt.wantNoData)
			}
			last := ""
			if s.LastOutage != nil {
				last = s.LastOutage.State
			}
			if last != tt.wantLastState {
				t.Errorf("last outage = %q, want %q", last, tt.wantLastState)
			}
			cur := ""
			if s.Current != nil {
				cur = s.Current.State
			}
			if cur != tt.wantCurrent {
				t.Errorf("current = %q, want %q", cur, tt.wantCurrent)
			}
		})
	}
}

func TestSummarizeOfNothingClaimsNothing(t *testing.T) {
	s := Summarize(Transitions(nil, GapTolerance, base))
	if s.Samples != 0 || s.UptimePercent != 0 || s.Current != nil || s.LastOutage != nil {
		t.Fatalf("a fresh instance summarised as %+v, want all zero", s)
	}
}

func TestBuckets(t *testing.T) {
	min := time.Minute
	now := base.Add(6 * time.Hour)
	// Six hourly cells. The third hour holds one wedged sample among alive
	// ones; the fifth holds nothing at all.
	var samples []Sample
	for h := 0; h < 6; h++ {
		if h == 4 {
			continue
		}
		for m := 0; m < 60; m += 10 {
			st := StateAlive
			if h == 2 && m == 30 {
				st = StateWedged
			}
			samples = append(samples, Sample{At: base.Add(time.Duration(h)*time.Hour + time.Duration(m)*min), State: st})
		}
	}

	cells := Buckets(samples, now, time.Hour, 6)
	if len(cells) != 6 {
		t.Fatalf("got %d cells, want 6", len(cells))
	}
	want := []struct {
		state   string
		samples int
		healthy int
	}{
		{StateAlive, 6, 6},
		{StateAlive, 6, 6},
		{StateWedged, 6, 5},
		{StateAlive, 6, 6},
		{"", 0, 0},
		{StateAlive, 6, 6},
	}
	for i, w := range want {
		g := cells[i]
		if g.State != w.state || g.Samples != w.samples || g.Healthy != w.healthy {
			t.Errorf("cell %d = %q samples=%d healthy=%d, want %q samples=%d healthy=%d",
				i, g.State, g.Samples, g.Healthy, w.state, w.samples, w.healthy)
		}
		wantStart := base.Add(time.Duration(i) * time.Hour)
		if !g.Start.Equal(wantStart) {
			t.Errorf("cell %d starts %v, want %v", i, g.Start, wantStart)
		}
	}
}

func TestBucketsWithNoSamplesAreEmptyNotHealthy(t *testing.T) {
	cells := Buckets(nil, base, time.Hour, 24)
	if len(cells) != 24 {
		t.Fatalf("got %d cells, want 24", len(cells))
	}
	for i, c := range cells {
		if c.State != "" || c.Samples != 0 {
			t.Fatalf("cell %d = %q with %d samples, want an empty cell", i, c.State, c.Samples)
		}
	}
}

func TestBucketsKeepASampleTakenAtNow(t *testing.T) {
	// The newest cell is the one a reader looks at, and a sample stamped
	// exactly at `now` lands at index `cells` by arithmetic. Dropping it made
	// a healthy chain's last cell read as empty.
	cells := Buckets([]Sample{{At: base, State: StateAlive}}, base, time.Hour, 3)
	if cells[2].Samples != 1 {
		t.Fatalf("newest cell has %d samples, want 1", cells[2].Samples)
	}
}

func TestSeverityRanksAnUnknownStateWorst(t *testing.T) {
	if Severity("something-this-build-never-heard-of") <= Severity(StateUnknown) {
		t.Fatal("an unrecognised state must not outrank a known bad one")
	}
	if Severity(StateAlive) != 0 {
		t.Fatal("alive must be the floor")
	}
}
