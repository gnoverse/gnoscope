package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// The one promise this file pins: whenever what gnoscope serves is older than
// the chain, /cockpit says so, and says which half is late.
//
// An explorer that is down is a problem somebody notices. An explorer that
// answers every page from data that stopped moving is one nobody does, so the
// three ways that happens each get a case here: an upstream that refuses us
// (429), an indexer that answers but trails the node, and a testnet that was
// reset under a stored index. Each case states what the two upstreams said and
// what is stored, and the assertion is always the same shape: the verdict is
// not "all clear", and a finding names the stale half.

// gradeStale is what HandleCockpit does after its reads, for one network:
// diagnose, lay the row out, grade it. The row arithmetic is cockpitNetworks'
// own two lines, repeated here because that function reads the tip from the
// database; everything graded goes through the production diagnose,
// cockpitFindings and cockpitVerdict.
func gradeStale(node NodeState, live store.SanityLiveness, indexedHeight int) ([]CockpitFinding, CockpitVerdict) {
	row := CockpitNetwork{
		Network:           "onyx",
		Diagnosis:         diagnose(node, live),
		ChainHeight:       live.ChainHeight,
		Reachable:         live.Reachable,
		LastBlockTime:     live.LastBlockTime,
		SecondsSinceBlock: live.SecondsSinceBlock,
		Indexed:           true,
		IndexedHeight:     indexedHeight,
	}
	if live.Reachable && live.ChainHeight > indexedHeight {
		row.LagBlocks = live.ChainHeight - indexedHeight
	}
	resp := cockpitResponse{
		// An hour up, so the cold-start grace never hides a case.
		Service:  CockpitService{UptimeSeconds: 3600},
		Networks: []CockpitNetwork{row},
	}
	findings := cockpitFindings(resp)
	return findings, cockpitVerdict(findings)
}

