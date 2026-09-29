// Package price prices GRC20 assets, and says how much that price is worth
// believing.
//
// The honest summary of gno.land on 2026-09-29, every figure below measured
// against mainnet rather than assumed:
//
//   - There are **five** liquidity pools on the entire chain, all on GnoSwap v1.
//   - One of them (wugnot/GNS) holds $759,104. The other four hold $5,687, $92,
//     $78 and $47.
//   - Six of the 28 indexed assets touch a pool. Twenty-two have no market at
//     all, so they have no price, and this package returns none for them.
//   - The chain has no USD oracle. GNOT trades on Kraken and KuCoin, so the USD
//     leg is off-chain by necessity, not by choice.
//   - GnoSwap ships a Uniswap-V3 TWAP (`pool.OracleConsult`) and it is useless:
//     every pool reports observationCardinality = 1, so the "average" is the
//     spot tick. See TierFor and warnTWAPIsSpot.
//
// That is the whole reason this package is shaped the way it is. A price here is
// never a bare number. It is a number, the route that produced it, the depth
// behind it, a tier that says which of those four decimal places mean anything,
// and a list of warnings each carrying a paragraph the frontend shows on hover.
//
// The alternative was the position this repo held until now, written into
// pkg/httpapi/assets.go: show no money columns at all, because any figure would
// be invented. That was right when it was written and it is wrong now, for one
// reason only: GNOT acquired a real market. What it got right, and what this
// package keeps, is that an unqualified number is worse than no number.
package price

import (
	"math/big"
	"time"
)

// Tier says what a price is good for. It is derived from measured slippage,
// not from anybody's opinion of a token.
//
// The thresholds come from a ladder run against all four priced assets on
// 2026-09-29: GnoSwap's own `router.DrySwapRoute` quoting a
// buy at $10 / $100 / $1,000 / $10,000, compared against the spot price the
// same pool reports. Fee included, because a reader cannot spend the fee-free
// price either.
//
//	token   $10      $100     $1,000   $10,000
//	GNS     0.45%    0.45%    0.46%    0.52%
//	BUBBLE  0.59%    1.84%    16.06%   69.93%
//	PERUN   18.66%   68.65%   95.61%   99.54%
//	GNOMIC  21.44%   71.32%   96.61%   99.66%
//
// Those four rows are four different kinds of number wearing the same
// formatting, which is exactly what a tier exists to stop.
type Tier string

const (
	// TierMarket: you could trade $10,000 and get within 2% of this. GNS is the
	// only asset on the chain in this tier today.
	TierMarket Tier = "market"
	// TierIndicative: right at $1,000, wrong at size. No asset on mainnet is in
	// this tier today. It is not a placeholder: it is the gap between GNS and
	// BUBBLE, and the day a second serious pool opens it is where that pool
	// lands before it is deep enough to be a market.
	TierIndicative Tier = "indicative"
	// TierThin: right at $100 and nothing beyond. BUBBLE, whose $5,687 pool
	// costs 1.84% at $100 and 16.06% at $1,000.
	TierThin Tier = "thin"
	// TierDecorative: the number exists, the market does not. A $10 trade moves
	// GNOMIC 21%, and its supply times this price is $582,003 against a pool
	// holding $47.
	TierDecorative Tier = "decorative"
	// TierNone: no route to wugnot, so no price. Twenty-two of 28 assets.
	// Rendered as an explicit "no market" rather than a blank cell, because a
	// blank reads as a loading failure.
	TierNone Tier = "none"
)

// Label is the one-word badge the frontend prints beside a figure.
func (t Tier) Label() string {
	switch t {
	case TierMarket:
		return "market"
	case TierIndicative:
		return "indicative"
	case TierThin:
		return "thin"
	case TierDecorative:
		return "decorative"
	default:
		return "no market"
	}
}

