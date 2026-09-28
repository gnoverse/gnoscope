package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// The holdings endpoint is the realm defi tab pointed at an account, so what
// has to hold is that it reaches the same ledgers for an address nobody can
// derive a package path for. A realm's page could always answer this; an
// account's could not, and the address is the only input either one really had.
func TestAddressHoldingsReadsTheSameLedgersAsTheRealmTab(t *testing.T) {
	db := store.NewTestDB(t)
	const net = "mainnet"
	const holder = "g1holder00000000000000000000000000000"

	if err := db.InsertTokenTransfer(net, "TX1", 0, store.TokenTransfer{
		Token: "gno.land/r/demo/tok.TOK.0", From: "", To: holder,
		Value: 900, BlockHeight: 10, BlockTime: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("InsertTokenTransfer: %v", err)
	}
	if err := db.InsertTokenTransfer(net, "TX2", 0, store.TokenTransfer{
		Token: "gno.land/r/demo/tok.TOK.0", From: holder, To: "g1other",
		Value: 400, BlockHeight: 11, BlockTime: "2026-01-02T00:00:00Z"}); err != nil {
		t.Fatalf("InsertTokenTransfer: %v", err)
	}

	api := NewAPI(db, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/address/"+holder+"/holdings?network="+net, nil)
	req.SetPathValue("addr", holder)
	api.HandleAddressHoldings(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Address string `json:"address"`
		Path    string `json:"path"`
		Tokens  []struct {
			Token    string `json:"token"`
			Balance  int64  `json:"balance"`
			Received int64  `json:"received"`
			Sent     int64  `json:"sent"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Address != holder {
		t.Errorf("address %q, want %q", got.Address, holder)
	}
	// An account has no package path, and reporting one would invent a realm.
	if got.Path != "" {
		t.Errorf("path %q on a plain account, want empty", got.Path)
	}
	if len(got.Tokens) != 1 {
		t.Fatalf("%d token positions, want 1: %+v", len(got.Tokens), got.Tokens)
	}
	if got.Tokens[0].Balance != 500 || got.Tokens[0].Received != 900 || got.Tokens[0].Sent != 400 {
		t.Errorf("position %+v, want balance 500 from 900 in and 400 out", got.Tokens[0])
	}
}

// Denominated figures cannot be blended across chains, and the realm tab
// already refuses that case. The account one has to refuse it identically, or
// the same question answers two different ways depending which page asked.
func TestAddressHoldingsRefusesTheAllNetworksCase(t *testing.T) {
	db := store.NewTestDB(t)
	api := NewAPI(db, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/address/g1x/holdings", nil)
	req.SetPathValue("addr", "g1x")
	api.HandleAddressHoldings(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d with no network, want 400", rec.Code)
	}
}

// The denominator counts only what can be earned.
//
// session-key marks a delegated signing address, which a master account can
// never become. Counted, it gave every human reader a ceiling one below the one
// printed with nothing saying which badge was impossible: moul's page read
// "21 of 26" against a real 25.
func TestAddressAchievementsExcludeMarkersFromTheScore(t *testing.T) {
	db := store.NewTestDB(t)
	api := NewAPI(db, nil, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/address/g1nobody/achievements", nil)
	req.SetPathValue("addr", "g1nobody")
	api.HandleAddressAchievements(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Total        int `json:"total"`
		Achievements []struct {
			Slug   string `json:"slug"`
			Marker bool   `json:"marker"`
		} `json:"achievements"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	markers := 0
	for _, a := range got.Achievements {
		if a.Marker {
			markers++
		}
	}
	if markers == 0 {
		t.Fatal("no marker came back, so this test proves nothing")
	}
	// Still served: on a session address the marker is the useful line. Just
	// not counted.
	if want := len(got.Achievements) - markers; got.Total != want {
		t.Errorf("total %d over %d entries with %d markers, want %d",
			got.Total, len(got.Achievements), markers, want)
	}
}
