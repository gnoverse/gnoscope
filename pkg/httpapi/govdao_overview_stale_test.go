package httpapi

import (
	"testing"
	"time"
)

// The gov/dao memos a reader can end up waiting on must all serve stale.
//
// Each one was found the same way: a cold page measured against production,
// then the same page measured again a minute later with a very different
// number. A memo without a grace recomputes on whoever arrives after its TTL,
// and on an explorer this quiet that is most readers.
//
// This is a table rather than one assertion each so that the next expensive
// gov/dao cache has an obvious place to be listed, and no place to hide if it
// is not. It has already earned that once: govDAOProposalCache was the fourth,
// found when the overview stayed slow after the first three were fixed,
// because it reads one of these per proposal.
func TestTheExpensiveGovDAOCachesServeStale(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grace time.Duration
		ttl   time.Duration
	}{
		{"the call list", govDAOTxCache.staleGrace, govDAOTxCache.ttl},
		{"the creation index", creationsCache.staleGrace, creationsCache.ttl},
		{"the overview", govDAOOverviewCache.staleGrace, govDAOOverviewCache.ttl},
		{"one proposal's render", govDAOProposalCache.staleGrace, govDAOProposalCache.ttl},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.grace == 0 {
				t.Fatal("no stale grace: a reader arriving after the TTL recomputes it themselves")
			}
			// A grace shorter than the TTL would cover less than the gap
			// between two visitors, which is the gap it exists for.
			if tc.grace <= tc.ttl {
				t.Errorf("grace %v is not longer than the %v TTL", tc.grace, tc.ttl)
			}
		})
	}
}

// The memos whose answer must not lag keep the plain behaviour. Listed for the
// same reason as above: so that turning one of them stale is a deliberate edit
// to this test rather than a side effect somewhere else.
func TestTheLiveCachesDoNotServeStale(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grace time.Duration
	}{
		{"the validator set", valsetCache.staleGrace},
		{"validator uptime", uptimeCache.staleGrace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.grace != 0 {
				t.Errorf("grace is %v, want none: this one answers about the chain as it is now", tc.grace)
			}
		})
	}
}