// Explain is the tooltip. Long on purpose: the tier is the single most
// load-bearing thing on the row and a reader who does not understand it will
// read a decorative price as a market one.
func (t Tier) Explain() string {
	switch t {
	case TierMarket:
		return "A $10,000 trade against this pool settles within 2% of the price shown. " +
			"This is a price in the ordinary sense: you could act on it."
	case TierIndicative:
		return "A $1,000 trade settles within 5% of the price shown, but a larger one does not. " +
			"Treat this as the right order of magnitude, not as a quote."
	case TierThin:
		return "Only a trade of about $100 settles anywhere near this price. " +
			"It tells you the direction of value, not the value."
	case TierDecorative:
		return "There is effectively no market here. A trade of $10, the price of a sandwich, " +
			"moves this pool by more than 15%. The number is arithmetic over a pool that " +
			"nobody can trade against, and multiplying it by a token's supply produces a " +
			"market cap that has never existed."
	default:
		return "No GnoSwap pool routes this token to wugnot, so there is no market price to " +
			"compute from. Nothing is shown rather than a zero, because zero would be a claim."
	}
}

// Severity orders the warnings for display and picks their colour.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityCaution Severity = "caution"
	SeverityAlarm   Severity = "alarm"
)

// Warning is one caveat attached to a price.
//
// Three fields rather than one string, because they land in three different
// places: Code is what a test asserts on and what an API consumer switches on,
// Short is what fits in a badge, and Explain is the paragraph the frontend
// hangs off a title= attribute. Writing only Short leaves a reader who hovers
// with nothing; writing only Explain leaves no room to show anything at all.
type Warning struct {
	Code     string   `json:"code"`
	Short    string   `json:"short"`
	Explain  string   `json:"explain"`
	Severity Severity `json:"severity"`
}

// The warning catalogue. Every one of these was measured, not imagined, and the
// measurement is in the Explain text so a reader can check it rather than take
// it. Dates are absolute for the same reason.
var (
	// warnNoOnchainOracle is on every price, always. It is the one caveat that
	// cannot be engineered away from inside the chain.
	warnNoOnchainOracle = Warning{
		Code:     "no-onchain-oracle",
		Short:    "USD leg is off-chain",
		Severity: SeverityCaution,
		Explain: "gno.land has no price oracle. Nothing on the chain knows what a dollar is. " +
			"The USD figure here comes from GNOT's price on Kraken and KuCoin, fetched by this " +
			"server over the public internet, and every token price scales linearly with that " +
			"one number. If the anchor is stale, wrong, or manipulated, so is everything below it.",
	}

	// warnTWAPIsSpot is the finding that surprised me most, and the one most
	// likely to be repeated as a mistake by anyone else building on GnoSwap.
	warnTWAPIsSpot = Warning{
		Code:     "twap-is-spot",
		Short:    "no time-weighted price exists",
		Severity: SeverityAlarm,
		Explain: "GnoSwap implements the Uniswap V3 time-weighted average price, and calling " +
			"pool.OracleConsult returns a number. That number is the spot tick. Every pool on " +
			"mainnet reports observationCardinality = 1 (measured 2026-09-29, all five), which " +
			"means one stored observation and therefore no history to average over: the oracle " +
			"answers the current tick for a 1-second window and for a 30-minute one alike, and " +
			"errors past that. So no manipulation-resistant price exists anywhere on this chain. " +
			"A single transaction can set the price this page shows. Fixing it is one " +
			"permissionless IncreaseObservationCardinalityNext call per pool, which nobody has made.",
	}

	// warnSpotNotAverage is separate from warnTWAPIsSpot on purpose. One says
	// "the average does not exist", the other says "this is an instant". A
	// reader can understand the second without the first.
	warnSpotNotAverage = Warning{
		Code:     "spot-not-average",
		Short:    "instant, not an average",
		Severity: SeverityInfo,
		Explain: "This is the pool's price at one block, read just now. It is not a daily " +
			"average, not volume-weighted, and carries no history: a trade that lands one " +
			"block later changes it, and this page will not know until it refreshes.",
	}

	// warnSingleVenue matters more here than on a chain with several DEXes,
	// because there is no second venue to arbitrage against.
	warnSingleVenue = Warning{
		Code:     "single-venue",
		Short:    "one venue only",
		Severity: SeverityCaution,
		Explain: "GnoSwap is the only decentralised exchange on gno.land, so there is no second " +
			"market to cross-check against and no arbitrage pressure keeping this price " +
			"honest. On a chain with several venues a wrong price gets corrected in minutes " +
			"because correcting it is profitable. Here nothing corrects it.",
	}

	// warnDecimalsUnknown is why this package reports a base-unit price as the
	// primary figure and the per-token price as a derived one.
	warnDecimalsUnknown = Warning{
		Code:     "decimals-unknown",
		Short:    "decimals assumed",
		Severity: SeverityCaution,
		Explain: "This token's realm exposes no Decimals(), and the curated registry has no " +
			"entry for it, so the per-token figure assumes 6 decimals. Only 2 of 7 tokens " +
			"tested answered Decimals() (measured 2026-09-29: gns and gnomic yes; wugnot, " +
			"wbubble, grc20factory, xgns and padv3 no). If the real figure is not 6, the " +
			"per-token price is wrong by a factor of ten per digit. The price per base unit " +
			"beside it is unaffected: that one is an exact ratio and needs no decimals at all.",
	}

	// warnUnverifiedToken mirrors the registry's own framing.
	warnUnverifiedToken = Warning{
		Code:     "unverified-token",
		Short:    "token not verified",
		Severity: SeverityCaution,
		Explain: "Nobody has confirmed this is the token it claims to be. Anyone may deploy a " +
			"realm called gns and mint a token called GNS, and a pool may be created against " +
			"the impostor. Verification here means a human asserted the match in a merged pull " +
			"request; its absence is not an accusation, only the absence of a check.",
	}
)

