package indexer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The inert lifecycle feeds ask for every MsgEnablePackage and every
// MsgRejectPackage on the chain. Both predicates are sparse — mainnet held 620
// and 25 of them at height 925,618 — so the widening window they used to go
// through could never reach its quota and walked all six windows down to
// genesis, re-querying from scratch each time, to end up running the full-chain
// query anyway. Measured against indexer.gno.land on 2026-10-19 that was 9.66s
// and 21.55s; asking directly is 4.85s and 12.77s.
//
// So what this locks in is the shape, not the speed: one query, no height
// bound, and no tip lookup to compute one with.
func TestPackageLifecycleQueriesAskOnceForTheWholeChain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    func(*Client, context.Context) ([]Transaction, error)
		msgType string
	}{
		{
			name: "enables",
			call: func(c *Client, ctx context.Context) ([]Transaction, error) {
				return c.GetPackageEnableTransactions(ctx)
			},
			msgType: "MsgEnablePackage",
		},
		{
			name: "rejects",
			call: func(c *Client, ctx context.Context) ([]Transaction, error) {
				return c.GetPackageRejectTransactions(ctx)
			},
			msgType: "MsgRejectPackage",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var txQueries []string
			tipLookups := 0

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				query := string(body)
				w.Header().Set("Content-Type", "application/json")

				mu.Lock()
				switch {
				case strings.Contains(query, "latestBlockHeight"):
					tipLookups++
				case strings.Contains(query, "getTransactions"):
					txQueries = append(txQueries, query)
				}
				mu.Unlock()

				if strings.Contains(query, "latestBlockHeight") {
					io.WriteString(w, `{"data":{"latestBlockHeight":925618}}`)
					return
				}
				if strings.Contains(query, "getTransactions") {
					io.WriteString(w, `{"data":{"getTransactions":[{"hash":"A","block_height":12}]}}`)
					return
				}
				io.WriteString(w, `{"data":{}}`)
			}))
			defer srv.Close()

			txs, err := tc.call(NewClient(srv.URL), context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(txs) != 1 {
				t.Errorf("got %d transactions, want 1", len(txs))
			}

			mu.Lock()
			defer mu.Unlock()
			if len(txQueries) != 1 {
				t.Fatalf("made %d getTransactions queries, want exactly 1 (the widening window is back)", len(txQueries))
			}
			// `block_height` is also a selected field, so match the bound itself.
			if strings.Contains(txQueries[0], "block_height: { gt:") {
				t.Errorf("the query carries a height bound, so it does not cover the whole chain:\n%s", txQueries[0])
			}
			if !strings.Contains(txQueries[0], tc.msgType) {
				t.Errorf("query does not filter on %s:\n%s", tc.msgType, txQueries[0])
			}
			if tipLookups != 0 {
				t.Errorf("looked the chain tip up %d times; an unwindowed query has no use for it", tipLookups)
			}
		})
	}
}
