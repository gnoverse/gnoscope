package price

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"time"
)

// Asset is what the caller knows about a token before any pricing happens: the
// ledger's view plus whatever the curated registry added.
type Asset struct {
	Token   string
	Symbol  string
	PkgPath string
	// Supply is the replayed supply in base units, and is only ever used to
	// produce an FDV figure that arrives carrying a warning about itself.
	Supply   int64
	Fungible bool
	// Decimals and DecimalsKnown are separate because "6" and "we know it is 6"
	// are different claims, and the second is what decides whether a per-token
	// price is printed as a figure or as an assumption.
	Decimals      int
	DecimalsKnown bool
	Verified      bool
}

// Inputs is everything Build needs. Assembled by the caller so this package
// never touches a database, an HTTP client or a clock of its own.
type Inputs struct {
	Assets []Asset
	Pools  []*Pool
	Anchor *Anchor
	// ChainSupply is what each token's own realm says its supply is, keyed in
	// pool-token space. Present only for the tokens that answered; a token that
	// did not gets no supply-times-price figure at all, rather than one built
	// on the replayed floor.
	ChainSupply map[string]int64
	// Depth is the measured slippage ladder per token, keyed by event key.
	// Absent for a token means no measurement was made, which is treated as no
	// tier rather than as a bad one.
	Depth map[string][]DepthPoint
	// LedgerFrom dates the start of the transfer ledger, so the FDV warning can
	// say what the supply figure is missing. Empty when unknown.
	LedgerFrom string
	Now        time.Time
}

// Result is the whole priced picture, plus the two facts a reader needs before
// any of it: what the dollar came from, and how much of the chain has a market
// at all.
type Result struct {
	Network string  `json:"network"`
	Anchor  *Anchor `json:"anchor"`
	Quotes  []Quote `json:"quotes"`

	// PoolCount and PricedCount are the headline honesty numbers. Five pools
	// for a whole chain is the single most important thing on this page and it
	// is invisible unless stated.
	PoolCount   int `json:"pool_count"`
	PricedCount int `json:"priced_count"`
	AssetCount  int `json:"asset_count"`

	// Unavailable names the reason there are no prices, when there are none.
	// Empty on a successful read, including one that legitimately priced
	// nothing. An absent price and an unreadable one look identical on a page
	// and mean opposite things: the first is a fact about the chain, the second
	// is a fact about this server.
	Unavailable string `json:"unavailable,omitempty"`

	// PartialReads is how many known pools could not be read this pass. Any
	// token priced only through one of them will say "no market", which is a
	// fact about this server rather than about the chain, so the page has to be
	// able to tell a reader that.
	PartialReads int `json:"partial_reads,omitempty"`

	// SupplyReadsFailed is how many priced tokens would not answer
	// TotalSupply() this pass. Those show no supply-times-price figure, and the
	// count is what separates "this realm does not expose it" from "this pass
	// could not reach the chain".
	SupplyReadsFailed int `json:"supply_reads_failed,omitempty"`

	// TWAPAvailable is false on gno.land and has never been true. Carried as a
	// field rather than assumed, so the day somebody calls
	// IncreaseObservationCardinalityNext the page stops saying otherwise
	// without anyone having to remember to edit it.
	TWAPAvailable bool `json:"twap_available"`

	ComputedAt time.Time `json:"computed_at"`
}

