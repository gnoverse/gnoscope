package price

import (
	"context"
	"math"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every fixture in this file is a verbatim mainnet reading taken on 2026-09-29,
// not a hand-made value. That is the point: the arithmetic is easy and the
// thing that actually breaks is a repr that does not parse or a token order
// that silently inverts a price, and neither is catchable against invented data.
const (
	sqrtWugnotGNS    = "158761177616844740432909320156" // wugnot/GNS 3000, tick 13902
	sqrtBubbleWugnot = "138593274276005217621702521786" // BUBBLE/wugnot 3000, tick 11184
	sqrtPerunGNS     = "163212212946449073201118118044" // PERUN/GNS 10000, tick 14455
	sqrtPerunWugnot  = "81579000262254204451513850777"  // PERUN/wugnot 10000, tick 584
	sqrtWugnotGnomic = "126503603319273409237122988918" // wugnot/GNOMIC 3000, tick 9359

	// Two key spaces, and conflating them served "no market" for every asset on
	// a chain with five live pools. `tok*` is what a pool path is built from,
	// `key*` is what the Transfer event carries and what the ledger stores.
	tokGNS    = "gno.land/r/gnoswap/gns.GNS"
	tokBubble = "gno.land/r/g1leu8d2vsplhehcfkjg50mwgdpxdkt8tztu95wr/wbubble.BUBBLE"
	tokPerun  = "gno.land/r/demo/defi/grc20factory.PERUN"
	tokGnomic = "gno.land/r/nym-thegnomic001/gnomic.GNOMIC"

	keyGNS    = tokGNS + ".0000000"
	keyBubble = tokBubble + ".0000000"
	keyPerun  = tokPerun + ".0000001"
	keyGnomic = tokGnomic + ".0000000"
	keyWugnot = WUGNOT + ".0000000"

	// GNOT/USD on Kraken (24h VWAP) when the rest of this was measured.
	gnotUSD = 0.070707
)

func TestRatioFromSqrtPriceX96(t *testing.T) {
	tests := []struct {
		name  string
		sqrt  string
		want  float64
		tol   float64
		wantE bool
	}{
		// 1 base wugnot buys 4.0154 base GNS. The independent check is the tick:
		// 1.0001^13902 = 4.0154.
		{name: "wugnot/GNS", sqrt: sqrtWugnotGNS, want: 4.01540592790, tol: 1e-9},
		{name: "BUBBLE/wugnot", sqrt: sqrtBubbleWugnot, want: 3.06002618474, tol: 1e-9},
		{name: "PERUN/GNS", sqrt: sqrtPerunGNS, want: 4.24371430922, tol: 1e-9},
		{name: "PERUN/wugnot", sqrt: sqrtPerunWugnot, want: 1.06022390019, tol: 1e-9},
		{name: "wugnot/GNOMIC", sqrt: sqrtWugnotGnomic, want: 2.54945073816, tol: 1e-9},
		{name: "empty", sqrt: "", wantE: true},
		{name: "not a number", sqrt: "0x1f", wantE: true},
		{name: "zero", sqrt: "0", wantE: true},
		{name: "negative", sqrt: "-5", wantE: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RatioFromSqrtPriceX96(tt.sqrt)
			if tt.wantE {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			f := ratFloat(got)
			if math.Abs(f-tt.want)/tt.want > tt.tol {
				t.Fatalf("ratio = %.12g, want %.12g", f, tt.want)
			}
		})
	}
}

// The sqrt price and the tick are two encodings of the same quantity, and a
// pool path with its tokens the wrong way round produces a ratio that is the
// reciprocal of the tick's. So this is not a test of the exponentiation, it is
// a test that the two readings describe the same pool.
func TestTickAgreesWithSqrtPrice(t *testing.T) {
	tests := []struct {
		name string
		sqrt string
		tick int32
	}{
		{"wugnot/GNS", sqrtWugnotGNS, 13902},
		{"BUBBLE/wugnot", sqrtBubbleWugnot, 11184},
		{"PERUN/GNS", sqrtPerunGNS, 14455},
		{"PERUN/wugnot", sqrtPerunWugnot, 584},
		{"wugnot/GNOMIC", sqrtWugnotGnomic, 9359},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fromSqrt, err := RatioFromSqrtPriceX96(tt.sqrt)
			if err != nil {
				t.Fatal(err)
			}
			fromTick := ratFloat(TickToRatio(tt.tick))
			a := ratFloat(fromSqrt)
			// A tick is a 1-basis-point bucket, so the two agree to within one
			// tick's width and no better.
			if math.Abs(a-fromTick)/a > 1e-4 {
				t.Fatalf("sqrt says %.8g, tick %d says %.8g", a, tt.tick, fromTick)
			}
		})
	}
}

