package traffic

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Counting people without keeping them.
//
// "How many readers" and "which one of them is this" are different questions,
// and only the first one is any of this server's business. A raw IP answers
// both, which is exactly why it is not stored: it is personal data at rest, it
// obliges a retention and deletion policy the moment it lands, and it buys
// nothing the dashboard actually shows.
//
// So the stored identity is HMAC(key-of-the-day, ip + ua). Within a UTC day two
// requests from the same client collapse to the same string, which is what
// "unique visitors" and "sessions" need. Across days they do not, and cannot be
// made to: the key is 32 bytes from crypto/rand, lives only in this process's
// memory, is replaced at the first request after midnight UTC, and is never
// written anywhere. Nobody, including whoever holds the database file, can
// reverse a stored id to an address or link yesterday's ids to today's, because
// the key that would do it no longer exists.
//
// The deliberate cost: a reader active across midnight is counted twice, and
// "returning visitor over a week" is unanswerable. Both are fine. Neither is
// worth keeping an address on disk for.
type saltRotator struct {
	mu  sync.Mutex
	day string
	key []byte
}

func newSaltRotator() *saltRotator { return &saltRotator{} }

// keyFor returns the key for now's UTC day, minting a new one if the day turned.
func (s *saltRotator) keyFor(now time.Time) []byte {
	day := now.UTC().Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.day != day || len(s.key) == 0 {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			// crypto/rand failing is not a condition this process can serve
			// through, and degrading to a weak key here would quietly turn a
			// one-way hash into a guessable one. Better to stop identifying
			// visitors than to pretend an id is unlinkable when it is not.
			s.day, s.key = day, nil
			return nil
		}
		s.day, s.key = day, key
	}
	return s.key
}

// visitor is the stored id for one client on one day, or "" when no key could
// be minted. Truncated to 16 hex characters: enough that collisions do not
// distort a count at this scale, short enough that the table stays small.
func (s *saltRotator) visitor(ip, ua string, now time.Time) string {
	key := s.keyFor(now)
	if key == nil || ip == "" {
		return ""
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(ip))
	m.Write([]byte{0}) // separator, so "1.2.3.4"+"5Go" and "1.2.3.45"+"Go" differ
	m.Write([]byte(ua))
	return hex.EncodeToString(m.Sum(nil))[:16]
}