// warnThinPool is generated rather than constant, because the whole value of it
// is the two numbers in it.
func warnThinPool(tvlUSD float64, worstSlippagePct float64, notionalUSD int) Warning {
	sev := SeverityCaution
	if worstSlippagePct >= 15 {
		sev = SeverityAlarm
	}
	return Warning{
		Code:     "thin-pool",
		Short:    "thin market",
		Severity: sev,
		Explain: "The shallowest pool on this route holds " + usd(tvlUSD) + " in total. " +
			"A buy of " + usd(float64(notionalUSD)) + " against it settles " +
			pct(worstSlippagePct) + " away from the price shown, measured by asking GnoSwap's " +
			"own router for a quote rather than by modelling it. Depth this small means the " +
			"price is set by whoever traded last, and can be moved by anyone willing to spend " +
			"pocket change.",
	}
}

// thinPoolFloorPct is the slippage below which a pool is not called thin. See
// buildWarnings.
const thinPoolFloorPct = 1.0

// routeSpreadFloorPct is the gap below which two routes are treated as
// agreeing. See buildWarnings for why it is not zero.
const routeSpreadFloorPct = 0.1

// warnRouteDisagreement fires when two independent routes to wugnot produce
// different answers. It is the only cross-check this chain offers for free, so
// it is worth printing even when the gap is small.
func warnRouteDisagreement(spreadPct float64, routes int) Warning {
	sev := SeverityInfo
	if spreadPct >= 2 {
		sev = SeverityCaution
	}
	if spreadPct >= 10 {
		sev = SeverityAlarm
	}
	return Warning{
		Code:     "route-disagreement",
		Short:    "routes disagree",
		Severity: sev,
		Explain: "This token reaches wugnot by " + itoa(routes) + " different pool routes, and " +
			"they price it " + pct(spreadPct) + " apart. The figure shown is from the deepest " +
			"route. A gap under about 1% is ordinary arbitrage lag; a large one means at least " +
			"one of the pools is stale or is being used as something other than a market.",
	}
}

// warnAnchorDisagreement fires when the off-chain venues disagree about GNOT.
func warnAnchorDisagreement(spreadPct float64) Warning {
	return Warning{
		Code:     "anchor-disagreement",
		Short:    "GNOT venues disagree",
		Severity: SeverityCaution,
		Explain: "The exchanges quoted for GNOT/USD differ by " + pct(spreadPct) + ". The value " +
			"used is the volume-weighted one where a venue publishes it. A wide gap usually " +
			"means one venue is illiquid rather than that the price is uncertain, but it " +
			"propagates into every token figure on this page either way.",
	}
}

// warnFDVNotMarketCap exists because supply times price is the single most
// misread number in this whole area, and on this chain the supply half is
// independently doubtful.
func warnFDVNotMarketCap(ledgerFrom string) Warning {
	w := Warning{
		Code:     "fdv-not-marketcap",
		Short:    "fully diluted, and the supply is a floor",
		Severity: SeverityCaution,
		Explain: "This is supply multiplied by price. It is not a market capitalisation: no part " +
			"of that supply has to be liquid, and on a pool this size almost none of it is. " +
			"Selling even a fraction of it at the price shown is not possible.",
	}
	if ledgerFrom != "" {
		w.Explain += " The supply itself is replayed from Transfer events this indexer walked, " +
			"starting at " + ledgerFrom + ", so anything minted before that is missing and the " +
			"supply is a floor rather than a total."
	}
	return w
}

