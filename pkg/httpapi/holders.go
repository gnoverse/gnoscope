package httpapi

import (
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/price"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// /api/holders: who holds what, across every asset at once.
//
// The per-asset question was already answerable (`/api/asset/<key>` carries its
// own holder list). This is the other axis, and it is the one a reader arrives
// with: who are the holders on this chain, and what is a portfolio worth.
//
// Ranking is by **estimated value**, which is the only ordering that means
// anything across assets: ranking by raw base units compares a token's
// 1e6-scaled balance against another's and puts whichever has more decimals on
// top, and ranking by position count makes an address holding six worthless
// tokens beat one holding the chain.
//
// That makes this endpoint entirely dependent on prices, which on gno.land are
// mostly absent and, where present, mostly decorative (see pkg/price). So every
// row says how much of its value is actually priced, and the response says how
// many assets have no price at all. An unpriced position is never valued at
// zero: zero is a claim, and the honest statement is "this part is not priced".

// holdersMaxPositions bounds the replay. Mainnet produces roughly a thousand
// positions; a chain that produces more gets a truncation flag rather than a
// silently ranked prefix.
const holdersMaxPositions = 50000

// holdersDefaultLimit is how many addresses come back by default. Ten is what
// the section shows; a reader who wants the tail asks for it.
const (
	holdersDefaultLimit = 10
	holdersMaxLimit     = 500
)

type holderPosition struct {
	Token   string `json:"token"`
	Symbol  string `json:"symbol"`
	Kind    string `json:"kind"`
	Balance int64  `json:"balance"`
	// Amount is Balance scaled by decimals, when they are known well enough to
	// scale by. Absent otherwise, rather than guessed.
	Amount   float64 `json:"amount,omitempty"`
	USDValue float64 `json:"usd_value,omitempty"`
	// Priced separates "worth nothing" from "not priced". Twenty-three of
	// mainnet's twenty-eight assets have no market at all.
	Priced   bool `json:"priced"`
	Fungible bool `json:"fungible"`
}

type holderRow struct {
	Address string `json:"address"`
	// Assets is how many distinct assets this address holds, priced or not.
	Assets int `json:"assets"`
	// USDValue is the sum over the priced positions only, and PricedAssets says
	// how many of Assets that was. A row with 6 assets and 1 priced is a row
	// whose total describes a sixth of what it holds, and the pair is what lets
	// a reader see that rather than infer it.
	USDValue     float64 `json:"usd_value"`
	PricedAssets int     `json:"priced_assets"`
	// Largest is the biggest position by value where anything is priced, and by
	// balance otherwise, so a row with no priced holdings still says what it is
	// mostly made of.
	Largest   *holderPosition  `json:"largest,omitempty"`
	Positions []holderPosition `json:"positions,omitempty"`
	// HoldsNative says the row includes a swept ugnot balance, which is the one
	// position in it that comes from a sample rather than a replay.
	HoldsNative bool `json:"holds_native"`
}

type holdersResponse struct {
	Network string      `json:"network"`
	Holders []holderRow `json:"holders"`
	// Totals a reader needs before trusting the ranking.
	TotalHolders int `json:"total_holders"`
	PricedAssets int `json:"priced_assets"`
	TotalAssets  int `json:"total_assets"`
	// Truncated says the position replay hit its guard, so the ranking covers a
	// prefix of the chain rather than all of it.
	Truncated bool `json:"truncated"`
	// NativeIncluded says whether swept ugnot balances are in the ranking, and
	// NativeSwept how many addresses that sample covers. A reader comparing two
	// rows needs to know one of the numbers is a sample.
	NativeIncluded bool `json:"native_included"`
	NativeSwept    int  `json:"native_swept"`
	// PricesUnavailable carries the reason there are no values, when there are
	// none. Ranking falls back to position count, and the response says so
	// rather than presenting an arbitrary order as a leaderboard.
	PricesUnavailable string `json:"prices_unavailable,omitempty"`
}

// HandleHolders ranks addresses by what they hold.
//
// `?limit=` (default 10, max 500), `?token=` to narrow to one asset,
// `?native=0` to exclude the swept native balances, `?positions=1` to include
// each row's full breakdown rather than just its largest.
func (a *API) HandleHolders(w http.ResponseWriter, r *http.Request) {
	network := a.singleNetwork(r)
	if network == "" {
		jsonError(w, "no network configured", 404)
		return
	}
	q := r.URL.Query()
	limit := holdersDefaultLimit
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, holdersMaxLimit)
	}
	tokenFilter := strings.TrimSpace(q.Get("token"))
	wantNative := q.Get("native") != "0"
	wantPositions := q.Get("positions") == "1"

	positions, truncated, err := a.db.AllPositions(network, holdersMaxPositions)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	resp := holdersResponse{Network: network, Truncated: truncated}

	if wantNative && (tokenFilter == "" || tokenFilter == store.NativeKey) {
		// The sweep is read at the same depth as the replay rather than at the
		// display limit: an address rich in ugnot and poor in everything else
		// must be able to out-rank one that is the other way round, and cutting
		// the native side early would decide that by accident.
		native, err := a.db.NativePositions(network, holdersMaxPositions)
		if err == nil && len(native) > 0 {
			positions = append(positions, native...)
			resp.NativeIncluded = true
			resp.NativeSwept = len(native)
		}
	}

	// Prices are best-effort and usually mostly absent. A failure here does not
	// fail the endpoint: the ranking falls back to position count and says so.
	quotes := map[string]*price.Quote{}
	if res, err := a.pricesFor(r.Context(), network); err == nil && res != nil {
		if res.Unavailable != "" {
			resp.PricesUnavailable = res.Unavailable
		}
		for i := range res.Quotes {
			q := res.Quotes[i]
			if q.USDPerToken > 0 {
				quotes[q.Token] = &q
				resp.PricedAssets++
			}
			resp.TotalAssets++
		}
	} else if err != nil {
		resp.PricesUnavailable = err.Error()
	}

	byAddr := map[string]*holderRow{}
	for _, p := range positions {
		if tokenFilter != "" && p.Token != tokenFilter {
			continue
		}
		row, ok := byAddr[p.Address]
		if !ok {
			row = &holderRow{Address: p.Address}
			byAddr[p.Address] = row
		}
		hp := holderPosition{
			Token: p.Token, Symbol: p.Symbol, Balance: p.Balance, Fungible: p.Fungible,
			Kind: store.KindGRC20,
		}
		switch {
		case p.Token == store.NativeKey:
			hp.Kind = store.KindNative
			row.HoldsNative = true
		case !p.Fungible:
			hp.Kind = store.KindGRC721
		}
		if q, ok := quotes[p.Token]; ok && p.Fungible {
			hp.Priced = true
			hp.USDValue = usdValue(p.Balance, q)
			hp.Amount = scaled(p.Balance, q.Decimals)
			row.USDValue += hp.USDValue
			row.PricedAssets++
		}
		row.Assets++
		if row.Largest == nil || better(&hp, row.Largest) {
			cp := hp
			row.Largest = &cp
		}
		if wantPositions {
			row.Positions = append(row.Positions, hp)
		}
	}

	out := make([]holderRow, 0, len(byAddr))
	for _, row := range byAddr {
		if wantPositions {
			sort.SliceStable(row.Positions, func(i, j int) bool { return better(&row.Positions[i], &row.Positions[j]) })
		}
		out = append(out, *row)
	}
	// Value first, then priced-asset count, then asset count, then address for
	// a stable order. The tie-breaks matter more here than usual: with 23 of 28
	// assets unpriced, most rows have a value of exactly zero and would
	// otherwise come back in map order, which changes between requests and
	// reads as data churning.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].USDValue != out[j].USDValue {
			return out[i].USDValue > out[j].USDValue
		}
		if out[i].PricedAssets != out[j].PricedAssets {
			return out[i].PricedAssets > out[j].PricedAssets
		}
		if out[i].Assets != out[j].Assets {
			return out[i].Assets > out[j].Assets
		}
		return out[i].Address < out[j].Address
	})
	resp.TotalHolders = len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	resp.Holders = out
	JSONResponse(w, resp)
}