// Build turns pools plus an anchor into quotes.
//
// Pure: no network, no clock, no database. Everything that could fail has
// already failed by the time it is called, which is what makes the tier logic
// and the warning assembly testable against the real measured numbers rather
// than against a mock chain.
func Build(in Inputs) *Result {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res := &Result{
		Anchor:     in.Anchor,
		PoolCount:  len(in.Pools),
		AssetCount: len(in.Assets),
		ComputedAt: now,
	}
	if in.Anchor == nil || in.Anchor.USDPerGNOT <= 0 {
		// No anchor means no USD, and a token-to-token price with no dollar to
		// hang it on is not what anybody came here for. Returning the counts
		// with no quotes is the honest empty state.
		return res
	}

	anchorBase := ugnotUSDRat(in.Anchor.USDPerGNOT)
	usd := map[string]*big.Rat{WUGNOT: anchorBase}

	// TVL per pool, needed before routing because it is what ranks routes.
	// Computed by solving the graph once with a cheap pass: any pool touching
	// wugnot is priceable immediately, and the rest resolve as their neighbours
	// do. Three passes is more than enough for a five-pool chain and terminates
	// regardless.
	g := NewGraph(in.Pools)
	for pass := 0; pass < 3; pass++ {
		for _, p := range in.Pools {
			ratio, err := p.Ratio()
			if err != nil {
				continue
			}
			if v, ok := usd[p.Key.Token0]; ok {
				if _, done := usd[p.Key.Token1]; !done {
					usd[p.Key.Token1] = new(big.Rat).Quo(v, ratio)
				}
			}
			if v, ok := usd[p.Key.Token1]; ok {
				if _, done := usd[p.Key.Token0]; !done {
					usd[p.Key.Token0] = new(big.Rat).Mul(v, ratio)
				}
			}
		}
	}
	tvl := map[string]float64{}
	for _, p := range in.Pools {
		t := 0.0
		if v, ok := usd[p.Key.Token0]; ok {
			t += ratFloat(new(big.Rat).Mul(new(big.Rat).SetInt64(p.Reserve0), v))
		}
		if v, ok := usd[p.Key.Token1]; ok {
			t += ratFloat(new(big.Rat).Mul(new(big.Rat).SetInt64(p.Reserve1), v))
		}
		tvl[p.Key.Path()] = t
	}

	for _, a := range in.Assets {
		q := Quote{
			Token:         a.Token,
			Symbol:        a.Symbol,
			PkgPath:       a.PkgPath,
			Decimals:      a.Decimals,
			DecimalsKnown: a.DecimalsKnown,
			ReadAt:        now,
		}
		if q.Decimals <= 0 {
			// The chain-wide convention, and the only defensible guess: every
			// token that has ever answered Decimals() on this chain answered 6,
			// and ugnot is 6 by definition. It is still a guess, and it still
			// gets a warning.
			q.Decimals = 6
		}

		var (
			routes     []route
			usdPerBase *big.Rat
		)
		// Route in pool-token space, report in event-key space. A quote keeps
		// the event key as its identity because that is what every other
		// endpoint here joins on; the pool graph knows nothing about the `.id`
		// segment. See PoolToken.
		pk := PoolToken(a.Token)
		if pk == WUGNOT {
			usdPerBase = anchorBase
			q.Tier = TierMarket
			q.DecimalsKnown = true
			q.Decimals = ugnotDecimals
		} else {
			routes = g.Routes(pk, 3, usd, tvl)
			if len(routes) > 0 {
				best := routes[0]
				usdPerBase = best.usdPerBase
				for _, h := range best.hops {
					from, to := h.Key.Token0, h.Key.Token1
					q.Route = append(q.Route, Hop{
						PoolPath: h.Key.Path(), From: from, To: to, Fee: h.Key.Fee,
						TVLUSD: tvl[h.Key.Path()],
					})
				}
				q.RouteCount = len(routes)
				q.RouteSpreadPct = routeSpread(routes)
				q.Depth = in.Depth[pk]
				q.Tier = TierFor(q.Depth)
			} else {
				q.Tier = TierNone
			}
		}

		q.TierLabel = q.Tier.Label()
		q.TierExplain = q.Tier.Explain()

		// Depth this token actually sits in, over EVERY pool it touches rather
		// than only the route that priced it. A token with one deep pool and
		// one shallow one is a different asset from one with a single pool of
		// the same total, and the route alone cannot say which it is.
		for _, p := range in.Pools {
			if p.Key.Token0 != pk && p.Key.Token1 != pk {
				continue
			}
			if t := tvl[p.Key.Path()]; t > 0 {
				q.TVLUSD += t
				q.Pools++
			}
		}

		if usdPerBase != nil {
			q.USDPerBaseUnit = ratString(usdPerBase, 18)
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(q.Decimals)), nil)
			q.USDPerToken = ratFloat(new(big.Rat).Mul(usdPerBase, new(big.Rat).SetInt(scale)))
			// Supply times price comes from the CHAIN's supply, never from the
			// replayed one. The replayed figure is a floor by construction, and
			// using it understated GNS by 20x while producing nothing at all
			// for the three tokens whose replay nets to zero or negative. A
			// token whose realm will not answer gets no figure: an absent
			// number is honest, a floor dressed as a total is not.
			if chain, ok := in.ChainSupply[pk]; a.Fungible && ok && chain > 0 {
				q.ChainSupply = chain
				q.FDVUSD = ratFloat(new(big.Rat).Mul(new(big.Rat).SetInt64(chain), usdPerBase))
			}
			res.PricedCount++
		}

		worstTVL, worstSlip, worstNotional := worstRung(q.Route, q.Depth)
		q.buildWarnings(warningInputs{
			WorstTVLUSD:      worstTVL,
			WorstSlippagePct: worstSlip,
			WorstNotionalUSD: worstNotional,
			RouteCount:       q.RouteCount,
			RouteSpreadPct:   q.RouteSpreadPct,
			DecimalsKnown:    q.DecimalsKnown,
			Verified:         a.Verified,
			AnchorSpreadPct:  in.Anchor.SpreadPct,
			HasFDV:           q.FDVUSD > 0,
		})
		res.Quotes = append(res.Quotes, q)
	}

	// Priced first, deepest first, then everything with no market in a stable
	// order behind them. A list sorted by supply would put the $47 tokens at
	// the top, which is precisely the reading this package exists to prevent.
	sort.SliceStable(res.Quotes, func(i, j int) bool {
		a, b := res.Quotes[i], res.Quotes[j]
		if (a.Tier == TierNone) != (b.Tier == TierNone) {
			return b.Tier == TierNone
		}
		if a.FDVUSD != b.FDVUSD {
			return a.FDVUSD > b.FDVUSD
		}
		return a.Symbol < b.Symbol
	})
	return res
}

