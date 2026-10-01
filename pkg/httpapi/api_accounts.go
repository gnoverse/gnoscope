package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/store"
)

const addressTxPage = 200

// HandleAddress serves an address's activity from storage.
//
// It used to ask the indexer, which cannot answer at chain scale: the query is
// five address predicates over fields it has no index for, so it scans.
// Windowing it from the tip (#121) bought time and the chain outgrew it — the
// busiest account went back to a 500 at 13.9s.
//
// Every message the syncer decodes is already written to a per-type table keyed
// by the address involved, all indexed. Balance still comes from RPC, which is
// the only place it exists.

func (a *API) HandleAddress(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	addr := r.PathValue("addr")

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > addressTxPage {
		limit = addressTxPage
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	txs, total, err := a.db.AddressTransactions(network, addr, limit, offset)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	pkgs, err := a.db.Search(network, addr)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	a.stampInertStatus(r.Context(), pkgs)

	// The oldest block on this page, not the address's first ever: paging back
	// would otherwise make "first seen" wander. Named accordingly.
	oldestOnPage := -1
	oldestOnPageTime := ""
	for _, tx := range txs {
		if oldestOnPage < 0 || tx.BlockHeight < oldestOnPage {
			oldestOnPage = tx.BlockHeight
			oldestOnPageTime = tx.BlockTime
		}
	}

	// Balance is per chain and lives only at the RPC, so it is reported only
	// when one chain is selected. Summing balances across chains would repeat
	// the category error of adding ugnot from different networks.
	balance := ""
	if network != "" {
		balance = fetchBalance(r.Context(), addr, a.rpcURLFor(network))
	}

	JSONResponse(w, map[string]any{
		"address":      addr,
		"transactions": txs,
		// `total` is the message count and always was; it is what the pager
		// pages over and what every older client reads, so it keeps its name
		// and its meaning. `total_txs` is the number the header wants, because
		// the rows above are messages and a multicall is several of them.
		"total":          total.Messages,
		"total_txs":      total.Txs,
		"packages":       pkgs,
		"oldest_on_page": oldestOnPage,
		// Paired with the height rather than derived from row order: the age
		// beside it has to be that block's, not the page's newest.
		"oldest_on_page_time": oldestOnPageTime,
		"balance":             balance,
	})
}

func (a *API) HandleValidators(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	// Served entirely from storage since #83, so no indexer client is needed —
	// and requiring one would have made this 500 in all-networks mode.
	regs, err := a.db.ValoperRegistrations(network)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, regs)
}

// HandleValidatorMonikers serves signing-address -> name for one network, for
// labelling block proposers. r/gnops/valopers is the source, through the
// SigningAddress it stores per profile (see valset.go), so it names validators
// on any chain, including ones registered and not yet in the set. gnockpit
// fills the gaps, on the one chain it describes. Best-effort: an unreachable
// RPC yields {}, not an error.
func (a *API) HandleValidatorMonikers(w http.ResponseWriter, r *http.Request) {
	vs := a.FetchValset(r.Context(), a.singleNetwork(r))
	monikers := map[string]string{}
	for signing, p := range vs.Valopers {
		if p.Moniker != "" {
			monikers[signing] = p.Moniker
		}
	}
	for _, m := range vs.Members {
		if m.Name != "" {
			monikers[m.Address] = m.Name
		}
	}
	JSONResponse(w, monikers)
}

// HandleValidatorsLive serves one network's current validator set, from that
// network's own RPC, joined with the valopers profile and the proposals that
// changed each member (see valset.go). Until 2026-10-01 this served gnockpit's
// mainnet set for every network. Best-effort: an unreachable RPC yields [].
func (a *API) HandleValidatorsLive(w http.ResponseWriter, r *http.Request) {
	vs := a.FetchValset(r.Context(), a.singleNetwork(r))
	members := vs.Members
	if members == nil {
		members = []ValsetMember{}
	}
	JSONResponse(w, members)
}

func (a *API) HandleTokens(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	// Get all packages that look like token contracts (import grc20)
	tokens, err := a.db.GetTokenPackages(network)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, tokens)
}

// Account listing bounds. The default matches the previous fixed top 100, so an
// existing caller passing nothing sees no change.

const maxWatchItems = 100

