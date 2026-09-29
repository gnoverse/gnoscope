package price

import (
	"context"
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
		if a.Token == WUGNOT {
			usdPerBase = anchorBase
			q.Tier = TierMarket
			q.DecimalsKnown = true
			q.Decimals = ugnotDecimals
		} else {
			routes = g.Routes(a.Token, 3, usd, tvl)
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
				q.Depth = in.Depth[a.Token]
				q.Tier = TierFor(q.Depth)
			} else {
				q.Tier = TierNone
			}
		}

		q.TierLabel = q.Tier.Label()
		q.TierExplain = q.Tier.Explain()

		if usdPerBase != nil {
			q.USDPerBaseUnit = ratString(usdPerBase, 18)
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(q.Decimals)), nil)
			q.USDPerToken = ratFloat(new(big.Rat).Mul(usdPerBase, new(big.Rat).SetInt(scale)))
			if a.Fungible && a.Supply > 0 {
				q.FDVUSD = ratFloat(new(big.Rat).Mul(new(big.Rat).SetInt64(a.Supply), usdPerBase))
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
			LedgerFrom:       in.LedgerFrom,
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
	for _, k := range keys {
		p, err := LoadPool(ctx, eval, k)
		if err != nil {
			continue
		}
		pools = append(pools, p)
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
		Depth: depth, LedgerFrom: ledgerFrom,
	})
	for _, p := range pools {
		if p.ObservationCardinality > 1 {
			res.TWAPAvailable = true
		}
	}
	return res
}