// Hop is one pool traversed on the way from a token to wugnot.
type Hop struct {
	PoolPath string `json:"pool_path"`
	From     string `json:"from"`
	To       string `json:"to"`
	Fee      uint32 `json:"fee"`
	// TVLUSD is the pool's whole depth, both reserves priced. The minimum of
	// these across a route is what decides the tier.
	TVLUSD float64 `json:"tvl_usd"`
}

// DepthPoint is one rung of the slippage ladder: what GnoSwap's router says you
// would actually receive for a given notional, against what spot claims.
//
// Measured rather than modelled. A constant-product approximation would be
// wrong here, because these are concentrated-liquidity pools and the curve
// depends on where the ticks sit.
type DepthPoint struct {
	NotionalUSD int     `json:"notional_usd"`
	SlippagePct float64 `json:"slippage_pct"`
	// Quoted is false when the router refused the quote, which happens on a
	// route whose liquidity runs out entirely. Distinguished from a 100%
	// slippage reading because they mean different things to a reader.
	Quoted bool `json:"quoted"`
}

// AnchorSource is one off-chain venue's opinion of GNOT in USD.
type AnchorSource struct {
	Venue string  `json:"venue"`
	Pair  string  `json:"pair"`
	USD   float64 `json:"usd"`
	// Kind is "vwap-24h" where the venue publishes one and "last" otherwise. A
	// 24-hour volume-weighted average is a materially better anchor than the
	// last trade, and the difference is worth naming rather than averaging away.
	Kind      string  `json:"kind"`
	Volume24h float64 `json:"volume_24h,omitempty"`
}

// Anchor is the GNOT/USD leg: the one number every other figure is multiplied by.
type Anchor struct {
	USDPerGNOT float64        `json:"usd_per_gnot"`
	Sources    []AnchorSource `json:"sources"`
	SpreadPct  float64        `json:"spread_pct"`
	FetchedAt  time.Time      `json:"fetched_at"`
}

// Quote is one asset's price and everything needed to disbelieve it.
type Quote struct {
	Token   string `json:"token"`
	Symbol  string `json:"symbol"`
	PkgPath string `json:"pkg_path"`

	Tier      Tier   `json:"tier"`
	TierLabel string `json:"tier_label"`
	// TierExplain travels with the row so the frontend never has to keep its
	// own copy of this text. Two copies of a caveat is how one of them goes
	// stale and gets shown.
	TierExplain string `json:"tier_explain"`

	// USDPerBaseUnit is the exact figure, and the only one that needs no
	// assumption about the token. A string because it is far below the range
	// where a JSON number keeps its digits.
	USDPerBaseUnit string `json:"usd_per_base_unit"`
	// USDPerToken applies Decimals. Present only when Decimals is trustworthy;
	// DecimalsKnown says which it is.
	USDPerToken   float64 `json:"usd_per_token"`
	Decimals      int     `json:"decimals"`
	DecimalsKnown bool    `json:"decimals_known"`

	// Route is token to wugnot. Empty for wugnot itself, which is the anchor.
	Route []Hop `json:"route"`
	// RouteCount and RouteSpreadPct are the free cross-check: how many
	// independent ways this token reaches wugnot, and how far apart they price it.
	RouteCount     int     `json:"route_count"`
	RouteSpreadPct float64 `json:"route_spread_pct"`

	Depth []DepthPoint `json:"depth"`

	// FDVUSD is supply times price, and carries warnFDVNotMarketCap whenever it
	// is set. Zero when supply is unknown or the asset is not fungible.
	FDVUSD float64 `json:"fdv_usd,omitempty"`

	Warnings []Warning `json:"warnings"`
	ReadAt   time.Time `json:"read_at"`
}

