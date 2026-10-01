package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/store"
)

const (
	moulAddr  = "g1manfred47kzduec920z88wfr64ylksmdcedlf5"
	richAddr  = "g1qyfled5ulf6wmu2u4smstn8rlzazanmqt0n8kh"
	poorAddr  = "g1qyzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
	otherAddr = "g1r0000000000000000000000000000000000000"
)

func seedAddressSearch(t *testing.T, db *store.DB) {
	t.Helper()
	for net, rows := range map[string][]store.BalanceRow{
		"alpha": {
			{Address: moulAddr, Amount: "5ugnot", Ugnot: 5},
			{Address: richAddr, Amount: "900ugnot", Ugnot: 900},
			{Address: poorAddr, Amount: "1ugnot", Ugnot: 1},
			{Address: otherAddr, Amount: "7ugnot", Ugnot: 7},
		},
		// Same chain-agnostic address on another network: a scoped search must
		// not return it, and an unscoped one returns it once per network.
		"beta": {{Address: richAddr, Amount: "3ugnot", Ugnot: 3}},
	} {
		if err := db.UpsertBalances(net, rows); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []store.User{
		{Name: "moul", Address: moulAddr, BlockHeight: 10},
		{Name: "oldmoul", Address: moulAddr, BlockHeight: 5, Alias: true},
	} {
		if err := db.UpsertUser("alpha", u); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHandleAddressSearchMatchesByPrefix(t *testing.T) {
	api, db := newTestAPI(t)
	seedAddressSearch(t, db)

	cases := []struct {
		name string
		url  string
		want []string // network/address, in order
	}{
		{"the reported fragment", "/api/addresses/search?q=g1qyfled5ulf6wmu&network=alpha",
			[]string{"alpha/" + richAddr}},
		{"short prefix ranks richest first", "/api/addresses/search?q=g1qy&network=alpha",
			[]string{"alpha/" + richAddr, "alpha/" + poorAddr}},
		{"case and whitespace do not matter", "/api/addresses/search?q=%20G1QYFLED%20&network=alpha",
			[]string{"alpha/" + richAddr}},
		{"a complete address matches itself", "/api/addresses/search?q=" + moulAddr + "&network=alpha",
			[]string{"alpha/" + moulAddr}},
		{"no network spans the configured ones", "/api/addresses/search?q=g1qyf",
			[]string{"alpha/" + richAddr, "beta/" + richAddr}},
		{"scoped to the other network", "/api/addresses/search?q=g1qyf&network=beta",
			[]string{"beta/" + richAddr}},
		{"bare hrp is not a query", "/api/addresses/search?q=g1&network=alpha", nil},
		{"a name is not an address", "/api/addresses/search?q=moul&network=alpha", nil},
		{"nothing past the last address", "/api/addresses/search?q=g1zz&network=alpha", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp addressSearchResponse
			getJSON(t, api.HandleAddressSearch, tc.url, &resp)
			if resp.Addresses == nil {
				t.Fatal("addresses is null, want an array even when empty")
			}
			var got []string
			for _, a := range resp.Addresses {
				got = append(got, a.Network+"/"+a.Address)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A match carries what tells two accounts apart before either is opened: the
// current registered name (never an alias), the curated label, the balance.
func TestHandleAddressSearchCarriesIdentity(t *testing.T) {
	api, db := newTestAPI(t)
	seedAddressSearch(t, db)

	var resp addressSearchResponse
	getJSON(t, api.HandleAddressSearch, "/api/addresses/search?q=g1manfred&network=alpha", &resp)
	if len(resp.Addresses) != 1 {
		t.Fatalf("got %+v, want one match", resp.Addresses)
	}
	m := resp.Addresses[0]
	if m.Name != "moul" {
		t.Errorf("name = %q, want the current registration, not the alias", m.Name)
	}
	if m.Label == "" {
		t.Error("label is empty, want the curated name for a known address")
	}
	if m.Ugnot != 5 {
		t.Errorf("ugnot = %d, want 5", m.Ugnot)
	}
}

func TestHandleAddressSearchRequiresAQuery(t *testing.T) {
	api, _ := newTestAPI(t)
	rec := httptest.NewRecorder()
	api.HandleAddressSearch(rec, httptest.NewRequest(http.MethodGet, "/api/addresses/search", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