func TestTickToRatioNegative(t *testing.T) {
	// The sign handling is the only branch in TickToRatio worth a test, and no
	// mainnet pool exercises it: all five sit at a positive tick.
	pos := ratFloat(TickToRatio(1000))
	neg := ratFloat(TickToRatio(-1000))
	if math.Abs(pos*neg-1) > 1e-9 {
		t.Fatalf("1.0001^1000 * 1.0001^-1000 = %.12g, want 1", pos*neg)
	}
}

func TestReprParsing(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		kind  string
		wantS string
		wantI int64
		wantB bool
		wantK bool
	}{
		{name: "sqrt price string", in: `("158761177616844740432909320156" string)`, kind: "s",
			wantS: "158761177616844740432909320156", wantK: true},
		{name: "empty string", in: `("" string)`, kind: "s", wantS: "", wantK: true},
		{name: "tick int32", in: `(13902 int32)`, kind: "i", wantI: 13902, wantK: true},
		{name: "negative tick", in: `(-1020 int32)`, kind: "i", wantI: -1020, wantK: true},
		{name: "balance int64", in: `(28769887840258 int64)`, kind: "i", wantI: 28769887840258, wantK: true},
		{name: "uint plain", in: `(6 uint)`, kind: "i", wantI: 6, wantK: true},
		{name: "bool true", in: `(true bool)`, kind: "b", wantB: true, wantK: true},
		{name: "bool false", in: `(false bool)`, kind: "b", wantB: false, wantK: true},
		// The whole reason a shared parser exists: a struct repr must not be
		// mistaken for any of the scalars.
		{name: "struct is not a scalar", in: `(struct{(1 uint16)} pool.Slot0)`, kind: "i", wantK: false},
		{name: "int is not a string", in: `(13902 int32)`, kind: "s", wantK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switch tt.kind {
			case "s":
				got, ok := ReprString(tt.in)
				if ok != tt.wantK || (ok && got != tt.wantS) {
					t.Fatalf("ReprString = %q,%v want %q,%v", got, ok, tt.wantS, tt.wantK)
				}
			case "i":
				got, ok := ReprInt(tt.in)
				if ok != tt.wantK || (ok && got != tt.wantI) {
					t.Fatalf("ReprInt = %d,%v want %d,%v", got, ok, tt.wantI, tt.wantK)
				}
			case "b":
				got, ok := ReprBool(tt.in)
				if ok != tt.wantK || (ok && got != tt.wantB) {
					t.Fatalf("ReprBool = %v,%v want %v,%v", got, ok, tt.wantB, tt.wantK)
				}
			}
		})
	}
}

// A missing pool answers 200 OK with a plausible first line. Reading only that
// line turns "there is no such pool" into "this pool is worth zero", which is
// the difference between an empty cell and a wrong number.
func TestReprErrored(t *testing.T) {
	missing := `("" string)` + "\n" +
		`(&(struct{("expected poolPath(gno.land/r/x:gno.land/r/y:3000) to exist" string)} gno.land/p/nt/ufmt/v0.errMsg) *gno.land/p/nt/ufmt/v0.errMsg)`
	ok := `(3571040328026 int64)` + "\n" + `(28769887840258 int64)` + "\n" + `(undefined)`
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"missing pool carries an errMsg", missing, true},
		{"healthy multi-return ends in undefined", ok, false},
		{"single value", `(13902 int32)`, false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReprErrored(tt.in); got != tt.want {
				t.Fatalf("ReprErrored = %v, want %v", got, tt.want)
			}
		})
	}
}

// The verbatim GetSlot0 response for wugnot/GNS. Kept whole rather than
// trimmed, because the parser's job is to survive the nested uint256 repr in
// front of the three uint16 fields it wants.
const slot0WugnotGNS = `(struct{(&(array[(17100230488246206428 uint64),(8606460683 uint64),(0 uint64),(0 uint64)] gno.land/p/gnoswap/uint256/v1.Uint) *gno.land/p/gnoswap/uint256/v1.Uint),(13902 int32),(0 uint8),(true bool),(0 uint16),(1 uint16),(1 uint16)} gno.land/r/gnoswap/pool.Slot0)`

