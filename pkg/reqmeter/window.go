package reqmeter

import "time"

// A rolling count of requests, in two resolutions and in fixed memory.
//
// Two resolutions because two different questions are being asked. "What are we
// spending right now" wants seconds and only needs to reach back one budget
// window; "what was the worst it got today" wants a whole day, and a day of
// per-second buckets to answer a question about minutes would be 86,400 slots
// for no extra truth.
//
// Each slot carries the index of the interval it holds. That is what makes this
// rolling rather than cumulative: a slot whose index is older than the window
// reads as zero without anybody having to sweep it, so there is no cleanup
// goroutine and no boundary at which counts are lost. It is also the one thing
// here with a real edge case, which is why TestWindowBoundary exists: a slot
// written 1,441 minutes ago sits at the same ring position as the current
// minute, and must not be mistaken for it.
const (
	secBuckets = 300  // one budget window, to the second
	minBuckets = 1440 // one day, to the minute
)

type window struct {
	sec [secBuckets]slot
	min [minBuckets]slot
}

type slot struct {
	// idx is the interval this slot holds: unix seconds for sec, unix minutes
	// for min.
	idx     int64
	sent    int64
	refused int64
}

func (w *window) add(now time.Time, refused bool) {
	s := now.Unix()
	bump(&w.sec[mod(s, secBuckets)], s, refused)
	m := s / 60
	bump(&w.min[mod(m, minBuckets)], m, refused)
}

func bump(sl *slot, idx int64, refused bool) {
	if sl.idx != idx {
		*sl = slot{idx: idx}
	}
	sl.sent++
	if refused {
		sl.refused++
	}
}

// mod is a modulo that stays non-negative. Unix seconds are positive in every
// realistic case, but a test clock or a badly wrong system date should produce
// a wrong number rather than a panic.
func mod(v, n int64) int64 {
	r := v % n
	if r < 0 {
		r += n
	}
	return r
}

// lastSeconds counts the requests in the n most recent seconds, n capped at the
// retained span.
func (w *window) lastSeconds(now time.Time, n int64) (sent, refused int64) {
	if n > secBuckets {
		n = secBuckets
	}
	cur := now.Unix()
	for i := range w.sec {
		// idx > cur is a slot from the future, which a clock stepped backwards
		// can leave behind. Counting it would attribute requests to a second
		// that has not happened.
		if w.sec[i].idx <= cur && w.sec[i].idx > cur-n {
			sent += w.sec[i].sent
			refused += w.sec[i].refused
		}
	}
	return sent, refused
}

// lastMinutes counts the requests in the n most recent minutes.
func (w *window) lastMinutes(now time.Time, n int64) (sent, refused int64) {
	if n > minBuckets {
		n = minBuckets
	}
	cur := now.Unix() / 60
	for i := range w.min {
		if w.min[i].idx <= cur && w.min[i].idx > cur-n {
			sent += w.min[i].sent
			refused += w.min[i].refused
		}
	}
	return sent, refused
}

// series lays the retained day out oldest to newest, so the last element is the
// minute in progress. Zero for a minute nothing was recorded in, which is
// indistinguishable from a minute before the meter started: see Snapshot.Since
// for the honesty note that goes with it.
func (w *window) series(now time.Time) []int64 {
	cur := now.Unix() / 60
	out := make([]int64, minBuckets)
	for i := range w.min {
		d := cur - w.min[i].idx
		if d < 0 || d >= minBuckets {
			continue
		}
		out[minBuckets-1-d] = w.min[i].sent
	}
	return out
}

// peakRun is the busiest run of n consecutive minutes in the retained day, with
// the instant that run began.
//
// n=1 answers "the peak minute". n=5 answers the question the budget actually
// poses, since the ceiling is BudgetRequests per BudgetWindow and not a rate:
// one minute at 4x the sustainable rate is fine if the four minutes around it
// are quiet, and four minutes at 1.2x is not. Minute-aligned, so the true worst
// 300 seconds can straddle two of these runs and be up to one minute's traffic
// higher than what this reports.
func (w *window) peakRun(now time.Time, n int) (count int64, at time.Time) {
	if n < 1 {
		n = 1
	}
	if n > minBuckets {
		n = minBuckets
	}
	s := w.series(now)
	var sum int64
	for i := 0; i < n && i < len(s); i++ {
		sum += s[i]
	}
	best, bestAt := sum, 0
	for i := n; i < len(s); i++ {
		sum += s[i] - s[i-n]
		if sum > best {
			best, bestAt = sum, i-n+1
		}
	}
	cur := now.Unix() / 60
	startMin := cur - int64(minBuckets-1-bestAt)
	return best, time.Unix(startMin*60, 0).UTC()
}
