package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/config"
)

// abciDataResponse is the shape fetchABCIQuery unwraps.
func abciDataResponse(data string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"response":{"ResponseBase":{"Data":%q}}}}`,
		base64.StdEncoding.EncodeToString([]byte(data)))
}

// nameFromQEval pulls the argument back out of `…ResolveName("x")`, so the fake
// node can answer per name the way the real one does.
func nameFromQEval(body []byte) string {
	var req struct {
		Params struct{ Data string } `json:"params"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	expr, err := base64.StdEncoding.DecodeString(req.Params.Data)
	if err != nil {
		return ""
	}
	_, rest, ok := strings.Cut(string(expr), `ResolveName("`)
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, `"`)
	return name
}

// nsCache is a package-level map keyed by network alone, so each test needs its
// own network id or the second one reads the first one's answers.
func testAPIWithRPC(t *testing.T, url string) (*API, string) {
	t.Helper()
	network := "test-" + t.Name()
	a := &API{networks: []config.NetworkConfig{{ID: network}}}
	a.setRPCVerified(network, url)
	return a, network
}

// The resolve loop used to be sequential, so the cost of a page's namespace
// lookup was the sum of every name's round trip rather than the slowest one.
// This holds every request open until all of them have arrived: a sequential
// loop never gets past the first and fails on the barrier, a fan-out passes.
func TestNamespaceOwnersResolvesConcurrently(t *testing.T) {
	names := []string{"alice", "bob", "carol", "dave", "erin"}

	var mu sync.Mutex
	arrived := 0
	all := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		arrived++
		if arrived == len(names) {
			close(all)
		}
		mu.Unlock()

		select {
		case <-all:
		case <-time.After(5 * time.Second):
			http.Error(w, "the resolves did not overlap", http.StatusGatewayTimeout)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, abciDataResponse(
			`(struct{...} g1`+strings.Repeat("a", 38)+`)`+nameFromQEval(body)))
	}))
	defer srv.Close()

	api, network := testAPIWithRPC(t, srv.URL)
	owners := api.NamespaceOwners(context.Background(), network, names)
	if len(owners) != len(names) {
		t.Fatalf("resolved %d of %d names: %v", len(owners), len(names), owners)
	}
}

// A name the node cannot answer for is absent from the map, not guessed at and
// not fatal to the names around it.
func TestNamespaceOwnersSkipsUnresolvableNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if nameFromQEval(body) == "broken" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, abciDataResponse(`owner: g1`+strings.Repeat("b", 38)))
	}))
	defer srv.Close()

	api, network := testAPIWithRPC(t, srv.URL)
	owners := api.NamespaceOwners(context.Background(), network, []string{"fine", "broken", "alsofine"})

	if _, ok := owners["broken"]; ok {
		t.Error(`"broken" resolved; an unanswerable name must be omitted, not guessed`)
	}
	for _, name := range []string{"fine", "alsofine"} {
		if owners[name] == "" {
			t.Errorf("%q did not resolve; one bad name took the others with it", name)
		}
	}
}