// usdValue converts a base-unit balance using the exact per-base-unit price
// rather than the per-token one.
//
// The per-token figure divides by a decimals value that, for most tokens on this
// chain, nobody has confirmed. The per-base-unit one is a pool ratio and needs
// no decimals at all, so multiplying by it introduces no assumption the price
// did not already carry.
func usdValue(balance int64, q *price.Quote) float64 {
	if q == nil || balance <= 0 {
		return 0
	}
	if r, ok := new(big.Rat).SetString(q.USDPerBaseUnit); ok && r.Sign() > 0 {
		v, _ := new(big.Rat).Mul(r, new(big.Rat).SetInt64(balance)).Float64()
		return v
	}
	// Fall back through the per-token price, which is what the quote has when
	// the exact string did not parse. Still better than dropping the row.
	if q.Decimals > 0 {
		return q.USDPerToken * scaled(balance, q.Decimals)
	}
	return 0
}

func scaled(balance int64, decimals int) float64 {
	if decimals <= 0 {
		return float64(balance)
	}
	d := 1.0
	for i := 0; i < decimals; i++ {
		d *= 10
	}
	return float64(balance) / d
}

// better orders two positions: by value where either is priced, by raw balance
// otherwise. Never by balance across a priced and an unpriced position, which
// would let an unpriced 1e18-scaled token outrank a real one.
func better(a, b *holderPosition) bool {
	if a.Priced != b.Priced {
		return a.Priced
	}
	if a.Priced {
		return a.USDValue > b.USDValue
	}
	return a.Balance > b.Balance
}