func TestCardinalityFromSlot0(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		// The finding this whole feature hangs on: every mainnet pool reports 1,
		// so GnoSwap's TWAP returns the spot tick.
		{"mainnet wugnot/GNS", slot0WugnotGNS, 1},
		// Right after IncreaseObservationCardinalityNext, `next` jumps and
		// `cardinality` grows lazily as observations get written, so the three
		// fields differ. Distinct values on purpose: with all three equal this
		// case passes while reading the wrong one of them.
		{"a pool someone grew", strings.Replace(slot0WugnotGNS, "(0 uint16),(1 uint16),(1 uint16)", "(7 uint16),(16 uint16),(64 uint16)", 1), 16},
		{"garbage", "not a struct", 0},
		{"too few fields", "(1 uint16),(2 uint16)", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cardinalityFromSlot0(tt.in); got != tt.want {
				t.Fatalf("cardinality = %d, want %d", got, tt.want)
			}
		})
	}
}

// The measured slippage ladders, verbatim from the DrySwapRoute pass on
// 2026-09-29. These four rows are the entire justification for the tier
// thresholds, so the test asserts the mapping rather than the arithmetic.
func TestTierFor(t *testing.T) {
	ladder := func(a, b, c, d float64) []DepthPoint {
		return []DepthPoint{
			{NotionalUSD: 10, SlippagePct: a, Quoted: true},
			{NotionalUSD: 100, SlippagePct: b, Quoted: true},
			{NotionalUSD: 1000, SlippagePct: c, Quoted: true},
			{NotionalUSD: 10000, SlippagePct: d, Quoted: true},
		}
	}
	tests := []struct {
		name  string
		depth []DepthPoint
		want  Tier
	}{
		{"GNS, mainnet", ladder(0.45, 0.45, 0.46, 0.52), TierMarket},
		// BUBBLE is thin, not indicative: 1.84% at $100 is fine, 16.06% at
		// $1,000 is not, and the tier is decided by the worst rung that passes.
		{"BUBBLE, mainnet", ladder(0.59, 1.84, 16.06, 69.93), TierThin},
		{"PERUN, mainnet", ladder(18.66, 68.65, 95.61, 99.54), TierDecorative},
		{"GNOMIC, mainnet", ladder(21.44, 71.32, 96.61, 99.66), TierDecorative},
		{"good at $1,000 but not at size", ladder(0.5, 0.8, 3.0, 40.0), TierIndicative},
		{"no measurement", nil, TierNone},
		// A rung the router refused is a failure at that rung, not a missing
		// data point: a reader cannot trade through a refused quote either.
		{"unquoted top rung is not a pass", []DepthPoint{
			{NotionalUSD: 10, SlippagePct: 0.1, Quoted: true},
			{NotionalUSD: 100, SlippagePct: 0.1, Quoted: true},
			{NotionalUSD: 1000, SlippagePct: 0.2, Quoted: true},
			{NotionalUSD: 10000, SlippagePct: 100, Quoted: false},
		}, TierIndicative},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TierFor(tt.depth); got != tt.want {
				t.Fatalf("tier = %q, want %q", got, tt.want)
			}
		})
	}
}

// mainnetPools is the whole chain's liquidity on 2026-09-29. Five pools.
func mainnetPools() []*Pool {
	return []*Pool{
		{Key: PoolKey{Token0: tokBubble, Token1: WUGNOT, Fee: 3000},
			SqrtPriceX96: sqrtBubbleWugnot, Tick: 11184,
			Reserve0: 18305514717, Reserve1: 24412670405, ObservationCardinality: 1},
		{Key: PoolKey{Token0: tokPerun, Token1: tokGNS, Fee: 10000},
			SqrtPriceX96: sqrtPerunGNS, Tick: 14455,
			Reserve0: 519636009, Reserve1: 2204925713, ObservationCardinality: 1},
		{Key: PoolKey{Token0: tokPerun, Token1: WUGNOT, Fee: 10000},
			SqrtPriceX96: sqrtPerunWugnot, Tick: 584,
			Reserve0: 613514965, Reserve1: 650403017, ObservationCardinality: 1},
		{Key: PoolKey{Token0: WUGNOT, Token1: tokGnomic, Fee: 3000},
			SqrtPriceX96: sqrtWugnotGnomic, Tick: 9359,
			Reserve0: 185436989, Reserve1: 1227406092, ObservationCardinality: 1},
		{Key: PoolKey{Token0: WUGNOT, Token1: tokGNS, Fee: 3000},
			SqrtPriceX96: sqrtWugnotGNS, Tick: 13902,
			Reserve0: 3571040328026, Reserve1: 28769887840258, ObservationCardinality: 1},
	}
}

