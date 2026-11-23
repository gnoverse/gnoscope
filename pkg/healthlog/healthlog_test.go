package healthlog

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func open(t *testing.T, retentionDays int) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "health.db"), retentionDays)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	// A real interval would let one test write one row.
	s.SetMinGap(0)
	return s
}

func TestRecordAndRead(t *testing.T) {
	s := open(t, 30)
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		s.Record(Sample{At: now.Add(time.Duration(i) * time.Minute), Network: "onyx", State: StateAlive, Height: int64(100 + i)})
	}
	s.Record(Sample{At: now, Network: "pearl", State: StateWedged})

	got, err := s.Samples("onyx", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d onyx samples, want 5", len(got))
	}
	for i, sm := range got {
		if !sm.At.Equal(now.Add(time.Duration(i) * time.Minute)) {
			t.Errorf("sample %d at %v, out of order", i, sm.At)
		}
		if sm.Height != int64(100+i) {
			t.Errorf("sample %d height %d, want %d", i, sm.Height, 100+i)
		}
	}
	// Network-scoped, like every other query in this repo.
	pearl, err := s.Samples("pearl", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if len(pearl) != 1 || pearl[0].State != StateWedged {
		t.Fatalf("pearl = %+v, want one wedged sample", pearl)
	}
}

func TestRecordThrottlesPerNetwork(t *testing.T) {
	s := open(t, 30)
	s.SetMinGap(30 * time.Second)
	now := time.Now().UTC()

	if !s.Record(Sample{At: now, Network: "onyx", State: StateAlive}) {
		t.Fatal("first sample refused")
	}
	if s.Record(Sample{At: now.Add(10 * time.Second), Network: "onyx", State: StateAlive}) {
		t.Fatal("a sample inside the throttle window was taken")
	}
	// Another network is not throttled by onyx's write.
	if !s.Record(Sample{At: now.Add(10 * time.Second), Network: "pearl", State: StateAlive}) {
		t.Fatal("pearl refused because onyx had just been sampled")
	}
	if !s.Record(Sample{At: now.Add(31 * time.Second), Network: "onyx", State: StateAlive}) {
		t.Fatal("a sample past the throttle window was refused")
	}
	if st := s.Stats(); st.Throttled != 1 {
		t.Errorf("throttled = %d, want 1", st.Throttled)
	}
}

func TestRecordRefusesIncompleteSamples(t *testing.T) {
	s := open(t, 30)
	if s.Record(Sample{At: time.Now(), State: StateAlive}) {
		t.Error("a sample with no network was taken")
	}
	if s.Record(Sample{At: time.Now(), Network: "onyx"}) {
		t.Error("a sample with no state was taken")
	}
}

func TestRecordTruncatesAnOversizedDetail(t *testing.T) {
	tests := []struct {
		name   string
		detail string
		want   int
	}{
		{"ascii cuts at the limit", strings.Repeat("x", 4096), detailLimit},
		// A multi-byte rune straddling the limit is dropped whole rather than
		// halved: half a rune comes back out of the JSON encoder as a
		// replacement character, in the field whose job is to say what broke.
		{"a rune is never split", strings.Repeat("x", detailLimit-1) + strings.Repeat("é", 10), detailLimit - 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := open(t, 30)
			now := time.Now().UTC()
			s.Record(Sample{At: now, Network: "onyx", State: StateIndexerDown, Detail: tt.detail})
			got, err := s.Samples("onyx", now.Add(-time.Minute), now.Add(time.Minute))
			if err != nil {
				t.Fatalf("samples: %v", err)
			}
			if len(got) != 1 || len(got[0].Detail) != tt.want {
				t.Fatalf("stored detail is %d bytes, want %d", len(got[0].Detail), tt.want)
			}
			if !utf8.ValidString(got[0].Detail) {
				t.Fatal("stored detail is not valid UTF-8")
			}
		})
	}
}