// watchTimelineLimit bounds the merged recent-activity timeline HandleWatch
// returns alongside the digest. A fixed cap, not a query parameter: the
// timeline is a "what just happened" glance, not a paged history browser.

const watchTimelineLimit = 50

// HandleWatch summarises activity for the realms and addresses a caller watches.
//
// Items arrive as repeated `realm=` and `address=` parameters, each optionally
// carrying the height the caller last saw as `path@height`. That height is what
// turns a list into a digest: it is what "12 new calls since you last looked"
// counts against.
//
// Height rather than a timestamp because it is exact and monotonic per chain,
// where comparing wall-clock time against block time drifts.

func (a *API) HandleWatch(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	q := r.URL.Query()

	parse := func(values []string) []store.WatchRequest {
		out := make([]store.WatchRequest, 0, len(values))
		for _, v := range values {
			if len(out) >= maxWatchItems {
				break
			}
			id, since := v, 0
			if at := strings.LastIndex(v, "@"); at > 0 {
				if n, err := strconv.Atoi(v[at+1:]); err == nil {
					id, since = v[:at], n
				}
			}
			if id == "" {
				continue
			}
			out = append(out, store.WatchRequest{ID: id, Since: since})
		}
		return out
	}

	realmReqs := parse(q["realm"])
	addressReqs := parse(q["address"])

	realms, err := a.db.WatchRealms(network, realmReqs)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	addresses, err := a.db.WatchAddresses(network, addressReqs)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	ids := func(reqs []store.WatchRequest) []string {
		out := make([]string, len(reqs))
		for i, r := range reqs {
			out[i] = r.ID
		}
		return out
	}
	// A timeline alongside the digest: the digest says how much changed,
	// this is the actual activity behind that count. Capped independently of
	// maxWatchItems — that bounds how many realms/addresses can be watched,
	// this bounds how many rows their combined history returns.
	txs, err := a.db.WatchTransactions(network, ids(realmReqs), ids(addressReqs), watchTimelineLimit)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	JSONResponse(w, map[string]any{"realms": realms, "addresses": addresses, "transactions": txs})
}

func (a *API) HandleAccounts(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = defaultAccounts
	}
	if limit > maxAccounts {
		limit = maxAccounts
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	accounts, err := a.db.GetActiveAccounts(network, r.URL.Query().Get("sort"), limit, offset)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	// From the local cache, not from a fan-out of one RPC call per row. See
	// pkg/httpapi/balances.go for why that moved off the read path.
	a.fillAccountBalancesFromCache(network, accounts)
	JSONResponse(w, accounts)
}

func (a *API) HandleBankStats(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	stats, err := a.db.GetBankStats(network)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, stats)
}

// HandleGovDAO lists governance calls, from storage.
//
// This used to ask the indexer, which cannot answer it: the filter is a
// substring match over a field it has no index for, so on a chain with no
// governance activity it widened its window until the deadline and returned a
// 500 — 12 seconds on sapphire. On pearl it returned a row that was not a
// governance call at all, because the predicate matched a message carrying no
// pkg_path.
//
// The syncer already records every MsgCall with its path, indexed by
// (network, pkg_path), so this is a prefix scan that answers instantly and
// cannot match a non-call.

func (a *API) HandleTimeSeriesActiveAddresses(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	days, granularity := a.resolveTimeseriesParams(r, network)
	pts, err := a.db.GetActiveAddressTimeSeries(network, granularity, days)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	if pts == nil {
		pts = []store.ActiveAddressTimePoint{}
	}
	JSONResponse(w, pts)
}

func (a *API) HandleBlockProposers(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	days, _ := a.resolveTimeseriesParams(r, network)
	topN, _ := strconv.Atoi(r.URL.Query().Get("topN"))
	props, err := a.db.GetBlockProposers(network, days, topN)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	if props == nil {
		props = []store.ProposerCount{}
	}
	JSONResponse(w, props)
}

func (a *API) HandleTimeSeriesNewAddresses(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	days, granularity := a.resolveTimeseriesParams(r, network)
	pts, err := a.db.GetNewAddressTimeSeries(network, granularity, days)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	if pts == nil {
		pts = []store.NewAddressPoint{}
	}
	JSONResponse(w, pts)
}

// HandleTimeSeriesActiveRolling ignores ?granularity= on purpose: DAU/WAU/MAU
// are trailing *day* windows, so the series is daily whatever the caller asks.