func mainnetInputs() Inputs {
	ladder := func(a, b, c, d float64) []DepthPoint {
		return []DepthPoint{
			{NotionalUSD: 10, SlippagePct: a, Quoted: true},
			{NotionalUSD: 100, SlippagePct: b, Quoted: true},
			{NotionalUSD: 1000, SlippagePct: c, Quoted: true},
			{NotionalUSD: 10000, SlippagePct: d, Quoted: true},
		}
	}
	return Inputs{
		// Assets arrive keyed the way the ledger keys them, with the `.<id>`
		// segment, because that is what the caller has.
		Assets: []Asset{
			{Token: keyGNS, Symbol: "GNS", PkgPath: "gno.land/r/gnoswap/gns",
				Supply: 108092465530720, Fungible: true, Decimals: 6, DecimalsKnown: true, Verified: true},
			{Token: keyWugnot, Symbol: "WUGNOT", PkgPath: "gno.land/r/gnoland/wugnot",
				Supply: 3685791634930, Fungible: true, Decimals: 6, DecimalsKnown: true, Verified: true},
			{Token: keyBubble, Symbol: "BUBBLE", Supply: 123890421048, Fungible: true},
			{Token: keyGnomic, Symbol: "GNOMIC", Supply: 20985103432232, Fungible: true,
				Decimals: 6, DecimalsKnown: true},
			{Token: keyPerun, Symbol: "PERUN", Supply: 1000000000000, Fungible: true},
			// An asset with no pool at all, which is 22 of the 28 on mainnet.
			{Token: "gno.land/r/x/y.GDOG.0000000", Symbol: "GDOG", Supply: 1048226, Fungible: true},
		},
		Pools: mainnetPools(),
		Anchor: &Anchor{USDPerGNOT: gnotUSD, SpreadPct: 0.02,
			Sources: []AnchorSource{{Venue: "Kraken", Pair: "GNOT/USD", USD: gnotUSD, Kind: "vwap-24h"}}},
		Depth: map[string][]DepthPoint{
			tokGNS:    ladder(0.45, 0.45, 0.46, 0.52),
			tokBubble: ladder(0.59, 1.84, 16.06, 69.93),
			tokPerun:  ladder(18.66, 68.65, 95.61, 99.54),
			tokGnomic: ladder(21.44, 71.32, 96.61, 99.66),
		},
		LedgerFrom: "2026-09-21",
		Now:        time.Date(2026, 9, 29, 17, 0, 0, 0, time.UTC),
	}
}

func quoteFor(t *testing.T, res *Result, token string) Quote {
	t.Helper()
	for _, q := range res.Quotes {
		if q.Token == token {
			return q
		}
	}
	t.Fatalf("no quote for %s", token)
	return Quote{}
}

