package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gnoverse/gnoscope/pkg/price"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// /api/prices: what a GRC20 is worth, and how much of that to believe.
//
// This is the first endpoint here that puts a dollar figure on anything, and
// the reason it took until now is stated in assets.go: for most of this
// explorer's life GNOT had no market, so every money column would have been an
// invention. That changed. GNOT trades on Kraken and KuCoin, which gives the
// chain a USD anchor it does not have itself, and GnoSwap gives five pools to
// route through. Five, for the whole chain.
//
// So the endpoint's real job is not arithmetic. It is saying, per token, which
// of these numbers is a price and which is a decoration, and pkg/price does
// that from measured slippage rather than from an opinion. Everything here is
// plumbing: read the ledger, read the chain, hand it to that package.

// Two caches with very different clocks, because the two reads cost very
// different amounts.
//
// Pool discovery is O(tokens^2 * fee tiers) ExistsPoolPath probes and the answer
// changes when somebody creates a pool, which on mainnet has happened five
// times ever. Quotes are ~9 reads per pool plus 4 per priced token and change
// every block.
const (
	poolDiscoveryTTL = 30 * time.Minute
	priceQuoteTTL    = 90 * time.Second
	// priceReadTimeout bounds one whole refresh. Generous because a cold
	// refresh is dozens of sequential-ish RPC reads, and because the failure
	// mode of being too tight is serving no prices at all rather than serving
	// slow ones.
	priceReadTimeout = 90 * time.Second
	// anchorReadTimeout is the USD leg's own budget, carved out of the total
	// rather than shared with it. Two public tickers answered in 80 ms and 300
	// ms when measured from val1, so this is generous by two orders of
	// magnitude and still cannot be eaten by a slow chain.
	anchorReadTimeout = 12 * time.Second
	// poolDiscoveryTimeout bounds the probe walk so it cannot spend the whole
	// refresh. A walk that runs out keeps whatever it found and the next
	// refresh continues from there, which prices fewer tokens rather than none.
	poolDiscoveryTimeout = 40 * time.Second
	// priceConcurrency is the fan-out for the discovery probe. The same shape
	// and the same reasoning as inert.go's 60: these are lightweight
	// local-state abci_query reads, not writes, and the walk is several hundred
	// of them. At 32 the pass took longer than its own budget on val1
	// (2026-09-29), which is how the anchor came to be starved.
	priceConcurrency = 64
)

type assetPriceCache struct {
	mu sync.Mutex
	// pools is the discovered pool set per network, and poolsAt dates it.
	pools   map[string][]price.PoolKey
	poolsAt map[string]time.Time
	// result is the last full answer per network. Kept even when stale so a
	// failing RPC degrades to an old price with a visible timestamp rather than
	// to an empty page.
	result   map[string]*price.Result
	resultAt map[string]time.Time
	// inflight collapses concurrent refreshes of the same network into one, so
	// a cache expiry under load does not fan out hundreds of RPC reads.
	inflight map[string]*sync.Mutex
}

var assetPrices = &assetPriceCache{
	pools:    map[string][]price.PoolKey{},
	poolsAt:  map[string]time.Time{},
	result:   map[string]*price.Result{},
	resultAt: map[string]time.Time{},
	inflight: map[string]*sync.Mutex{},
}

// HandlePrices answers the priced view of every indexed asset.
//
// Always 200, even with nothing to say, and that is a deliberate departure from
// the 503 this was first written with. Two reasons. A reader needs to tell
// "this chain has no market for that token" from "this server could not find
// out", and only a body can carry that difference. And a 503 on a supplementary
// column puts a red line in the browser console of every page load on a network
// with no verified RPC, which is most of them. `unavailable` names the reason
// instead, the same shape /api/forge already uses.
//
// A stale result is served in preference to an empty one: the response carries
// computed_at, and a reader who can see the timestamp can judge an old price
// for themselves.
func (a *API) HandlePrices(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	res, err := a.pricesFor(r.Context(), network)
	if res == nil {
		res = &price.Result{Unavailable: "no price data"}
		if err != nil {
			res.Unavailable = err.Error()
		}
	}
	res.Network = network
	JSONResponse(w, res)
}

func (a *API) pricesFor(ctx context.Context, network string) (*price.Result, error) {
	assetPrices.mu.Lock()
	if res, ok := assetPrices.result[network]; ok && time.Since(assetPrices.resultAt[network]) < priceQuoteTTL {
		assetPrices.mu.Unlock()
		return res, nil
	}
	lock, ok := assetPrices.inflight[network]
	if !ok {
		lock = &sync.Mutex{}
		assetPrices.inflight[network] = lock
	}
	assetPrices.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()

	// Re-check: whoever held the lock may have just refreshed it.
	assetPrices.mu.Lock()
	if res, ok := assetPrices.result[network]; ok && time.Since(assetPrices.resultAt[network]) < priceQuoteTTL {
		assetPrices.mu.Unlock()
		return res, nil
	}
	assetPrices.mu.Unlock()

	res, err := a.refreshPrices(ctx, network)
	if res == nil {
		assetPrices.mu.Lock()
		stale := assetPrices.result[network]
		assetPrices.mu.Unlock()
		return stale, err
	}
	assetPrices.mu.Lock()
	assetPrices.result[network] = res
	assetPrices.resultAt[network] = time.Now()
	assetPrices.mu.Unlock()
	return res, err
}