// worstRung finds the first notional at which the route stops behaving, and the
// shallowest pool on it. Those two numbers are what the thin-pool warning is
// made of, and picking the *first* failing rung rather than the worst one keeps
// the sentence useful: "$100 costs you 1.8%" tells a reader what they can do,
// "$10,000 costs you 70%" tells them about a trade they were never going to make.
func worstRung(route []Hop, depth []DepthPoint) (tvl, slip float64, notional int) {
	for _, h := range route {
		if h.TVLUSD > 0 && (tvl == 0 || h.TVLUSD < tvl) {
			tvl = h.TVLUSD
		}
	}
	for _, d := range depth {
		if !d.Quoted || d.SlippagePct >= 1 {
			return tvl, d.SlippagePct, d.NotionalUSD
		}
	}
	if len(depth) > 0 {
		last := depth[len(depth)-1]
		if last.SlippagePct > 0 {
			return tvl, last.SlippagePct, last.NotionalUSD
		}
	}
	return tvl, 0, 0
}

// routeSpread is how far apart the cheapest and dearest routes price the same
// token, in percent of the cheaper one.
func routeSpread(routes []route) float64 {
	if len(routes) < 2 {
		return 0
	}
	lo, hi := routes[0].usdPerBase, routes[0].usdPerBase
	for _, r := range routes {
		if r.usdPerBase == nil {
			continue
		}
		if r.usdPerBase.Cmp(lo) < 0 {
			lo = r.usdPerBase
		}
		if r.usdPerBase.Cmp(hi) > 0 {
			hi = r.usdPerBase
		}
	}
	if lo == nil || lo.Sign() == 0 {
		return 0
	}
	return ratFloat(new(big.Rat).Quo(new(big.Rat).Sub(hi, lo), lo)) * 100
}