func TestBuildAgainstMainnet(t *testing.T) {
	res := Build(mainnetInputs())

	if res.PoolCount != 5 {
		t.Fatalf("pool count = %d, want 5", res.PoolCount)
	}
	// Five of six assets reach wugnot; GDOG does not.
	if res.PricedCount != 5 {
		t.Fatalf("priced count = %d, want 5", res.PricedCount)
	}

	tests := []struct {
		name        string
		token       string
		wantTier    Tier
		wantPerTok  float64
		tol         float64
		wantFDV     float64
		fdvTol      float64
		wantRoutes  int
		wantWarning string
	}{
		// The measured per-token prices, assuming 6 decimals throughout.
		// Two routes, not one: GNS reaches wugnot directly and again through
		// PERUN, and the pair disagree by 0.32%.
		{name: "GNS is the only real market", token: keyGNS, wantTier: TierMarket,
			wantPerTok: 0.017608930, tol: 1e-6, wantFDV: 1903377, fdvTol: 2000, wantRoutes: 2,
			wantWarning: "route-disagreement"},
		{name: "wugnot is the anchor", token: keyWugnot, wantTier: TierMarket,
			wantPerTok: gnotUSD, tol: 1e-9},
		{name: "BUBBLE is thin", token: keyBubble, wantTier: TierThin,
			wantPerTok: 0.216365, tol: 1e-4, wantRoutes: 1, wantWarning: "decimals-unknown"},
		// The headline: a $582k paper value over a $47 pool.
		{name: "GNOMIC is decorative", token: keyGnomic, wantTier: TierDecorative,
			wantPerTok: 0.027734209, tol: 1e-6, wantFDV: 582003, fdvTol: 1000, wantRoutes: 1,
			wantWarning: "thin-pool"},
		// PERUN is the only token with two routes, and they disagree slightly.
		{name: "PERUN has two routes", token: keyPerun, wantTier: TierDecorative,
			wantPerTok: 0.074965, tol: 1e-3, wantRoutes: 2, wantWarning: "route-disagreement"},
		{name: "GDOG has no market", token: "gno.land/r/x/y.GDOG.0000000", wantTier: TierNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := quoteFor(t, res, tt.token)
			if q.Tier != tt.wantTier {
				t.Errorf("tier = %q, want %q", q.Tier, tt.wantTier)
			}
			if tt.wantPerTok != 0 && math.Abs(q.USDPerToken-tt.wantPerTok) > tt.tol {
				t.Errorf("usd per token = %.9g, want %.9g", q.USDPerToken, tt.wantPerTok)
			}
			if tt.wantFDV != 0 && math.Abs(q.FDVUSD-tt.wantFDV) > tt.fdvTol {
				t.Errorf("fdv = %.2f, want ~%.2f", q.FDVUSD, tt.wantFDV)
			}
			if tt.wantRoutes != 0 && q.RouteCount != tt.wantRoutes {
				t.Errorf("route count = %d, want %d", q.RouteCount, tt.wantRoutes)
			}
			if tt.wantWarning != "" && !hasWarning(q, tt.wantWarning) {
				t.Errorf("missing warning %q, got %v", tt.wantWarning, warningCodes(q))
			}
		})
	}
}

// The four caveats that are true of every price on this chain must be on every
// price on this chain, including the ones that look solid. GNS is the token a
// reader is most likely to trust, so it is the one most worth checking.
func TestUniversalWarningsAreOnEveryQuote(t *testing.T) {
	res := Build(mainnetInputs())
	universal := []string{"no-onchain-oracle", "twap-is-spot", "single-venue", "spot-not-average"}
	for _, q := range res.Quotes {
		if q.Tier == TierNone {
			continue
		}
		for _, code := range universal {
			if !hasWarning(q, code) {
				t.Errorf("%s: missing universal warning %q, got %v", q.Symbol, code, warningCodes(q))
			}
		}
	}
}

// Severity decides both the colour and the order, and the order is what a
// reader who does not expand anything actually sees. An alarm buried under four
// informational notes is an alarm nobody reads.
func TestWarningsAreOrderedBySeverity(t *testing.T) {
	res := Build(mainnetInputs())
	rank := map[Severity]int{SeverityAlarm: 0, SeverityCaution: 1, SeverityInfo: 2}
	for _, q := range res.Quotes {
		last := -1
		for _, w := range q.Warnings {
			r, ok := rank[w.Severity]
			if !ok {
				t.Fatalf("%s: warning %q has severity %q", q.Symbol, w.Code, w.Severity)
			}
			if r < last {
				t.Fatalf("%s: %q (%s) sorts after a lower-severity warning", q.Symbol, w.Code, w.Severity)
			}
			last = r
			if w.Short == "" || w.Explain == "" {
				t.Fatalf("%s: warning %q has an empty short or explain", q.Symbol, w.Code)
			}
		}
	}
}

// A verified token with known decimals must NOT collect the two warnings that
// exist to flag their absence. Without this, "warn about everything" passes
// every other test in this file while telling a reader nothing.
func TestWarningsAreNotUnconditional(t *testing.T) {
	res := Build(mainnetInputs())
	gns := quoteFor(t, res, keyGNS)
	for _, code := range []string{"decimals-unknown", "unverified-token"} {
		if hasWarning(gns, code) {
			t.Errorf("GNS should not carry %q: it is verified and has 6 decimals", code)
		}
	}
	// The deepest pool on the chain must not be badged "thin market". Its worst
	// rung is 0.52% at $10,000, which is a fee, not a warning.
	if hasWarning(gns, "thin-pool") {
		t.Error("GNS sits in a $759k pool and must not carry thin-pool")
	}
	// GNOMIC reaches wugnot exactly one way, so there is nothing for its routes
	// to disagree about.
	gnomic := quoteFor(t, res, keyGnomic)
	if hasWarning(gnomic, "route-disagreement") {
		t.Errorf("GNOMIC has a single route and must not carry route-disagreement")
	}
}