// TierFor picks a tier from a measured depth ladder.
//
// Deliberately pessimistic: the tier is decided by the worst rung that still
// passes, so a pool that is fine at $100 and catastrophic at $1,000 is not
// promoted on the strength of the $100 reading. A rung the router refused to
// quote counts as a failure at that notional, because for a reader it is one.
func TierFor(depth []DepthPoint) Tier {
	at := func(n int) (float64, bool) {
		for _, d := range depth {
			if d.NotionalUSD == n {
				return d.SlippagePct, d.Quoted
			}
		}
		return 0, false
	}
	if len(depth) == 0 {
		return TierNone
	}
	if s, ok := at(10000); ok && s < 2 {
		return TierMarket
	}
	if s, ok := at(1000); ok && s < 5 {
		return TierIndicative
	}
	if s, ok := at(100); ok && s < 5 {
		return TierThin
	}
	return TierDecorative
}

// Warnings assembles the caveat list for one quote.
//
// Order is severity first, then the order they are added, so the alarming ones
// are the ones a reader sees without expanding anything. The three that are
// always present come last in the code and first in nobody's attention, which
// is the right way round: they are true of every price here, so they carry no
// information about *this* one.
func (q *Quote) buildWarnings(in warningInputs) {
	// A token with no market has nothing to caveat. Telling a reader that an
	// unpriced token's decimals are unknown, or that GnoSwap's TWAP is spot, is
	// noise attached to an empty cell: the tier already says the only true
	// thing there is to say, and it says it in one word.
	if q.Tier == TierNone {
		return
	}
	var alarm, caution, info []Warning
	add := func(w Warning) {
		switch w.Severity {
		case SeverityAlarm:
			alarm = append(alarm, w)
		case SeverityCaution:
			caution = append(caution, w)
		default:
			info = append(info, w)
		}
	}

	// A pool is thin when it costs you something, not merely when it has a
	// finite depth. Without the floor this fired on GNS, whose worst rung is
	// 0.52% at $10,000 against a $759,104 pool, and a "thin market" badge on the
	// deepest pool on the chain teaches a reader that the badge means nothing.
	// One percent is the lowest number that is unambiguously worse than the
	// 0.30% fee the deep pool already charges.
	if in.WorstTVLUSD > 0 && in.WorstSlippagePct >= thinPoolFloorPct {
		add(warnThinPool(in.WorstTVLUSD, in.WorstSlippagePct, in.WorstNotionalUSD))
	}
	// Two routes always disagree a little, and a warning that fires on every
	// multi-route token teaches a reader to skip warnings. The floor is a tenth
	// of a percent, which is well inside the fee on the cheapest tier (0.01%)
	// plus ordinary block latency, so anything above it is a real gap rather
	// than arithmetic. GNS reaches wugnot directly and through PERUN and the
	// two price it 0.32% apart (measured 2026-09-29), which does clear it.
	if in.RouteCount > 1 && in.RouteSpreadPct >= routeSpreadFloorPct {
		add(warnRouteDisagreement(in.RouteSpreadPct, in.RouteCount))
	}
	if !in.DecimalsKnown {
		add(warnDecimalsUnknown)
	}
	if !in.Verified {
		add(warnUnverifiedToken)
	}
	if in.AnchorSpreadPct >= 1 {
		add(warnAnchorDisagreement(in.AnchorSpreadPct))
	}
	if in.HasFDV {
		add(warnFDVNotMarketCap(in.LedgerFrom))
	}
	add(warnTWAPIsSpot)
	add(warnSingleVenue)
	add(warnNoOnchainOracle)
	add(warnSpotNotAverage)

	q.Warnings = append(append(alarm, caution...), info...)
}

type warningInputs struct {
	WorstTVLUSD      float64
	WorstSlippagePct float64
	WorstNotionalUSD int
	RouteCount       int
	RouteSpreadPct   float64
	DecimalsKnown    bool
	Verified         bool
	AnchorSpreadPct  float64
	HasFDV           bool
	LedgerFrom       string
}

// ratString renders an exact ratio for JSON without pretending it is a float.
//
// A price per base unit is around 1e-8 USD, which a float64 holds but which
// json.Marshal renders in a form ("7.0707e-08") that several consumers parse
// back wrong or truncate. Rendering it ourselves at fixed precision keeps the
// digits that exist and adds none that do not.
func ratString(r *big.Rat, digits int) string {
	if r == nil {
		return ""
	}
	return r.FloatString(digits)
}