// refusingNode is an RPC that answers every path with 429, which is what
// rpc.gno.land does for the whole of its penalty window.
func refusingNode(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		http.Error(w, `{"error":{"code":-32000,"message":"Per-IP rate limit exceeded; retry after 600 seconds."}}`,
			http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// refusedIndexer is what livenessOf makes of an indexer answering 429, read
// through a real client rather than assumed.
func refusedIndexer(t *testing.T) store.SanityLiveness {
	t.Helper()
	fake, client := indexer.NewFake(t)
	fake.Status = http.StatusTooManyRequests
	fake.StatusBody = `{"code":-32000,"message":"Per-IP rate limit exceeded; retry after 300 seconds."}`
	return livenessOf(context.Background(), client)
}

func TestCockpitSignalsStaleData(t *testing.T) {
	const tip = 1_402_900 // onyx's height the week this was written

	// A node with nothing wrong with it, at the tip.
	node := NodeState{Reachable: true, Height: tip, Peers: 13, RoundAgeSecond: 2}
	// An indexer with nothing wrong with it, at the tip.
	fresh := store.SanityLiveness{
		Reachable: true, IsAlive: true, ChainHeight: tip,
		LastBlockTime: time.Now().UTC().Add(-3 * time.Second).Format(time.RFC3339), SecondsSinceBlock: 3,
	}
	// The same indexer, answering from n blocks ago. secs is how old its
	// newest block is, which is what decides IsAlive.
	late := func(n, secs int) store.SanityLiveness {
		return store.SanityLiveness{
			Reachable: true, IsAlive: secs < 120, ChainHeight: tip - n,
			LastBlockTime:     time.Now().UTC().Add(-time.Duration(secs) * time.Second).Format(time.RFC3339),
			SecondsSinceBlock: secs,
		}
	}

	tests := []struct {
		name    string
		node    func(t *testing.T) NodeState
		live    func(t *testing.T) store.SanityLiveness
		indexed int

		wantState   string // the diagnosis
		wantFinding string // the finding that must be reported, "" for none
		wantVerdict string
		wantIn      string // a phrase the finding has to carry
	}{
		// The control. Without it every case below could pass on a build that
		// simply reports everything, always.
		{
			name:        "control: both upstreams at the tip report nothing",
			node:        func(*testing.T) NodeState { return node },
			live:        func(*testing.T) store.SanityLiveness { return fresh },
			indexed:     tip,
			wantState:   DiagAlive,
			wantVerdict: CockpitOK,
		},

		// --- 1. An upstream refuses us (429) ---------------------------------
		{
			// History stops moving and nothing on a page says so: the cockpit
			// has to. Down, because every page is now served from storage.
			name:        "429 from the indexer: the stored history is called stale",
			node:        func(*testing.T) NodeState { return node },
			live:        refusedIndexer,
			indexed:     tip - 400,
			wantState:   DiagIndexerDown,
			wantFinding: "chain",
			wantVerdict: CockpitDown,
			wantIn:      "history and search are stale",
		},
		{
			// A warning, not an outage: the indexer still answers. But the
			// reason has to survive to the page, or "the node is not
			// answering" sends an operator to look for a dead node.
			name: "429 from the node: live state is called unavailable, and the 429 is named",
			node: func(t *testing.T) NodeState {
				return probeNode(context.Background(), refusingNode(t).URL)
			},
			live:        func(*testing.T) store.SanityLiveness { return fresh },
			indexed:     tip,
			wantState:   DiagNodeUnreachable,
			wantFinding: "chain",
			wantVerdict: CockpitDegraded,
			wantIn:      "429",
		},
		{
			name: "429 from both: nothing can be vouched for",
			node: func(t *testing.T) NodeState {
				return probeNode(context.Background(), refusingNode(t).URL)
			},
			live:        refusedIndexer,
			indexed:     tip - 400,
			wantState:   DiagUnknown,
			wantFinding: "chain",
			wantVerdict: CockpitDown,
			wantIn:      "neither the node nor the indexer answered",
		},

		// --- 2. The indexer answers, late ------------------------------------
		{
			// The case that was invisible. The index has caught up with the
			// indexer, so the cockpit's own lag is zero, and the indexer's
			// newest block is 50 minutes old. The chain is fine; the finding
			// must blame the indexer and carry the gap.
			name:        "an indexer 1000 blocks behind the node is named, with the gap",
			node:        func(*testing.T) NodeState { return node },
			live:        func(*testing.T) store.SanityLiveness { return late(1000, 3000) },
			indexed:     tip - 1000,
			wantState:   DiagIndexerBehind,
			wantFinding: "chain",
			wantVerdict: CockpitDegraded,
			wantIn:      "1000 blocks behind",
		},
		{
			// A fast chain: the newest indexed block is under two minutes old,
			// so every age-based check passes, and the gap is still real.
			name:        "an indexer behind by blocks but not yet by the clock is still named",
			node:        func(*testing.T) NodeState { return node },
			live:        func(*testing.T) store.SanityLiveness { return late(60, 90) },
			indexed:     tip - 60,
			wantState:   DiagIndexerBehind,
			wantFinding: "chain",
			wantVerdict: CockpitDegraded,
			wantIn:      "60 blocks behind",
		},
		{
			// The other control: an indexer always trails the node by its own
			// polling delay, and a page that is always amber is a page nobody
			// reads.
			name:        "control: a few blocks of polling delay is not a finding",
			node:        func(*testing.T) NodeState { return node },
			live:        func(*testing.T) store.SanityLiveness { return late(4, 12) },
			indexed:     tip - 4,
			wantState:   DiagAlive,
			wantVerdict: CockpitOK,
		},

		// --- 3. The testnet was reset ----------------------------------------
		{
			// The new chain answers from a low height while the index still
			// holds the old one. Both upstreams look healthy, the lag is zero,
			// and every stored row is about a chain that no longer exists. The
			// syncer discards them on its next pass; this is the window before
			// it does, and the window never closes while the indexer refuses.
			name: "a reset chain under an old index is reported",
			node: func(*testing.T) NodeState {
				return NodeState{Reachable: true, Height: 120, Peers: 13, RoundAgeSecond: 2}
			},
			live: func(*testing.T) store.SanityLiveness {
				return store.SanityLiveness{
					Reachable: true, IsAlive: true, ChainHeight: 120,
					LastBlockTime: time.Now().UTC().Add(-3 * time.Second).Format(time.RFC3339), SecondsSinceBlock: 3,
				}
			},
			indexed:     tip,
			wantState:   DiagAlive,
			wantFinding: "index-ahead",
			wantVerdict: CockpitDegraded,
			wantIn:      "reset",
		},
		{
			// A node of our own left on the old chain: it still answers
			// /status at the old height, with no peers and a round that never
			// advances. That has to read as down, not as a chain at the tip.
			name: "a node left behind on the old chain is down, not alive",
			node: func(*testing.T) NodeState {
				return NodeState{
					Reachable: true, Height: tip, Peers: 0,
					RoundHeight: tip + 1, Step: 1, StepName: "NewHeight",
					RoundStart: "2026-11-20T10:00:00Z", RoundAgeSecond: 5 * 86400,
				}
			},
			live:        func(*testing.T) store.SanityLiveness { return late(0, 5*86400) },
			indexed:     tip,
			wantState:   DiagWedged,
			wantFinding: "chain",
			wantVerdict: CockpitDown,
			wantIn:      "has no peers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, live := tt.node(t), tt.live(t)

			if got := diagnose(node, live); got.State != tt.wantState {
				t.Fatalf("diagnosis = %q (%s), want %q", got.State, got.Detail, tt.wantState)
			}

			findings, verdict := gradeStale(node, live, tt.indexed)
			if verdict.State != tt.wantVerdict {
				t.Errorf("verdict = %q (%s), want %q; findings: %+v", verdict.State, verdict.Headline, tt.wantVerdict, findings)
			}
			if tt.wantFinding == "" {
				if len(findings) != 0 {
					t.Errorf("findings = %+v, want none", findings)
				}
				return
			}

			var found *CockpitFinding
			for i := range findings {
				if findings[i].ID == tt.wantFinding {
					found = &findings[i]
				}
			}
			if found == nil {
				t.Fatalf("no %q finding, so stale data would be served unannounced; got %+v", tt.wantFinding, findings)
			}
			if text := found.Title + " | " + found.Detail; !strings.Contains(text, tt.wantIn) {
				t.Errorf("finding = %q, want it to carry %q", text, tt.wantIn)
			}
			if found.Network != "onyx" {
				t.Errorf("finding names network %q, want onyx", found.Network)
			}
		})
	}
}

// The mislabel the indexer_behind state replaces, pinned on its own because it
// is the half that is easy to lose in a reordering of diagnose's cases: an
// indexer whose newest block is old must not be reported as a chain that
// "has stopped producing blocks" while the node says the chain is ahead of it.
func TestALateIndexerIsNotBlamedOnTheChain(t *testing.T) {
	node := NodeState{Reachable: true, Height: 10_000, Peers: 13, RoundAgeSecond: 2}
	live := store.SanityLiveness{
		Reachable: true, IsAlive: false, ChainHeight: 9_000,
		SecondsSinceBlock: 3000, LastBlockTime: "2026-11-25T08:00:00Z",
	}

	got := diagnose(node, live)
	if got.State == DiagStale {
		t.Fatalf("diagnosis = stale (%s): the chain is 1000 blocks ahead of the indexer, so the silence is the indexer's", got.Detail)
	}
	if title := cockpitChainTitle("onyx", got.State); !strings.Contains(title, "indexer") {
		t.Errorf("title = %q, want the indexer named", title)
	}

	// And the state it replaces still exists for what it was written for: no
	// recent block, and no evidence that the node is ahead.
	quiet := diagnose(NodeState{Reachable: true, Height: 9_000, Peers: 13, RoundAgeSecond: 2}, live)
	if quiet.State != DiagStale {
		t.Errorf("a quiet chain = %q, want stale", quiet.State)
	}
}