// A hair's-breadth gap between two routes is arithmetic, not news. Without a
// floor the warning fires on every multi-route token and stops being read.
func TestRouteDisagreementHasAFloor(t *testing.T) {
	q := Quote{}
	q.buildWarnings(warningInputs{RouteCount: 2, RouteSpreadPct: 0.01, DecimalsKnown: true, Verified: true})
	if hasWarning(q, "route-disagreement") {
		t.Error("a 0.01% gap should not warn")
	}
	q2 := Quote{}
	q2.buildWarnings(warningInputs{RouteCount: 2, RouteSpreadPct: 0.32, DecimalsKnown: true, Verified: true})
	if !hasWarning(q2, "route-disagreement") {
		t.Errorf("the measured GNS gap of 0.32%% should warn, got %v", warningCodes(q2))
	}
}

// An unpriced token gets one word and no paragraphs. Hanging seven caveats off
// an empty cell is how a warning list stops being read on the rows that need it.
func TestUnpricedTokensCarryNoWarnings(t *testing.T) {
	res := Build(mainnetInputs())
	q := quoteFor(t, res, "gno.land/r/x/y.GDOG.0000000")
	if q.Tier != TierNone {
		t.Fatalf("fixture changed: GDOG tier is %q", q.Tier)
	}
	if len(q.Warnings) != 0 {
		t.Fatalf("GDOG has no price and %d warnings: %v", len(q.Warnings), warningCodes(q))
	}
	if q.TierExplain == "" {
		t.Error("an unpriced token must still explain why it is unpriced")
	}
}

func TestBuildWithoutAnchorPricesNothing(t *testing.T) {
	in := mainnetInputs()
	in.Anchor = nil
	res := Build(in)
	if len(res.Quotes) != 0 {
		t.Fatalf("got %d quotes with no anchor, want 0", len(res.Quotes))
	}
	if res.PoolCount != 5 {
		t.Fatalf("pool count should survive a missing anchor, got %d", res.PoolCount)
	}
}

// The per-base-unit figure is the one that needs no assumption about the token,
// so it must be identical whatever decimals the caller claims. Only the
// per-token figure may move.
func TestBaseUnitPriceIgnoresDecimals(t *testing.T) {
	in := mainnetInputs()
	base := quoteFor(t, Build(in), keyGnomic)

	in2 := mainnetInputs()
	for i := range in2.Assets {
		if in2.Assets[i].Token == keyGnomic {
			in2.Assets[i].Decimals = 18
		}
	}
	changed := quoteFor(t, Build(in2), keyGnomic)

	if base.USDPerBaseUnit != changed.USDPerBaseUnit {
		t.Fatalf("base-unit price moved with decimals: %s vs %s", base.USDPerBaseUnit, changed.USDPerBaseUnit)
	}
	if math.Abs(changed.USDPerToken/base.USDPerToken-1e12) > 1e9 {
		t.Fatalf("per-token price should scale by 10^12, got %.6g vs %.6g", changed.USDPerToken, base.USDPerToken)
	}
}

func TestSlippagePct(t *testing.T) {
	tests := []struct {
		name       string
		got, ideal int64
		want       float64
	}{
		{"no slippage", 100, 100, 0},
		{"one percent", 99, 100, 1},
		// GNS at $1: 3,997,354 received against 4,015,405 promised.
		{"measured GNS rung", 3997354, 4015405, 0.4495},
		{"router refused", 0, 100, 100},
		// A better-than-spot fill is not negative slippage worth printing, it is
		// rounding, and a negative percentage in a warning reads as a bug.
		{"better than spot clamps to zero", 101, 100, 0},
		{"zero ideal", 5, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slippagePct(new(big.Rat).SetInt64(tt.got), new(big.Rat).SetInt64(tt.ideal))
			if math.Abs(got-tt.want) > 0.001 {
				t.Fatalf("slippage = %.4f, want %.4f", got, tt.want)
			}
		})
	}
}