func (a *API) refreshPrices(ctx context.Context, network string) (*price.Result, error) {
	rpcURL := a.rpcURLFor(network)
	if rpcURL == "" {
		return nil, fmt.Errorf("no verified RPC endpoint for network %s", network)
	}
	ctx, cancel := context.WithTimeout(ctx, priceReadTimeout)
	defer cancel()

	eval := func(ctx context.Context, expr string) (string, error) {
		return fetchABCIQuery(ctx, rpcURL, "vm/qeval", expr)
	}

	// The anchor comes FIRST, on its own clock, and this ordering is the whole
	// fix for a bug that shipped: with the chain scan in front of it, a slow
	// discovery pass spent the entire 60-second budget and the anchor fetch
	// then failed on an expired context, reporting "no GNOT/USD venue
	// answered" from a host that could reach both venues in 80 milliseconds
	// (val1, 2026-09-29). Two cheap HTTP calls must not be starved by dozens of
	// RPC reads, and if they fail there is nothing to compute anyway, so
	// failing here is also the fastest way to fail.
	anchorCtx, cancelAnchor := context.WithTimeout(ctx, anchorReadTimeout)
	anchor, anchorErr := price.FetchAnchor(anchorCtx, sharedClient(anchorReadTimeout))
	cancelAnchor()
	if anchorErr != nil {
		return nil, anchorErr
	}

	summaries, err := a.db.TokenSummaries(network)
	if err != nil {
		return nil, err
	}
	assets := a.priceAssets(summaries)

	keys, err := a.poolKeys(ctx, network, eval, summaries)
	if err != nil {
		return nil, err
	}

	res := price.Refresh(ctx, eval, anchor, assets, keys, a.db.EarliestTokenTransfer(network))
	res.Network = network
	return res, nil
}

// priceAssets projects the ledger's view of each token plus the registry's onto
// what pkg/price needs.
//
// DecimalsKnown is the field that matters, and it is false far more often than
// a reader would guess: the registry carries decimals for 2 of the 28 assets on
// mainnet, and asking the realm works on 2 of 7 tested. Everything else gets 6
// and a warning saying so.
func (a *API) priceAssets(summaries []store.TokenSummary) []price.Asset {
	out := make([]price.Asset, 0, len(summaries))
	for _, t := range summaries {
		asset := price.Asset{
			Token:    t.Token,
			Symbol:   t.Symbol,
			PkgPath:  t.PkgPath,
			Supply:   t.Supply,
			Fungible: t.Fungible,
		}
		if meta, ok := a.registry.Tokens[t.Token]; ok {
			asset.Verified = meta.Verified
			if meta.Symbol != "" {
				asset.Symbol = meta.Symbol
			}
			if meta.Decimals > 0 {
				asset.Decimals = meta.Decimals
				asset.DecimalsKnown = true
			}
		}
		out = append(out, asset)
	}
	return out
}

// poolKeys returns the network's pool set, discovering it when the cached one
// has expired.
//
// The candidate list is every fungible asset in the ledger plus wugnot, which
// on mainnet is about 15 tokens and therefore about 420 probes. That is a real
// cost and it is why this is behind a 30-minute cache and never on a request's
// critical path twice.
//
// A discovery that fails keeps the previous set rather than emptying it: a pool
// does not stop existing because one probe round timed out, and an empty set
// silently reprices every token to "no market".
func (a *API) poolKeys(ctx context.Context, network string, eval price.Eval, summaries []store.TokenSummary) ([]price.PoolKey, error) {
	assetPrices.mu.Lock()
	cached, ok := assetPrices.pools[network]
	fresh := ok && time.Since(assetPrices.poolsAt[network]) < poolDiscoveryTTL
	assetPrices.mu.Unlock()
	if fresh {
		return cached, nil
	}

	// The walk starts at wugnot, so the candidate list is everything it might
	// reach. A price is a route to the anchor: a pool between two tokens that
	// reaches neither cannot price either of them, and probing for it is pure
	// cost.
	candidates := []string{price.WUGNOT}
	seen := map[string]bool{price.WUGNOT: true}
	for _, t := range summaries {
		// Non-fungible assets ride the same Transfer event and carry no amount
		// (GRC721 through grc20's shape), so they cannot be in a pool and
		// probing them is pure cost.
		// Pool-token space, not event-key space: a pool path is built from
		// `<realm path>.<name>` and the ledger's key appends a `.<id>`. Passing
		// the ledger's key makes every probe answer false, which looks exactly
		// like a chain with no pools (shipped, 2026-09-29). Several tokens of
		// one realm collapse to distinct keys, so the dedupe still holds.
		tok := price.PoolToken(t.Token)
		if !t.Fungible || seen[tok] {
			continue
		}
		seen[tok] = true
		candidates = append(candidates, tok)
	}

	discoverCtx, cancel := context.WithTimeout(ctx, poolDiscoveryTimeout)
	defer cancel()
	found, err := price.DiscoverPools(discoverCtx, eval, price.WUGNOT, candidates, 3, priceConcurrency)
	if err != nil || len(found) == 0 {
		if ok {
			return cached, nil
		}
		return nil, err
	}
	assetPrices.mu.Lock()
	assetPrices.pools[network] = found
	assetPrices.poolsAt[network] = time.Now()
	assetPrices.mu.Unlock()
	return found, nil
}
