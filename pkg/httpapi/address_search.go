package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// Address autocomplete, for the search box's addresses group.
//
// A fifth endpoint beside /api/search, users, assets and symbols, for the
// reason each of those is separate: /api/search answers with an array of
// packages and every caller reads it as one, and an address is not a package.
// The query side, and why it reads `balances`, is in store/address_search.go.

type addressSearchResponse struct {
	Network   string             `json:"network"`
	Addresses []addressSearchRow `json:"addresses"`
}

type addressSearchRow struct {
	store.AddressMatch
	// Label is the curated name for the address, when the registry file has
	// one, so a prefix of a known account says whose it is before it is opened.
	Label string `json:"label,omitempty"`
}

// addressSearchLimit matches userSearchLimit, for the same reason: the popup
// shows several groups at once and one that fills the viewport hides the rest.
const addressSearchLimit = 6

// HandleAddressSearch answers GET /api/addresses/search?q=g1…
func (a *API) HandleAddressSearch(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		jsonError(w, "missing q parameter", 400)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = addressSearchLimit
	}
	found, err := a.db.SearchAddressPrefix(network, q, limit)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	rows := make([]addressSearchRow, len(found))
	for i, m := range found {
		row := addressSearchRow{AddressMatch: m}
		if e, ok := a.registry.Addresses[m.Address]; ok {
			row.Label = e.Label
		}
		rows[i] = row
	}
	JSONResponse(w, addressSearchResponse{Network: network, Addresses: rows})
}