// The normalizer between the two key spaces. Every case here is a real mainnet
// shape: the ordinary triple, a factory's non-zero id, a realm path with dots in
// it, and the two tokens that emit a bare symbol with no realm at all.
func TestPoolToken(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ordinary event key", "gno.land/r/gnoswap/gns.GNS.0000000", "gno.land/r/gnoswap/gns.GNS"},
		{"factory, non-zero id", "gno.land/r/demo/defi/grc20factory.PERUN.0000001", "gno.land/r/demo/defi/grc20factory.PERUN"},
		{"nested realm path", "gno.land/r/g1n4pl5uc4yt5r96m9w6fmdznx3x0jyg8l6arhmt/gnomi/padv3.GNOVA.0000008",
			"gno.land/r/g1n4pl5uc4yt5r96m9w6fmdznx3x0jyg8l6arhmt/gnomi/padv3.GNOVA"},
		// Two live mainnet tokens put a bare symbol in the attribute, with no
		// realm path and therefore no id to strip.
		{"bare symbol", "COVID", "COVID"},
		// Already in pool-token space: idempotent, because the wugnot constant
		// and a discovered pool's tokens both arrive this way.
		{"already a pool token", "gno.land/r/gnoswap/gns.GNS", "gno.land/r/gnoswap/gns.GNS"},
		{"anchor constant", WUGNOT, WUGNOT},
		{"trailing dot", "gno.land/r/x/y.Z.", "gno.land/r/x/y.Z."},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PoolToken(tt.in); got != tt.want {
				t.Fatalf("PoolToken(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPoolKeyPath(t *testing.T) {
	k := PoolKey{Token0: WUGNOT, Token1: tokGNS, Fee: 3000}
	want := "gno.land/r/gnoland/wugnot.wugnot:gno.land/r/gnoswap/gns.GNS:3000"
	if k.Path() != want {
		t.Fatalf("path = %q, want %q", k.Path(), want)
	}
}

// DiscoverPools and QuoteOut both compose an expression and then read one very
// specific shape back. A fake Eval is enough to pin both, and pins the
// GNOSWAP-ROUTER-014 trap in particular: the route's first token must be the
// input token, not whatever order the pool stores.
func TestQuoteOutUsesDirectionalRoute(t *testing.T) {
	var seen string
	eval := func(_ context.Context, expr string) (string, error) {
		seen = expr
		return `("1000000" string)` + "\n" + `("3997354" string)` + "\n" + `(undefined)`, nil
	}
	got, ok := QuoteOut(context.Background(), eval, WUGNOT, tokGNS, 3000, 1000000)
	if !ok || got != 3997354 {
		t.Fatalf("QuoteOut = %d,%v want 3997354,true", got, ok)
	}
	wantRoute := `"` + WUGNOT + ":" + tokGNS + `:3000"`
	if !strings.Contains(seen, wantRoute) {
		t.Fatalf("route must start at the input token; expression was %s", seen)
	}
}

func TestQuoteOutRejectsRouterError(t *testing.T) {
	eval := func(_ context.Context, _ string) (string, error) {
		return `("0" string)` + "\n" + `("0" string)` + "\n" +
			`(&(struct{("[GNOSWAP-ROUTER-014] invalid route first token" string)} gno.land/p/nt/ufmt/v0.errMsg) *gno.land/p/nt/ufmt/v0.errMsg)`, nil
	}
	if _, ok := QuoteOut(context.Background(), eval, WUGNOT, tokGNS, 3000, 1000000); ok {
		t.Fatal("a router error must not read as a successful zero quote")
	}
}

func TestDiscoverPoolsProbesBothOrders(t *testing.T) {
	// The pool stores wugnot first; a probe that only tried the other order
	// would see a chain with no liquidity at all.
	live := PoolKey{Token0: WUGNOT, Token1: tokGNS, Fee: 3000}
	eval := func(_ context.Context, expr string) (string, error) {
		if strings.Contains(expr, live.Path()) {
			return `(true bool)`, nil
		}
		return `(false bool)`, nil
	}
	found, err := DiscoverPools(context.Background(), eval, WUGNOT, []string{tokGNS, WUGNOT}, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Path() != live.Path() {
		t.Fatalf("found %v, want exactly %s", found, live.Path())
	}
}

// The walk has to reach a token that does not touch the anchor directly, or
// PERUN (which reaches wugnot through GNS as well as directly) would lose half
// its routes and the only cross-check the chain offers for free.
func TestDiscoverPoolsWalksPastTheFirstLayer(t *testing.T) {
	direct := PoolKey{Token0: WUGNOT, Token1: tokGNS, Fee: 3000}
	secondHop := PoolKey{Token0: tokPerun, Token1: tokGNS, Fee: 10000}
	live := map[string]bool{direct.Path(): true, secondHop.Path(): true}
	var probes int
	var mu sync.Mutex
	eval := func(_ context.Context, expr string) (string, error) {
		mu.Lock()
		probes++
		mu.Unlock()
		for p := range live {
			if strings.Contains(expr, p) {
				return `(true bool)`, nil
			}
		}
		return `(false bool)`, nil
	}
	found, err := DiscoverPools(context.Background(), eval, WUGNOT,
		[]string{WUGNOT, tokGNS, tokPerun, tokBubble, tokGnomic}, 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("found %d pools, want 2: %v", len(found), found)
	}
	// The whole point of walking rather than gridding: the full grid over five
	// tokens at four tiers in both orders is 80 probes, and the walk must do
	// materially fewer while finding everything reachable.
	if probes >= 80 {
		t.Errorf("walk made %d probes, no cheaper than the full grid (80)", probes)
	}
}

// The mainnet topology, which is the case an obvious walk gets wrong.
//
// wugnot reaches BUBBLE, GNOMIC, GNS and PERUN in one layer. PERUN/GNS joins
// two tokens the anchor has ALREADY reached separately, so a walk that skips a
// token once it is reached never probes that pair and finds 4 of the 5 pools.
// Measured against mainnet on 2026-09-29, where the first version of
// DiscoverPools did exactly that and silently cost PERUN and GNS their second
// route, which is the only price cross-check this chain offers.
func TestDiscoverPoolsFindsPoolsBetweenAlreadyReachedTokens(t *testing.T) {
	live := map[string]bool{}
	for _, p := range mainnetPools() {
		live[p.Key.Path()] = true
	}
	eval := func(_ context.Context, expr string) (string, error) {
		for p := range live {
			if strings.Contains(expr, p) {
				return `(true bool)`, nil
			}
		}
		return `(false bool)`, nil
	}
	found, err := DiscoverPools(context.Background(), eval, WUGNOT,
		[]string{WUGNOT, tokGNS, tokBubble, tokGnomic, tokPerun}, 3, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != len(live) {
		var paths []string
		for _, k := range found {
			paths = append(paths, k.Path())
		}
		t.Fatalf("found %d of %d pools: %v", len(found), len(live), paths)
	}
	// Name the one that goes missing, so a failure says which case broke.
	perunGNS := PoolKey{Token0: tokPerun, Token1: tokGNS, Fee: 10000}.Path()
	var got bool
	for _, k := range found {
		if k.Path() == perunGNS {
			got = true
		}
	}
	if !got {
		t.Errorf("missing %s, the pool joining two tokens the anchor already reached", perunGNS)
	}
}

// A discovery that runs out of time keeps what it found. Returning nothing
// would silently reprice every token on the chain to "no market", which reads
// as a fact about the chain rather than about the clock.
func TestDiscoverPoolsKeepsPartialResultsOnCancel(t *testing.T) {
	live := PoolKey{Token0: WUGNOT, Token1: tokGNS, Fee: 3000}
	ctx, cancel := context.WithCancel(context.Background())
	eval := func(_ context.Context, expr string) (string, error) {
		if strings.Contains(expr, live.Path()) {
			return `(true bool)`, nil
		}
		return `(false bool)`, nil
	}
	found, err := DiscoverPools(ctx, eval, WUGNOT, []string{WUGNOT, tokGNS, tokPerun}, 3, 4)
	if err != nil || len(found) != 1 {
		t.Fatalf("baseline: found %v err %v", found, err)
	}
	cancel()
	found, err = DiscoverPools(ctx, eval, WUGNOT, []string{WUGNOT, tokGNS, tokPerun}, 3, 4)
	if len(found) != 0 || err == nil {
		t.Fatalf("a cancelled walk with nothing found must report the cancellation: %v %v", found, err)
	}
}

func hasWarning(q Quote, code string) bool {
	for _, w := range q.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func warningCodes(q Quote) []string {
	out := make([]string, 0, len(q.Warnings))
	for _, w := range q.Warnings {
		out = append(out, w.Code)
	}
	return out
}