// Refresh is the whole read pass against a chain, in order.
//
// Split out of Build so that the pure half stays pure: this is the only
// function in the package that talks to a node, and every number it produces is
// handed to Build as data.
func Refresh(ctx context.Context, eval Eval, anchor *Anchor, assets []Asset, keys []PoolKey, ledgerFrom string) *Result {
	var pools []*Pool
	var failed int
	var lastErr error
	for _, k := range keys {
		p, err := LoadPool(ctx, eval, k)
		if err != nil {
			failed++
			lastErr = err
			continue
		}
		pools = append(pools, p)
	}

	// A pool that could not be READ is not a pool that does not exist, and the
	// difference is the whole discipline of this package. Skipping a failed
	// load and carrying on produced, live on 2026-09-29, a perfectly
	// well-formed answer saying the chain has 0 pools and 1 priced asset: val1
	// was getting 403 from rpc.gno.land while the pool set sat in cache, every
	// LoadPool failed, and the empty result was published and then cached as
	// though the liquidity had gone away.
	//
	// So: if there were pools to read and none of them could be, say so and
	// price nothing. Anything else is this feature committing the exact error
	// it exists to prevent.
	if len(keys) > 0 && len(pools) == 0 {
		res := &Result{
			PoolCount:   0,
			AssetCount:  len(assets),
			ComputedAt:  time.Now().UTC(),
			Anchor:      anchor,
			Unavailable: fmt.Sprintf("none of the %d known pools could be read: %v", len(keys), lastErr),
		}
		return res
	}
	// Supply comes BEFORE the depth ladder, and the order is the fix rather
	// than a preference.
	//
	// Depth is by far the most expensive step here: four router quotes per
	// priced token, each a real VM call. Supply is one cheap read per token.
	// With supply last, a slow chain spent the refresh budget on the ladder and
	// every supply read failed on an expired context, so FDV silently
	// disappeared from every asset. Observed live 2026-09-29: `pool_count: 5,
	// priced_count: 5` and `fdv_usd` absent everywhere, which reads as "these
	// tokens have no supply" rather than as "this pass ran out of time".
	//
	// Same shape as the anchor, which had to move to the front for the same
	// reason. Cheap and load-bearing goes first; expensive and refining goes
	// last, so what gets dropped under pressure is the least of it.
	inPool := map[string]bool{}
	for _, p := range pools {
		inPool[p.Key.Token0] = true
		inPool[p.Key.Token1] = true
	}
	chainSupply := map[string]int64{}
	var supplyFailed int
	for _, a := range assets {
		pk := PoolToken(a.Token)
		if !a.Fungible || !inPool[pk] || pk == WUGNOT {
			continue
		}
		if _, done := chainSupply[pk]; done {
			continue
		}
		if v, ok := FetchTotalSupply(ctx, eval, a.PkgPath, a.Symbol); ok {
			chainSupply[pk] = v
		} else {
			supplyFailed++
		}
	}

	depth := map[string][]DepthPoint{}
	if anchor != nil {
		for _, p := range pools {
			for _, tok := range []string{p.Key.Token0, p.Key.Token1} {
				if tok == WUGNOT {
					continue
				}
				if _, done := depth[tok]; done {
					continue
				}
				if d := MeasureDepth(ctx, eval, p, tok, anchor.USDPerGNOT); len(d) > 0 {
					depth[tok] = d
				}
			}
		}
	}

	res := Build(Inputs{
		Assets: assets, Pools: pools, Anchor: anchor,
		Depth: depth, ChainSupply: chainSupply, LedgerFrom: ledgerFrom,
	})
	// A partial read is not a failure, but it is not silence either: some
	// tokens will be priced and others will say "no market" for a reason that
	// has nothing to do with the chain.
	if failed > 0 {
		res.PartialReads = failed
	}
	// A token whose realm would not answer gets no supply-times-price figure,
	// and the absence has to be attributable: "this realm does not expose
	// TotalSupply" and "this pass could not reach the chain" look identical on
	// a page and are different facts.
	if supplyFailed > 0 {
		res.SupplyReadsFailed = supplyFailed
	}
	for _, p := range pools {
		if p.ObservationCardinality > 1 {
			res.TWAPAvailable = true
		}
	}
	return res
}