func TestPruneDeletesPastRetention(t *testing.T) {
	tests := []struct {
		name          string
		retentionDays int
		wantDeleted   int64
		wantLeft      int
	}{
		{"a month keeps the month", 30, 10, 30},
		{"a week keeps the week", 7, 33, 7},
		{"zero keeps everything", 0, 0, 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := open(t, tt.retentionDays)
			now := time.Now().UTC()
			// Forty daily samples, newest an hour ago. The extra hour keeps
			// every row off the retention boundary itself, where "older than
			// 30 days" is a second-level coin toss rather than a behaviour
			// worth asserting.
			for i := 0; i < 40; i++ {
				s.Record(Sample{At: now.Add(-time.Duration(i)*24*time.Hour - time.Hour), Network: "onyx", State: StateAlive})
			}
			s.Flush()

			n, err := s.Prune(now)
			if err != nil {
				t.Fatalf("prune: %v", err)
			}
			if n != tt.wantDeleted {
				t.Errorf("deleted %d rows, want %d", n, tt.wantDeleted)
			}
			left, err := s.Samples("onyx", now.AddDate(0, 0, -400), now.Add(time.Minute))
			if err != nil {
				t.Fatalf("samples: %v", err)
			}
			if len(left) != tt.wantLeft {
				t.Errorf("%d rows left, want %d", len(left), tt.wantLeft)
			}
			// And nothing older than the horizon survived.
			if tt.retentionDays > 0 {
				cutoff := now.AddDate(0, 0, -tt.retentionDays)
				for _, sm := range left {
					if sm.At.Before(cutoff) {
						t.Fatalf("sample at %v survived a %dd retention", sm.At, tt.retentionDays)
					}
				}
			}
		})
	}
}

func TestEarliest(t *testing.T) {
	s := open(t, 30)
	if got, err := s.Earliest("onyx"); err != nil || !got.IsZero() {
		t.Fatalf("Earliest on an empty store = %v, %v; want the zero time", got, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	s.Record(Sample{At: now, Network: "onyx", State: StateAlive})
	s.Record(Sample{At: now.Add(-2 * time.Hour), Network: "onyx", State: StateAlive})
	got, err := s.Earliest("onyx")
	if err != nil {
		t.Fatalf("earliest: %v", err)
	}
	if !got.Equal(now.Add(-2 * time.Hour)) {
		t.Fatalf("earliest = %v, want %v", got, now.Add(-2*time.Hour))
	}
}

// A nil Store is what every instance without -health-db has, and every call
// site is written to need no branch for it.
func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	if s.Record(Sample{At: time.Now(), Network: "onyx", State: StateAlive}) {
		t.Error("a nil store took a sample")
	}
	s.Flush()
	s.Run(nil) //nolint:staticcheck // returns immediately on nil
	if got, err := s.Samples("onyx", time.Time{}, time.Now()); err != nil || got != nil {
		t.Errorf("Samples on nil = %v, %v", got, err)
	}
	if got, err := s.Earliest("onyx"); err != nil || !got.IsZero() {
		t.Errorf("Earliest on nil = %v, %v", got, err)
	}
	if n, err := s.Prune(time.Now()); err != nil || n != 0 {
		t.Errorf("Prune on nil = %d, %v", n, err)
	}
	if s.RetentionDays() != 0 || s.Stats() != (Stats{}) {
		t.Error("nil store reported state")
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close on nil = %v", err)
	}
}

func TestReopenKeepsHistory(t *testing.T) {
	// Restarts are the common case, and a record that does not survive one is
	// no record at all.
	dir := t.TempDir()
	path := filepath.Join(dir, "health.db")
	now := time.Now().UTC().Truncate(time.Second)

	first, err := Open(path, 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	first.SetMinGap(0)
	first.Record(Sample{At: now, Network: "onyx", State: StateIndexerDown, Detail: "429"})
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := Open(path, 30)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	got, err := second.Samples("onyx", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if len(got) != 1 || got[0].Detail != "429" {
		t.Fatalf("after reopen: %+v, want the sample written before the close", got)
	}
}
