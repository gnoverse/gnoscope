package httpapi

import (
	"testing"
	"time"
)

// A background refresh has to outlive the slowest work it will ever be asked to
// do, or that work is never refreshed in the background at all: the refresh is
// cancelled, nothing is stored, the entry ages out of its grace, and the next
// reader computes it inline.
//
// Not theoretical. On 2026-10-26 backgroundRefreshTimeout was 60s while
// priceReadTimeout was 90s, so /api/prices had never once been refreshed off a
// reader's critical path. The server logged one of them paying for it:
//
//	slow GET /api/holders in 1m22.635s, positions;dur=730.8, prices;dur=81869.6
//
// which is past http.Server's 30s WriteTimeout, so that reader got a 502 rather
// than a slow page.
//
// A new endpoint that gives its own work a longer budget than the background
// window belongs in this list, and fails here until the window is raised past
// it.
func TestBackgroundRefreshOutlivesTheSlowestEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget time.Duration
	}{
		{"a price refresh", priceReadTimeout},
		{"pool discovery", poolDiscoveryTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if backgroundRefreshTimeout <= tc.budget {
				t.Errorf("backgroundRefreshTimeout is %v and %s is %v: that refresh can never finish off the critical path",
					backgroundRefreshTimeout, tc.name, tc.budget)
			}
		})
	}
}

// And the whole point of raising it is that a reader never waits that long, so
// the window must also stay above what http.Server will tolerate in front of
// one. If these ever cross, a refresh is being run somewhere a reader is
// waiting.
func TestTheWriteTimeoutIsNotTheRefreshBudget(t *testing.T) {
	const serverWriteTimeout = 30 * time.Second // main.go
	if backgroundRefreshTimeout <= serverWriteTimeout {
		t.Errorf("backgroundRefreshTimeout %v is inside the %v write timeout, so background work is sized for a reader's connection",
			backgroundRefreshTimeout, serverWriteTimeout)
	}
}
