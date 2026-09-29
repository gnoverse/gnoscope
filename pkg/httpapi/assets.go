package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// The assets views, over the GRC20 transfer ledger.
//
// This file used to say that money columns were impossible here: GNOT was not
// listed anywhere, there is no oracle on chain, and price, market cap and
// "total value" would each be a number this explorer invented. Half of that
// stopped being true. GNOT trades on Kraken and KuCoin, which gives the chain a
// USD anchor from outside it, and GnoSwap's five pools route tokens to it.
//
// The other half did not stop being true, and prices live in prices.go rather
// than here because of it: four of those five pools hold less than $6,000, so
// the figure is only worth printing beside a measurement of how little it means.
// These endpoints stay what they were, the part that is exact: supply, who
// holds it, and how much of it moves.

// assetRow is one asset, of any of the three kinds, with whatever the registry
// knows about it merged in.
//
// One row type for native ugnot, a GRC20 and a GRC721 on purpose: to a reader
// they are the same kind of thing, and three shapes meant three pages and three
// mental models. What the single shape must not do is imply the numbers are
// comparable, because two of the columns are not. Hence HoldersBasis and
// TransfersBasis, which travel with every row and say where its figure came
// from, and which the frontend turns into a marker and a hover rather than
// leaving to a footnote.
type assetRow struct {
	store.TokenSummary
	// Kind is native / grc20 / grc721.
	Kind string `json:"kind"`

	// HoldersBasis is "replayed" or "swept", and the difference is not a
	// detail. A GRC20's holders are reconstructed from every Transfer event, so
	// the figure is exact inside the ledger window and blind before it. ugnot's
	// are counted from the addresses this indexer has read a balance for,
	// because gno cannot enumerate accounts: that figure is a sample. The two
	// are incomplete at opposite ends, so ranking one against the other
	// produces a wrong answer that looks right.
	HoldersBasis string `json:"holders_basis"`
	// HoldersSwept is how many addresses were read at all, set only when
	// HoldersBasis is "swept". Without it the holder count reads as a total.
	HoldersSwept int `json:"holders_swept,omitempty"`

	// TransfersBasis is "events" or "banksend". A GRC20's count includes every
	// move, realm-internal ones included. ugnot's counts BankMsgSend only, so
	// coin a realm moves inside a call is absent: it surfaces as gas and realm
	// activity instead. The native figure under-counts movement; the GRC20 one
	// does not.
	TransfersBasis string `json:"transfers_basis"`

	// Locked is the share of supply the chain reports as not yet spendable, and
	// only ugnot has one: 83.2% of it when measured 2026-09-29, which is why
	// its market capitalisation is a seventh of its fully diluted value. For a
	// GRC20 the distinction does not exist, so the field is absent rather than
	// zero.
	Locked int64 `json:"locked,omitempty"`
	// SupplyKnown separates "the supply is zero" from "nobody could read it".
	// For a GRC20 the replayed figure always exists; for ugnot the read can
	// fail, and a zero supply on the chain's own coin would be a striking claim
	// to make by accident.
	SupplyKnown bool `json:"supply_known"`
	// Volume is total value moved, native only. A GRC20 has no equivalent here
	// because its summary already carries minted and burned, which is the
	// question its ledger answers well.
	Volume int64 `json:"volume,omitempty"`

	// Verified and DisplaySymbol come from the curated registry. Anyone can
	// deploy a realm called `gns`, so this is the only thing separating the
	// real one from a lookalike, and it is a human's claim rather than
	// anything the chain says.
	Verified      bool   `json:"verified"`
	DisplaySymbol string `json:"display_symbol,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
	Decimals      int    `json:"decimals,omitempty"`
}

type assetsResponse struct {
	Network string     `json:"network"`
	Assets  []assetRow `json:"assets"`
}

func (a *API) decorate(t store.TokenSummary) assetRow {
	row := assetRow{
		TokenSummary: t,
		// Kind is read from the data, not from the library path, for the same
		// reason TokenSummary.Fungible is: a GRC721 reaches this ledger through
		// the GRC20 Transfer event, so the import says grc20 for both. What
		// separates them is that a non-fungible transfer carries no amount.
		Kind:           store.KindGRC20,
		HoldersBasis:   "replayed",
		TransfersBasis: "events",
		SupplyKnown:    true,
	}
	if !t.Fungible {
		row.Kind = store.KindGRC721
	}
	if meta, ok := a.registry.Tokens[t.Token]; ok {
		row.Verified = meta.Verified
		row.DisplaySymbol = meta.Symbol
		row.DisplayName = meta.Name
		row.Decimals = meta.Decimals
	}
	return row
}

// nativeRow projects the chain's own coin into the same row every token uses.
//
// Two fields are deliberately left empty rather than filled with a plausible
// zero. Minted and Burned do not apply: ugnot has no mint event, and a 0 in a
// column headed "minted" reads as "none was ever issued". Holders carries the
// swept basis instead, which is the honest name for what it counts.
func (a *API) nativeRow(ctx context.Context, network string) (assetRow, bool) {
	s, err := a.db.NativeSummary(network)
	if err != nil {
		return assetRow{}, false
	}
	row := assetRow{
		TokenSummary: store.TokenSummary{
			Token:         store.NativeKey,
			Symbol:        "GNOT",
			Network:       network,
			Fungible:      true,
			Holders:       s.Holders,
			Transfers:     s.Transfers,
			Transfers24h:  s.Transfers24h,
			FirstSeenTime: s.FirstSeenTime,
			LastSeenTime:  s.LastSeenTime,
			FirstBlock:    s.FirstBlock,
			LastBlock:     s.LastBlock,
		},
		Kind:           store.KindNative,
		HoldersBasis:   "swept",
		HoldersSwept:   s.HoldersSwept,
		TransfersBasis: "banksend",
		Volume:         s.Volume,
		// ugnot's 6 decimals are a chain constant, not a token's claim about
		// itself, so this is the one asset whose per-token figures need no
		// caveat about scaling.
		Decimals:      6,
		DisplaySymbol: "GNOT",
		DisplayName:   "gno.land",
		// Nobody has to vouch for the chain's own coin being the chain's own
		// coin. The registry's whole purpose is separating a real token from a
		// lookalike, and there is no lookalike ugnot.
		Verified: true,
	}
	// Supply comes from the chain, not from this ledger: bank_sends says how
	// much moved, never how much exists. A failed read leaves SupplyKnown
	// false, because a zero supply on the chain's own coin would be a striking
	// claim to publish by accident.
	if client := a.clientFor(network); client != nil {
		if supply, err := client.GetSupply(ctx, "ugnot"); err == nil {
			if total, err := strconv.ParseInt(supply.Total, 10, 64); err == nil {
				row.Supply, row.SupplyKnown = total, true
			}
			if locked, err := strconv.ParseInt(supply.Locked, 10, 64); err == nil {
				row.Locked = locked
			}
		}
	}
	return row, true
}

// HandleAssets lists every asset, optionally narrowed to one realm or one kind.
//
// The native coin is one of the rows. It is not a GRC20 and never will be, but
// to a reader it is the same kind of thing as GNS, and keeping it on a separate
// page meant comparing them took two mental models. What the row does carry is
// `kind`, `holders_basis` and `transfers_basis`, so the two columns that are
// not comparable across kinds say so themselves.
//
// `?realm=<package path>` is what the realm page asks for, and the reason it
// has to ask at all: a realm is a *container* of tokens, not a token. Six of
// the twelve assets on mainnet (measured 2026-09-22) are issued by one realm,
// `.../gnomi/padv3`, and grc20factory exists to have many. So the realm page
// cannot show "the token"; it shows the list, and each row routes to its own
// page keyed on the full event key. A realm filter excludes the native row,
// which belongs to no realm.
//
// `?kind=native|grc20|grc721` is what the three section pages ask for.
// `?include_native=0` drops it for a caller that wants the old list back.
func (a *API) HandleAssets(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	summaries, err := a.db.TokenSummaries(network)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	realm := strings.TrimSpace(r.URL.Query().Get("realm"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	rows := make([]assetRow, 0, len(summaries)+1)

	// The native row goes first, and only in single-network mode: ugnot on two
	// chains is two different assets that share a denom, and one blended row
	// would describe neither. Same reasoning the bank totals already use.
	wantNative := realm == "" && r.URL.Query().Get("include_native") != "0" &&
		(kind == "" || kind == store.KindNative) && a.singleNetwork(r) != ""
	if wantNative {
		if row, ok := a.nativeRow(r.Context(), network); ok {
			rows = append(rows, row)
		}
	}
	for _, t := range summaries {
		if realm != "" && t.PkgPath != realm {
			continue
		}
		row := a.decorate(t)
		if kind != "" && row.Kind != kind {
			continue
		}
		rows = append(rows, row)
	}
	JSONResponse(w, assetsResponse{Network: network, Assets: rows})
}

// HandleAssetActivity is the defi home's chart: daily movement across every
// asset, split by kind.
//
// Split rather than summed. The native count is BankMsgSend only and the GRC20
// count includes realm-internal moves, so one "transfers today" line would add
// two differently-defined numbers and present the total as a fact. Three series
// let a reader see which is which, which is the same reason the list marks the
// two columns rather than blending them.
func (a *API) HandleAssetActivity(w http.ResponseWriter, r *http.Request) {
	network := a.singleNetwork(r)
	if network == "" {
		jsonError(w, "no network configured", 404)
		return
	}
	days := 90
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 365 {
		days = v
	}
	points, err := a.db.AssetActivityOverTime(network, days)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, map[string]any{"network": network, "days": days, "points": points})
}

// HandleAssetSearch matches assets by their event key, for the search box.
//
// Separate from /api/search rather than folded into it: that endpoint answers
// with a flat array of packages and every caller reads it as one, and a token
// is not a package. A realm holding six tokens would also collapse to a single
// package row, which is exactly the confusion this whole surface exists to
// undo.
func (a *API) HandleAssetSearch(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		jsonError(w, "missing q parameter", 400)
		return
	}
	found, err := a.db.SearchTokens(network, q, 8)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	rows := make([]assetRow, len(found))
	for i, t := range found {
		rows[i] = a.decorate(t)
	}
	JSONResponse(w, assetsResponse{Network: network, Assets: rows})
}

type assetDetailResponse struct {
	Network   string                 `json:"network"`
	Asset     assetRow               `json:"asset"`
	Holders   []store.TokenHolder    `json:"holders"`
	Transfers []store.TokenTransfer  `json:"transfers"`
	Supply    []store.BlockTimePoint `json:"supply_series"`
	// Flow is daily transfer count and value moved, which is the series ugnot
	// can honestly draw and the one a GRC20 was missing. Mints and burns are
	// counted as transfers and excluded from the value, so an issuance does not
	// read as trading: the supply series above is where minting belongs.
	Flow []store.TokenFlowPoint `json:"flow_series"`
	// Siblings are the other assets issued by the same realm. Empty for the
	// common one-token realm; six rows for `.../gnomi/padv3`. Carried on the
	// detail response rather than fetched separately so the page can say
	// "this realm issues N of these" without a second round trip, which is
	// the fact a reader arriving from the realm most needs.
	Siblings []assetRow `json:"siblings"`
	// Ledger is the span of transfer history this network actually has, so
	// the page can state the window it computed over. See TokenLedgerWindow.
	Ledger store.TokenLedgerWindow `json:"ledger"`
	// Realm is the issuing package, when the event key carried a path and the
	// package is known to this index. Nil for a token that emits a bare
	// symbol, and for one whose realm was deployed outside the synced range.
	Realm *assetRealm `json:"realm,omitempty"`
}

// assetRealm is the issuing package, reduced to what the token page shows.
type assetRealm struct {
	Path        string `json:"path"`
	Creator     string `json:"creator"`
	BlockHeight int    `json:"block_height"`
	BlockTime   string `json:"block_time,omitempty"`
	NumFiles    int    `json:"num_files"`
	Calls       int    `json:"call_count"`
}

// HandleAsset serves one token.
//
// Single network, because a balance reconstruction is per chain: the same token
// path can exist on two chains and its holders there are different people.
func (a *API) HandleAsset(w http.ResponseWriter, r *http.Request) {
	network := a.singleNetwork(r)
	if network == "" {
		jsonError(w, "no network configured", 404)
		return
	}
	token, err := url.PathUnescape(r.PathValue("token"))
	if err != nil || strings.TrimSpace(token) == "" {
		jsonError(w, "no token given", 400)
		return
	}

	// The chain's own coin answers here too, on the same response shape, so one
	// page draws either. Its ledger is a different table and two of its figures
	// mean something different, which the row says for itself.
	if token == store.NativeKey {
		a.handleNativeAsset(w, r, network)
		return
	}

	summaries, err := a.db.TokenSummaries(network)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	var found *store.TokenSummary
	for i := range summaries {
		if summaries[i].Token == token {
			found = &summaries[i]
			break
		}
	}
	if found == nil {
		jsonError(w, "token not found on this network", 404)
		return
	}

	holders, err := a.db.TopHolders(network, token, 50)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	transfers, err := a.db.TokenTransfers(network, token, 50)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	series, err := a.db.TokenSupplyOverTime(network, token, 90)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	flow, err := a.db.TokenFlowOverTime(network, token, 90)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	ledger, err := a.db.TokenLedgerWindow(network)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	siblings := []assetRow{}
	if found.PkgPath != "" {
		for i := range summaries {
			if summaries[i].PkgPath == found.PkgPath && summaries[i].Token != token {
				siblings = append(siblings, a.decorate(summaries[i]))
			}
		}
	}

	// Best-effort: a token whose key is a bare symbol has no realm to look up,
	// and a realm deployed before this index's range is not in `packages`.
	// Neither is an error on a page about the token.
	var realm *assetRealm
	if found.PkgPath != "" {
		if detail, err := a.db.GetPackageDetail(network, found.PkgPath); err == nil && detail != nil {
			realm = &assetRealm{
				Path:        detail.Path,
				Creator:     detail.Creator,
				BlockHeight: detail.BlockHeight,
				BlockTime:   detail.BlockTime,
				NumFiles:    detail.NumFiles,
				Calls:       detail.Calls,
			}
		}
	}

	JSONResponse(w, assetDetailResponse{
		Network:   network,
		Asset:     a.decorate(*found),
		Holders:   holders,
		Transfers: transfers,
		Supply:    series,
		Flow:      flow,
		Siblings:  siblings,
		Ledger:    ledger,
		Realm:     realm,
	})
}

// handleNativeAsset serves ugnot on the token detail shape.
//
// Three fields come back deliberately different from a GRC20's, and each one is
// a fact about the chain rather than a gap in this indexer:
//
//   - **No supply series.** A GRC20's is the running sum of its mints, which is
//     what its Transfer ledger records. ugnot's supply is set by the chain and
//     `bank_sends` has never seen a mint, so a cumulative line over this table
//     would draw volume and label it supply. The flow series is what this
//     ledger can honestly draw.
//   - **No siblings and no realm.** ugnot belongs to no package.
//   - **Holders are swept, not replayed.** The row says so; see assetRow.
func (a *API) handleNativeAsset(w http.ResponseWriter, r *http.Request, network string) {
	row, ok := a.nativeRow(r.Context(), network)
	if !ok {
		jsonError(w, "native coin unavailable on this network", 404)
		return
	}
	holders, err := a.db.NativeTopHolders(network, 50)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	transfers, err := a.db.NativeTransfers(network, 50)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	flow, err := a.db.NativeFlowOverTime(network, 90)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, assetDetailResponse{
		Network:   network,
		Asset:     row,
		Holders:   holders,
		Transfers: transfers,
		Flow:      flow,
		Siblings:  []assetRow{},
	})
}
