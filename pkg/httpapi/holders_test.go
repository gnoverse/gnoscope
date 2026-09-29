package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// /api/holders and the native asset page: the two endpoints where the GRC20
// replay and the native sweep meet in one result, which is exactly where a
// wrong answer would look most authoritative.

func seedHolders(t *testing.T, db *store.DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	xs := []store.TokenTransfer{
		// GNS: g1whale holds 900, g1minnow 100.
		{Token: "gno.land/r/gnoswap/gns.GNS.0000000", PkgPath: "gno.land/r/gnoswap/gns", From: "", To: "g1whale", Value: 1000, BlockHeight: 1, BlockTime: now},
		{Token: "gno.land/r/gnoswap/gns.GNS.0000000", PkgPath: "gno.land/r/gnoswap/gns", From: "g1whale", To: "g1minnow", Value: 100, BlockHeight: 2, BlockTime: now},
		// A second asset, so an address can hold more than one.
		{Token: "gno.land/r/x/rando.RANDO.0000000", PkgPath: "gno.land/r/x/rando", From: "", To: "g1minnow", Value: 500, BlockHeight: 3, BlockTime: now},
		// A GRC721: no amount, so its legs sum to zero and it is a count, not a
		// balance. It must never be valued.
		{Token: "gno.land/r/demo/pics.PIC.0000000", PkgPath: "gno.land/r/demo/pics", From: "", To: "g1minnow", Value: 0, BlockHeight: 4, BlockTime: now},
	}
	for i, x := range xs {
		if err := db.InsertTokenTransfer("alpha", "TXH"+string(rune('a'+i)), 0, x); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := db.UpsertBalances("alpha", []store.BalanceRow{
		{Address: "g1coinly", Network: "alpha", Amount: "500000ugnot", Ugnot: 500000, Height: 9},
		{Address: "g1whale", Network: "alpha", Amount: "10ugnot", Ugnot: 10, Height: 9},
	}); err != nil {
		t.Fatalf("seed balances: %v", err)
	}
}

func TestHandleHolders(t *testing.T) {
	api, db := newTestAPI(t)
	seedHolders(t, db)

	var resp holdersResponse
	getJSON(t, api.HandleHolders, "/api/holders?network=alpha&limit=50&positions=1", &resp)

	byAddr := map[string]holderRow{}
	for _, h := range resp.Holders {
		byAddr[h.Address] = h
	}

	// g1minnow holds three assets: GNS, RANDO and one NFT.
	minnow, ok := byAddr["g1minnow"]
	if !ok {
		t.Fatal("g1minnow is missing from the ranking")
	}
	if minnow.Assets != 3 {
		t.Errorf("g1minnow holds %d assets, want 3: %+v", minnow.Assets, minnow.Positions)
	}
	if minnow.HoldsNative {
		t.Error("g1minnow has no swept ugnot balance and must not be marked as holding native")
	}

	// g1whale holds GNS and, from the sweep, ugnot.
	whale, ok := byAddr["g1whale"]
	if !ok {
		t.Fatal("g1whale is missing")
	}
	if !whale.HoldsNative {
		t.Error("g1whale has a swept balance, so its row includes a position that came from a sample rather than a replay, and must say so")
	}

	// The sweep is in the ranking, and the response says how big the sample was.
	if !resp.NativeIncluded || resp.NativeSwept != 2 {
		t.Errorf("native_included=%v native_swept=%d, want true/2", resp.NativeIncluded, resp.NativeSwept)
	}
	// An address that holds only ugnot still ranks.
	if _, ok := byAddr["g1coinly"]; !ok {
		t.Error("g1coinly holds only the native coin and was dropped from a ranking that claims to span every asset")
	}
}

// A GRC721 position is a count of items, not a balance, so it must never carry
// a dollar value however the prices happen to land.
func TestHandleHoldersNeverValuesAnNFT(t *testing.T) {
	api, db := newTestAPI(t)
	seedHolders(t, db)

	var resp holdersResponse
	getJSON(t, api.HandleHolders, "/api/holders?network=alpha&limit=50&positions=1", &resp)
	for _, h := range resp.Holders {
		for _, p := range h.Positions {
			if p.Kind == store.KindGRC721 && (p.Priced || p.USDValue != 0) {
				t.Errorf("%s: NFT position %s came back priced at %v", h.Address, p.Token, p.USDValue)
			}
		}
	}
}

// native=0 is how a reader asks for the replayed ledger alone, which is the
// only view where every row was computed the same way.
func TestHandleHoldersCanExcludeTheSweep(t *testing.T) {
	api, db := newTestAPI(t)
	seedHolders(t, db)

	var resp holdersResponse
	getJSON(t, api.HandleHolders, "/api/holders?network=alpha&limit=50&native=0", &resp)
	if resp.NativeIncluded {
		t.Error("native_included is true with native=0")
	}
	for _, h := range resp.Holders {
		if h.Address == "g1coinly" {
			t.Error("g1coinly holds only ugnot and must disappear when the sweep is excluded")
		}
		if h.HoldsNative {
			t.Errorf("%s still carries a native position", h.Address)
		}
	}
}

// Ranking must be deterministic. With most assets unpriced, most rows have a
// value of exactly zero, and without the tie-breaks they would come back in Go
// map order, which changes between requests and reads as data churning.
func TestHandleHoldersIsStablyOrdered(t *testing.T) {
	api, db := newTestAPI(t)
	seedHolders(t, db)

	var first []string
	for i := 0; i < 5; i++ {
		var resp holdersResponse
		getJSON(t, api.HandleHolders, "/api/holders?network=alpha&limit=50", &resp)
		var order []string
		for _, h := range resp.Holders {
			order = append(order, h.Address)
		}
		if i == 0 {
			first = order
			continue
		}
		if len(order) != len(first) {
			t.Fatalf("pass %d returned %d rows, first returned %d", i, len(order), len(first))
		}
		for j := range order {
			if order[j] != first[j] {
				t.Fatalf("pass %d differs at %d: %v vs %v", i, j, order, first)
			}
		}
	}
}

func TestHandleHoldersFiltersByToken(t *testing.T) {
	api, db := newTestAPI(t)
	seedHolders(t, db)

	// Built with url.Values rather than spliced into a string: an event key is
	// full of slashes and dots that have to be escaped to survive a query, and
	// the raw form also trips scripts/check-tree.sh, which reads `token=<value>`
	// as a credential. Encoding it is the right answer to both.
	q := url.Values{"network": {"alpha"}, "limit": {"50"}, "positions": {"1"},
		"token": {"gno.land/r/gnoswap/gns.GNS.0000000"}}
	var resp holdersResponse
	getJSON(t, api.HandleHolders, "/api/holders?"+q.Encode(), &resp)
	if len(resp.Holders) != 2 {
		t.Fatalf("got %d holders of GNS, want 2: %+v", len(resp.Holders), resp.Holders)
	}
	for _, h := range resp.Holders {
		if h.Assets != 1 {
			t.Errorf("%s shows %d assets under a single-token filter", h.Address, h.Assets)
		}
	}
}

func TestHandleHoldersLimit(t *testing.T) {
	api, db := newTestAPI(t)
	seedHolders(t, db)

	var resp holdersResponse
	getJSON(t, api.HandleHolders, "/api/holders?network=alpha&limit=1", &resp)
	if len(resp.Holders) != 1 {
		t.Fatalf("got %d rows, want 1", len(resp.Holders))
	}
	// The cut must not hide how many there were.
	if resp.TotalHolders < 3 {
		t.Errorf("total_holders = %d, want the full count regardless of the page", resp.TotalHolders)
	}
}

// The chain's own coin answers the token detail endpoint, on the same shape, so
// one page draws either.
func TestHandleAssetServesTheNativeCoin(t *testing.T) {
	api, db := newTestAPI(t)
	seedHolders(t, db)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := db.InsertBankSend("alpha", "BS1", 7, now, "g1coinly", "g1whale", "4200ugnot", true); err != nil {
		t.Fatalf("seed send: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/asset/x?network=alpha", nil)
	req.SetPathValue("token", store.NativeKey)
	api.HandleAsset(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var resp assetDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Asset.Kind != store.KindNative {
		t.Errorf("kind = %q, want native", resp.Asset.Kind)
	}
	if len(resp.Holders) != 2 {
		t.Errorf("got %d holders, want the 2 swept balances", len(resp.Holders))
	}
	if len(resp.Transfers) != 1 || resp.Transfers[0].Value != 4200 {
		t.Errorf("transfers = %+v, want the one bank send", resp.Transfers)
	}
	// A GRC20's supply series is the running sum of its mints. bank_sends has
	// never seen a mint, so a cumulative line over it would draw volume and
	// label it supply. The flow series is what this ledger can honestly draw.
	if len(resp.Supply) != 0 {
		t.Errorf("native returned %d supply points; its supply is set by the chain, not by this ledger", len(resp.Supply))
	}
	if len(resp.Flow) == 0 {
		t.Error("native returned no flow series, which is the one chart its ledger can draw")
	}
	// ugnot belongs to no package.
	if resp.Realm != nil || len(resp.Siblings) != 0 {
		t.Errorf("native came back with a realm (%v) or siblings (%d)", resp.Realm, len(resp.Siblings))
	}
}

// A GRC20 gains the flow series too, so one chart component draws both kinds.
func TestHandleAssetCarriesAFlowSeries(t *testing.T) {
	api, db := newTestAPI(t)
	seedHolders(t, db)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/asset/x?network=alpha", nil)
	req.SetPathValue("token", "gno.land/r/gnoswap/gns.GNS.0000000")
	api.HandleAsset(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp assetDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Flow) == 0 {
		t.Fatal("no flow series on a grc20")
	}
	var volume int64
	for _, p := range resp.Flow {
		volume += p.Volume
	}
	// The mint of 1000 is activity, not value moved between holders. Only the
	// 100 that went from g1whale to g1minnow is volume.
	if volume != 100 {
		t.Errorf("flow volume = %d, want 100: the 1000 mint is not movement between holders", volume)
	}
}
