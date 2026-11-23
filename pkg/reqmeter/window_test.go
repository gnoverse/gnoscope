package reqmeter

import (
	"testing"
	"time"
)

var base = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

// A rolling window's whole job is to forget, and the moment it forgets is the
// only place it can be wrong. The ring reuses a slot every secBuckets seconds
// and every minBuckets minutes, so a slot written exactly one full lap ago sits
// at the position the current interval wants and must read as zero rather than
// as this interval's traffic.
func TestWindowBoundary(t *testing.T) {
	tests := []struct {
		name    string
		at      []time.Duration // when requests were recorded, relative to base
		read    time.Duration   // when the window is read, relative to base
		last60  int64
		last300 int64
		last1h  int64
	}{
		{
			name: "nothing recorded",
			read: 0,
		},
		{
			name:    "all inside the minute",
			at:      []time.Duration{0, 10 * time.Second, 30 * time.Second},
			read:    40 * time.Second,
			last60:  3,
			last300: 3,
			last1h:  3,
		},
		{
			name: "a second rolls out of the minute but stays in the window",
			// 59s before the read is inside the last 60; 61s before is not.
			at:      []time.Duration{0, 59 * time.Second},
			read:    60 * time.Second,
			last60:  1,
			last300: 2,
			last1h:  2,
		},
		{
			name:    "exactly one second-ring lap later, the old slot is not this one",
			at:      []time.Duration{0},
			read:    secBuckets * time.Second,
			last60:  0,
			last300: 0,
			last1h:  1, // the minute ring still holds it
		},
		{
			name:    "one second before the lap it is still counted",
			at:      []time.Duration{0},
			read:    (secBuckets - 1) * time.Second,
			last60:  0,
			last300: 1,
			last1h:  1,
		},
		{
			name:    "exactly one minute-ring lap later, the day has rolled over",
			at:      []time.Duration{0},
			read:    minBuckets * time.Minute,
			last60:  0,
			last300: 0,
			last1h:  0,
		},
		{
			name:    "an hour and a minute later is out of the hour",
			at:      []time.Duration{0},
			read:    61 * time.Minute,
			last60:  0,
			last300: 0,
			last1h:  0,
		},
		{
			name:    "fifty nine minutes later is still in the hour",
			at:      []time.Duration{0},
			read:    59 * time.Minute,
			last60:  0,
			last300: 0,
			last1h:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var w window
			for _, d := range tc.at {
				w.add(base.Add(d), false)
			}
			read := base.Add(tc.read)
			if got, _ := w.lastSeconds(read, 60); got != tc.last60 {
				t.Errorf("last 60s = %d, want %d", got, tc.last60)
			}
			if got, _ := w.lastSeconds(read, int64(BudgetWindow.Seconds())); got != tc.last300 {
				t.Errorf("last 300s = %d, want %d", got, tc.last300)
			}
			if got, _ := w.lastMinutes(read, 60); got != tc.last1h {
				t.Errorf("last 1h = %d, want %d", got, tc.last1h)
			}
		})
	}
}

// A slot from the future is what a clock stepped backwards leaves behind, and
// counting it would attribute requests to a second that has not happened.
func TestWindowIgnoresTheFuture(t *testing.T) {
	var w window
	w.add(base.Add(10*time.Second), false)
	if got, _ := w.lastSeconds(base, 60); got != 0 {
		t.Fatalf("a request recorded ten seconds from now counted as %d in the last minute", got)
	}
}

// The peak is the figure the budget is actually about: a day under the ceiling
// on average with one window four times over it is the shape that earns a 429,
// and an average would report that day as fine.
func TestWindowPeak(t *testing.T) {
	var w window
	// A quiet hour, then a burst inside one minute, then quiet again.
	for i := 0; i < 10; i++ {
		w.add(base.Add(time.Duration(i)*time.Minute), false)
	}
	burst := base.Add(20 * time.Minute)
	for i := 0; i < 40; i++ {
		w.add(burst.Add(time.Duration(i)*time.Millisecond), false)
	}
	read := base.Add(90 * time.Minute)

	peak, at := w.peakRun(read, 1)
	if peak != 40 {
		t.Errorf("peak minute = %d, want 40", peak)
	}
	if !at.Equal(burst.Truncate(time.Minute)) {
		t.Errorf("peak minute at %s, want %s", at, burst.Truncate(time.Minute))
	}

	// Five consecutive minutes starting at the burst: the burst plus nothing.
	// The quiet hour is one request a minute, so no other run of five can beat it.
	peak5, _ := w.peakRun(read, 5)
	if peak5 != 40 {
		t.Errorf("peak 5 minutes = %d, want 40", peak5)
	}

	// And the same window read a day later has forgotten all of it.
	peakLater, _ := w.peakRun(base.Add(25*time.Hour), 1)
	if peakLater != 0 {
		t.Errorf("peak minute a day later = %d, want 0", peakLater)
	}
}

// A run of five minutes that is individually unremarkable can still beat the
// single busiest minute, which is exactly why the budget is graded on the run.
func TestWindowPeakRunBeatsPeakMinute(t *testing.T) {
	var w window
	// One minute with 30, then five consecutive minutes with 20 each.
	spike := base.Add(5 * time.Minute)
	for i := 0; i < 30; i++ {
		w.add(spike, false)
	}
	for m := 20; m < 25; m++ {
		for i := 0; i < 20; i++ {
			w.add(base.Add(time.Duration(m)*time.Minute), false)
		}
	}
	read := base.Add(40 * time.Minute)

	if peak, _ := w.peakRun(read, 1); peak != 30 {
		t.Errorf("peak minute = %d, want 30", peak)
	}
	peak5, at := w.peakRun(read, 5)
	if peak5 != 100 {
		t.Errorf("peak 5 minutes = %d, want 100", peak5)
	}
	wantAt := base.Add(20 * time.Minute).UTC()
	if !at.Equal(wantAt) {
		t.Errorf("peak run began at %s, want %s", at, wantAt)
	}
}
