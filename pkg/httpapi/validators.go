package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// One validator, over time.
//
// /validators already renders the set: it derives proposers from recent blocks,
// draws a liveness sparkline per row, and joins gnockpit's power, missed-block
// and spof columns onto them. That table is a snapshot, and there was no way to
// ask about a single validator across the chain's history.
//
// Keyed on the **consensus** (signing) address, which is what proposes blocks.
// An **operator** address, the key a profile registers under in
// r/gnops/valopers, is accepted too and resolved through the realm's own
// signing-address field (see valset.go): the two spaces join since the realm
// started storing SigningAddress, which the comments here used to deny.

// ValidatorRow is one validator, merged from the live set and local history.
type ValidatorRow struct {
	Address string `json:"address"`
	// Name is operator-supplied: the valopers moniker, else gnockpit's. A
	// claim, not a fact.
	Name        string `json:"name,omitempty"`
	Moniker     string `json:"moniker,omitempty"`
	Operator    string `json:"operator,omitempty"`
	VotingPower int64  `json:"voting_power"`
	SPOF        bool   `json:"spof"`
	// Nil where gnockpit does not describe this chain, which is every chain
	// but mainnet: absent is not zero.
	Missed100  *int `json:"missed_100,omitempty"`
	Missed24h  *int `json:"missed_24h,omitempty"`
	AvgBlockMs *int `json:"avg_block_ms,omitempty"`
	// Blocks, Txs and Share are what this instance has synced, so they cover
	// the synced range rather than all of history.
	Blocks    int     `json:"blocks"`
	Txs       int     `json:"txs"`
	LastBlock int     `json:"last_height,omitempty"`
	LastTime  string  `json:"last_block_time,omitempty"`
	Share     float64 `json:"share"`
}

type validatorDetailResponse struct {
	Network   string       `json:"network"`
	Validator ValidatorRow `json:"validator"`
	InSet     bool         `json:"in_set"`
	// TotalPower is the whole set's, so a page can say "4 of 215".
	TotalPower int64 `json:"total_power,omitempty"`
	// Proposals changed this validator's membership, newest first.
	Proposals []ValsetProposalRef         `json:"proposals,omitempty"`
	Blocks    []store.ProposedBlock       `json:"blocks"`
	Shares    []store.ValidatorSharePoint `json:"shares"`
}

// HandleValidator serves one validator by consensus or operator address.
func (a *API) HandleValidator(w http.ResponseWriter, r *http.Request) {
	network := a.singleNetwork(r)
	if network == "" {
		jsonError(w, "no network configured", 404)
		return
	}
	addr := r.PathValue("addr")
	if addr == "" {
		jsonError(w, "no address given", 400)
		return
	}

	activity, err := a.db.ValidatorActivityAll(network)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	vs := a.FetchValset(r.Context(), network)
	// An operator address resolves to the key it signs with. Redirecting would
	// be tidier and would cost the page a round trip; the response says which
	// address it answered for, so the page can show both.
	var prof Valoper
	if p, ok := vs.ByOperator(addr); ok {
		prof, addr = p, p.Signing
	} else if p, ok := vs.Valopers[addr]; ok {
		prof = p
	}
	act, known := activity[addr]

	row := ValidatorRow{Address: addr, Operator: prof.Operator, Moniker: prof.Moniker, Name: prof.Moniker}
	m, inSet := vs.Member(addr)
	if inSet {
		if m.Name != "" {
			row.Name = m.Name
		}
		row.SPOF = m.SPOF
		row.Missed100, row.Missed24h, row.AvgBlockMs = m.Missed100, m.Missed24h, m.AvgBlockMs
		row.VotingPower, _ = strconv.ParseInt(m.VotingPower, 10, 64)
	}
	if !known && !inSet && prof.Operator == "" {
		// Neither in the set, nor ever seen proposing, nor registered. An empty
		// page would read as an idle validator rather than as an address that
		// is not one.
		jsonError(w, "no validator with that address on this network", 404)
		return
	}

	total := 0
	for _, a := range activity {
		total += a.Blocks
	}
	row.Blocks, row.Txs = act.Blocks, act.Txs
	row.LastBlock, row.LastTime = act.LastH, act.Last
	if total > 0 {
		row.Share = float64(act.Blocks) / float64(total)
	}

	blocks, err := a.db.ValidatorBlocks(network, addr, 50)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	shares, err := a.db.ValidatorShareSeries(network, 30)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	JSONResponse(w, validatorDetailResponse{
		Network: network, Validator: row, InSet: inSet, Blocks: blocks, Shares: shares,
		TotalPower: vs.TotalPower, Proposals: vs.ProposalsFor(addr, prof.Operator),
	})
}
