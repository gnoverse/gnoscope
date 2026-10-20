package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// countingIndexer answers the three queries a gov/dao call fetch makes — the
// capability probe, the chain tip and getTransactions — and counts only the
// last, which is the expensive one. Every getTransactions comes back empty, so
// the window ladder widens all the way to genesis exactly as it does against
// mainnet, where gov/dao has never had anything close to the 500 rows asked
// for.
type countingIndexer struct {
	mu    sync.Mutex
	scans int
}

func (f *countingIndexer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	switch {
	case strings.Contains(req.Query, "__type(name:"):
		fmt.Fprint(w, `{"data":{"__type":null}}`)
	case strings.Contains(req.Query, "latestBlockHeight"):
		fmt.Fprint(w, `{"data":{"latestBlockHeight":944396}}`)
	default:
		f.mu.Lock()
		f.scans++
		f.mu.Unlock()
		fmt.Fprint(w, `{"data":{"getTransactions":[]}}`)
	}
}

func (f *countingIndexer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scans
}

// A proposal page reads the gov/dao call list twice (govDAORelatedCalls and
// AuditGovDAOProposal) and the overview reads it once per proposal, all
// concurrently. Each read is a widening scan that reaches genesis — six
// sequential queries, 10.2s against the production indexer on 2026-10-20 — so
// the list has to be fetched once and shared, or a proposal page pays for it
// twice and /govdao once per proposal.
func TestGovDAOTransactionsAreFetchedOncePerNetwork(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reads int
	}{
		{"one proposal page", 2},
		{"the overview with eight proposals", 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &countingIndexer{}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			// Unique per case: the cache is keyed on the network, and two cases
			// sharing one key would have the second read the first's answer.
			network := strings.ReplaceAll(tc.name, " ", "-")
			a := &API{
				db:      store.NewTestDB(t),
				clients: map[string]*indexer.Client{network: indexer.NewClient(srv.URL)},
			}

			if _, ok := a.fetchGovDAOTransactions(context.Background(), network); !ok {
				t.Fatal("no indexer client for the network")
			}
			ladder := fake.count()
			if ladder < 2 {
				t.Fatalf("the first read issued %d queries, want the widening ladder", ladder)
			}

			var wg sync.WaitGroup
			for range tc.reads - 1 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					a.fetchGovDAOTransactions(context.Background(), network)
				}()
			}
			wg.Wait()

			if got := fake.count(); got != ladder {
				t.Errorf("%d reads cost %d queries, want %d — the list was re-scanned", tc.reads, got, ladder)
			}
		})
	}
}

// A network with no client is not a network whose gov/dao list is empty, and
// the cache must not blur the two: the bool is what the overview uses to say
// "this instance cannot reach that chain" rather than "that chain is quiet".
func TestGovDAOTransactionsReportsAMissingClient(t *testing.T) {
	a := &API{db: store.NewTestDB(t), clients: map[string]*indexer.Client{}}
	if txs, ok := a.fetchGovDAOTransactions(context.Background(), "nowhere"); ok || txs != nil {
		t.Errorf("got (%v, %v), want (nil, false)", txs, ok)
	}
}
