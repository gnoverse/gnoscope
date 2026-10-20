package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/config"
	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// slowIndexer answers the storage tab's two full-chain queries, each after a
// fixed delay, with one transaction apiece at different heights plus the block
// times for both. The delay is what the test measures: run in sequence the
// handler waits for their sum, concurrently for the longer of the two.
type slowIndexer struct {
	t     *testing.T
	delay time.Duration
}

func (f *slowIndexer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	switch {
	case strings.Contains(req.Query, "__type(name:"):
		http.Error(w, `{"data":{"__type":null}}`, http.StatusOK)
	case strings.Contains(req.Query, "StorageDepositEvent"):
		time.Sleep(f.delay)
		w.Write([]byte(`{"data":{"getTransactions":[{"hash":"storage-tx","block_height":11,"response":{"events":[{"__typename":"StorageDepositEvent","pkg_path":"gno.land/r/demo/thing","bytes_delta":128}]}}]}}`))
	case strings.Contains(req.Query, "MsgAddPackage"):
		time.Sleep(f.delay)
		w.Write([]byte(`{"data":{"getTransactions":[{"hash":"gas-tx","block_height":12,"messages":[]}]}}`))
	case strings.Contains(req.Query, "getBlocks"):
		w.Write([]byte(`{"data":{"getBlocks":[{"height":11,"time":"2026-10-01T00:00:00Z"},{"height":12,"time":"2026-10-02T00:00:00Z"}]}}`))
	default:
		w.Write([]byte(`{"data":{"getTransactions":[]}}`))
	}
}

// The storage tab is keyed on a realm path, so no warmer can ever cover it:
// whoever opens it pays the cold price. It used to pay both queries end to
// end, measured at 9.2s against production on 2026-10-20.
func TestStorageQueriesRunConcurrently(t *testing.T) {
	const network = "storage-concurrency"
	const delay = 300 * time.Millisecond

	srv := httptest.NewServer(&slowIndexer{t: t, delay: delay})
	defer srv.Close()

	a := &API{
		db:       store.NewTestDB(t),
		clients:  map[string]*indexer.Client{network: indexer.NewClient(srv.URL)},
		networks: []config.NetworkConfig{{ID: network}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/storage/{path...}", a.HandleStorage)

	req := httptest.NewRequest("GET", "/api/storage/r/demo/thing?network="+network, nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	mux.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	// Half way between one delay and two: generous enough not to flake on a
	// loaded machine, tight enough that a sequential pair cannot pass.
	if want := delay * 3 / 2; elapsed >= want {
		t.Errorf("handler took %v, want under %v — the two queries ran in sequence", elapsed, want)
	}
}

// Both result sets get their block times, not just the first. The times are
// resolved once for the union of heights and written back into both slices,
// which is the step a copy of their rows would silently skip.
func TestStorageStampsBlockTimesOnBothSets(t *testing.T) {
	const network = "storage-stamping"

	srv := httptest.NewServer(&slowIndexer{t: t})
	defer srv.Close()

	a := &API{
		db:       store.NewTestDB(t),
		clients:  map[string]*indexer.Client{network: indexer.NewClient(srv.URL)},
		networks: []config.NetworkConfig{{ID: network}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/storage/{path...}", a.HandleStorage)

	req := httptest.NewRequest("GET", "/api/storage/r/demo/thing?network="+network, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z"} {
		if !strings.Contains(body, want) {
			t.Errorf("response carries no %s, so one of the two sets went unstamped: %s", want, body)
		}
	}
}
