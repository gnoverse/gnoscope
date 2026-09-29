package traffic

import (
	"testing"
	"time"
)

func TestVisitorStableWithinADay(t *testing.T) {
	s := newSaltRotator()
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	later := time.Date(2026, 9, 29, 23, 59, 0, 0, time.UTC)

	a := s.visitor("203.0.113.7", "Mozilla/5.0", now)
	b := s.visitor("203.0.113.7", "Mozilla/5.0", later)
	if a == "" {
		t.Fatal("visitor id is empty")
	}
	if a != b {
		t.Errorf("same client on the same day got two ids: %q and %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("id is %d chars, want 16", len(a))
	}
}

func TestVisitorDiffersAcrossDays(t *testing.T) {
	s := newSaltRotator()
	day1 := s.visitor("203.0.113.7", "Mozilla/5.0", time.Date(2026, 9, 29, 23, 59, 0, 0, time.UTC))
	day2 := s.visitor("203.0.113.7", "Mozilla/5.0", time.Date(2026, 9, 30, 0, 1, 0, 0, time.UTC))
	if day1 == day2 {
		t.Fatal("the same client got the same id on two days; the salt did not rotate, " +
			"and yesterday's rows are now linkable to today's")
	}
}

func TestVisitorSeparatesClients(t *testing.T) {
	s := newSaltRotator()
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	if s.visitor("203.0.113.7", "A", now) == s.visitor("203.0.113.7", "B", now) {
		t.Error("two user agents on one address collapsed to one id")
	}
	if s.visitor("203.0.113.7", "A", now) == s.visitor("203.0.113.8", "A", now) {
		t.Error("two addresses collapsed to one id")
	}
	// The separator byte is why these two differ. Without it both hash
	// "203.0.113.45" + "Go" as the same byte string.
	if s.visitor("203.0.113.4", "5Go", now) == s.visitor("203.0.113.45", "Go", now) {
		t.Error("ip/ua boundary is ambiguous: the separator byte is missing")
	}
}

func TestVisitorEmptyWithoutIP(t *testing.T) {
	s := newSaltRotator()
	if got := s.visitor("", "Mozilla/5.0", time.Now()); got != "" {
		t.Errorf("visitor with no address = %q, want empty", got)
	}
}

// The property this whole design rests on: nothing stored can be turned back
// into an address. The only check available from outside is that the id does
// not contain the input, which is what a naive implementation would leak.
//
// The needles are deliberately non-hex. An earlier version of this test also
// looked for "203" and "113" from the address, and failed roughly one run in a
// hundred because a 16-character hex string contains a given 3-digit run by
// chance: 958ccb51138e7cc4 holds "113". A test that fails at random teaches
// people to re-run it, which is worse than not having it.
func TestVisitorDoesNotEmbedInput(t *testing.T) {
	s := newSaltRotator()
	id := s.visitor("203.0.113.7", "Mozilla/5.0 (Macintosh)", time.Now())
	for _, needle := range []string{"Mozilla", "Macintosh"} {
		if len(id) > 0 && contains(id, needle) {
			t.Errorf("stored id %q contains %q from the input", id, needle)
		}
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
