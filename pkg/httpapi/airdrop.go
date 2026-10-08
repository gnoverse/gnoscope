package httpapi

import (
	"net/http"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/genesis"
)

// genesisNetwork is the one network whose genesis the sheet describes: the
// sheet is gnoland-1's, and a testnet's address that happens to appear in it
// would be a coincidence of key bytes, not an allocation.
const genesisNetwork = "mainnet"

// Genesis statuses. The distinction between the last two is the whole point:
// "not in the sheet" is a finding, "the sheet is not loaded" is an absence of
// one, and a page that merges them tells people they got nothing.
const (
	genesisFound     = "found"
	genesisAbsent    = "not_in_genesis"
	genesisNotLoaded = "not_loaded"
	genesisInvalid   = "invalid"
)

type genesisVesting struct {
	Ugnot int64 `json:"ugnot"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	// Delayed means nothing vests before End; otherwise it vests continuously
	// between Start and End.
	Delayed bool `json:"delayed"`
}

// genesisAnswer is what the sheet says about one address.
type genesisAnswer struct {
	Status  string `json:"status"`
	Address string `json:"address,omitempty"`
	// Ugnot is the whole allocation, vesting part included.
	Ugnot   int64           `json:"ugnot,omitempty"`
	Vesting *genesisVesting `json:"vesting,omitempty"`
	Note    string          `json:"note,omitempty"`
}

// genesisLookup answers for a g1 address. The caller has already converted.
func (a *API) genesisLookup(g1 string) genesisAnswer {
	out := genesisAnswer{Address: g1}
	sha, _, ok, err := a.db.GenesisImported()
	if err != nil || !ok || sha != genesis.SourceSHA256 {
		out.Status = genesisNotLoaded
		out.Note = "the genesis sheet is not loaded on this instance yet, so this says nothing either way"
		return out
	}
	_, data, err := genesis.Decode(g1)
	if err != nil {
		out.Status, out.Note = genesisInvalid, "not a valid address"
		return out
	}
	row, found, err := a.db.GenesisLookup(data)
	if err != nil {
		out.Status, out.Note = genesisNotLoaded, "the genesis sheet could not be read"
		return out
	}
	if !found {
		out.Status = genesisAbsent
		return out
	}
	out.Status, out.Ugnot = genesisFound, row.Ugnot
	if row.HasVesting {
		out.Vesting = &genesisVesting{Ugnot: row.VestUgnot, Start: row.VestStart, End: row.VestEnd, Delayed: row.VestDelayed}
	}
	return out
}

type airdropResponse struct {
	Input string `json:"input"`
	// Converted is true when the input was another chain's address, re-spelled.
	Converted bool `json:"converted"`
	genesisAnswer
	Source airdropSource `json:"source"`
}

type airdropSource struct {
	URL    string `json:"url"`
	Commit string `json:"commit"`
	SHA256 string `json:"sha256"`
	Rows   int    `json:"rows"`
}

// HandleAirdrop serves GET /api/airdrop?address=. It accepts a cosmos1…, atone1…
// or g1… address: the two airdrops kept each recipient's key bytes under a
// different prefix, so the g1 address is the same key re-spelled.
func (a *API) HandleAirdrop(w http.ResponseWriter, r *http.Request) {
	input := strings.TrimSpace(r.URL.Query().Get("address"))
	if input == "" {
		jsonError(w, "missing address", 400)
		return
	}
	out := airdropResponse{Input: input, Source: airdropSource{
		URL: genesis.SourceURL, Commit: genesis.SourceCommit, SHA256: genesis.SourceSHA256, Rows: genesis.SourceRows,
	}}
	g1, err := genesis.ToGno(input)
	if err != nil {
		out.genesisAnswer = genesisAnswer{Status: genesisInvalid, Note: "that is not a bech32 account address (cosmos1…, atone1… or g1…)"}
		JSONResponse(w, out)
		return
	}
	out.Converted = g1 != strings.ToLower(input)
	out.genesisAnswer = a.genesisLookup(g1)
	JSONResponse(w, out)
}
