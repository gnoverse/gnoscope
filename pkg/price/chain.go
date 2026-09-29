package price

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WUGNOT is the anchor token: the only GRC20 whose USD value is knowable from
// outside the chain, because it wraps the coin that trades on real exchanges.
// Every route this package builds ends here.
const WUGNOT = "gno.land/r/gnoland/wugnot.wugnot"

// ugnotDecimals is a chain constant, not a token's claim about itself. It is
// the reason the USD anchor is trustworthy in a way no other token's decimals
// are: 1 GNOT is 1,000,000 ugnot by the chain's own definition, so converting
// the exchange price into a per-base-unit price introduces no assumption.
const ugnotDecimals = 6

// poolRealm is GnoSwap's unversioned pool facade. Unversioned on purpose: the
// versioned implementations (pool/v1 and successors) are swapped behind a proxy
// and reading the facade survives that.
const poolRealm = "gno.land/r/gnoswap/pool"

// routerRealm is the quote source. DrySwapRoute takes no `cur realm` parameter,
// so it is a plain read and answers over vm/qeval with no key and no gas.
const routerRealm = "gno.land/r/gnoswap/router"

// feeTiers are the four GnoSwap supports, read from its own pool render on
// 2026-09-29: 100 / 500 / 3000 / 10000 pips, tick spacing 1 / 10 / 60 / 200.
var feeTiers = []uint32{100, 500, 3000, 10000}

// depthLadder is the set of notionals the slippage measurement walks. Four
// rungs two orders of magnitude apart, because the interesting differences
// between gno.land's pools span exactly that range: GNS is flat across all four
// and GNOMIC has already lost a fifth of your money at the first.
var depthLadder = []int{10, 100, 1000, 10000}

// Eval runs one read-only expression against a chain and returns Gno's debug
// repr. Injected rather than imported so this package stays testable without a
// node, and so the caller keeps ownership of timeouts and connection pooling.
type Eval func(ctx context.Context, expr string) (string, error)

// PoolKey identifies a GnoSwap pool. Token0 and Token1 are event keys
// (`<realm path>.<name>`), in the order the pool itself stores them, which is
// not alphabetical and not the order they were passed to CreatePool.
type PoolKey struct {
	Token0 string
	Token1 string
	Fee    uint32
}

// Path is the identifier every pool getter takes.
func (k PoolKey) Path() string {
	return k.Token0 + ":" + k.Token1 + ":" + strconv.FormatUint(uint64(k.Fee), 10)
}

// Pool is one pool's live state.
type Pool struct {
	Key PoolKey
	// SqrtPriceX96 is kept as the string the chain returned. Parsing it into
	// anything narrower loses digits, and the raw value is what a reader would
	// need to check the arithmetic.
	SqrtPriceX96 string
	Tick         int32
	Reserve0     int64
	Reserve1     int64
	// ObservationCardinality is read only so the page can prove the claim it
	// makes about the TWAP rather than assert it. One, on every pool, every
	// time it has been measured.
	ObservationCardinality int
}

// Ratio is base units of Token1 per base unit of Token0.
func (p *Pool) Ratio() (*big.Rat, error) { return RatioFromSqrtPriceX96(p.SqrtPriceX96) }

// DiscoverPools finds the pools that can price something, by walking outward
// from the anchor.
//
// There is no enumeration endpoint. `pool.GetPools()` returns a read-only tree
// whose contents vm/qeval cannot iterate (it can only print the tree's own
// debug repr), CreatePool arguments are not kept by this indexer, and the pool
// realm's Render prints a count and not a list. So discovery is a probe:
// ExistsPoolPath, one read per candidate.
//
// The first version probed the full grid, every unordered pair at every fee
// tier. That is O(n^2 * 8) reads, and it does not survive contact with a real
// host: 15 fungible assets is 840 probes, which took **the entire 60-second
// refresh budget** on val1 on 2026-09-29 and left nothing for the anchor, so
// the endpoint answered "no GNOT/USD venue answered" when the truth was "the
// deadline expired before anyone asked one".
//
// This walks outward from `seed` instead. Layer 1 probes seed against every
// candidate; each token found becomes a frontier for the next layer. Two things
// fall out of that:
//
//   - **It is much cheaper.** The first layer is O(n * 8), 120 reads rather than
//     840, and on a chain with five pools it converges in two layers.
//   - **It finds exactly the pools that matter.** A price is a route to wugnot,
//     so a pool between two tokens that neither connects to the anchor cannot
//     price anything. The grid version paid for those and then ignored them.
//
// maxLayers bounds the walk; 3 matches the route solver's hop limit, past which
// a price is more pool depth than signal anyway. Order matters to the chain and
// not to us: a pool stores its tokens in one specific order and ExistsPoolPath
// is false for the other, so both are probed.
//
// Measured on mainnet 2026-09-29: finds all five pools, and the walk stops
// after the layer that reaches nothing new.
func DiscoverPools(ctx context.Context, eval Eval, seed string, tokens []string, maxLayers, concurrency int) ([]PoolKey, error) {
	if concurrency <= 0 {
		concurrency = 8
	}
	if maxLayers <= 0 {
		maxLayers = 3
	}

	rest := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if t != seed {
			rest = append(rest, t)
		}
	}

	var (
		mu       sync.Mutex
		found    []PoolKey
		seenPool = map[string]bool{}
		probed   = map[string]bool{}
		reached  = map[string]bool{seed: true}
		frontier = []string{seed}
	)

	for layer := 0; layer < maxLayers && len(frontier) > 0; layer++ {
		var candidates []PoolKey
		for _, from := range frontier {
			for _, to := range rest {
				if from == to {
					continue
				}
				// Deliberately NOT skipping a token already reached. That skip
				// was the first version of this walk and it silently lost a
				// pool: on mainnet 2026-09-29 it found 4 of 5, because
				// PERUN/GNS joins two tokens the anchor had already reached
				// separately. Those are precisely the pools that give a token a
				// second independent route, which is the only price
				// cross-check this chain offers for free. The pair ledger below
				// is what stops the duplicate work instead.
				for _, fee := range feeTiers {
					for _, k := range []PoolKey{
						{Token0: from, Token1: to, Fee: fee},
						{Token0: to, Token1: from, Fee: fee},
					} {
						if probed[k.Path()] {
							continue
						}
						probed[k.Path()] = true
						candidates = append(candidates, k)
					}
				}
			}
		}
		if len(candidates) == 0 {
			break
		}

		var wg sync.WaitGroup
		sem := make(chan struct{}, concurrency)
		for _, k := range candidates {
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(k PoolKey) {
				defer wg.Done()
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				out, err := eval(ctx, fmt.Sprintf(`%s.ExistsPoolPath("%s")`, poolRealm, k.Path()))
				if err != nil {
					return
				}
				lines := ReprLines(out)
				if len(lines) == 0 {
					return
				}
				if ok, parsed := ReprBool(lines[0]); !parsed || !ok {
					return
				}
				mu.Lock()
				if !seenPool[k.Path()] {
					seenPool[k.Path()] = true
					found = append(found, k)
				}
				mu.Unlock()
			}(k)
		}
		wg.Wait()

		// A cancelled walk keeps what it found. A partial pool set prices fewer
		// tokens, which the response already knows how to say; returning
		// nothing would reprice every token on the chain to "no market".
		if ctx.Err() != nil {
			break
		}

		var next []string
		mu.Lock()
		for _, k := range found {
			for _, t := range []string{k.Token0, k.Token1} {
				if !reached[t] {
					reached[t] = true
					next = append(next, t)
				}
			}
		}
		mu.Unlock()
		frontier = next
	}

	sort.Slice(found, func(i, j int) bool { return found[i].Path() < found[j].Path() })
	if len(found) == 0 && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return found, nil
}

// LoadPool reads one pool's live state.
//
// Four reads rather than one, because GnoSwap exposes no single call that
// returns all of it: GetSlot0 does return a struct with the sqrt price and the
// cardinality in it, but as a nested debug repr of a uint256 held as four
// uint64 limbs, which is a parser nobody should have to maintain. The typed
// getters cost three extra round trips over a pooled connection and cannot be
// misread.
func LoadPool(ctx context.Context, eval Eval, k PoolKey) (*Pool, error) {
	p := &Pool{Key: k}

	out, err := eval(ctx, fmt.Sprintf(`%s.GetSlot0SqrtPriceX96("%s")`, poolRealm, k.Path()))
	if err != nil {
		return nil, err
	}
	if ReprErrored(out) {
		return nil, fmt.Errorf("pool %s: %s", k.Path(), firstLine(out))
	}
	lines := ReprLines(out)
	if len(lines) == 0 {
		return nil, fmt.Errorf("pool %s: empty sqrtPriceX96 response", k.Path())
	}
	sq, ok := ReprString(lines[0])
	if !ok || sq == "" {
		return nil, fmt.Errorf("pool %s: unparseable sqrtPriceX96 %q", k.Path(), lines[0])
	}
	p.SqrtPriceX96 = sq

	if out, err := eval(ctx, fmt.Sprintf(`%s.GetSlot0Tick("%s")`, poolRealm, k.Path())); err == nil {
		if lines := ReprLines(out); len(lines) > 0 {
			if v, ok := ReprInt(lines[0]); ok {
				p.Tick = int32(v)
			}
		}
	}

	if out, err := eval(ctx, fmt.Sprintf(`%s.GetBalances("%s")`, poolRealm, k.Path())); err == nil {
		lines := ReprLines(out)
		if len(lines) >= 2 {
			if v, ok := ReprInt(lines[0]); ok {
				p.Reserve0 = v
			}
			if v, ok := ReprInt(lines[1]); ok {
				p.Reserve1 = v
			}
		}
	}

	// Cardinality is read last and its failure is not fatal: a pool with an
	// unreadable observation buffer is still a priceable pool, it just cannot
	// prove the TWAP claim for itself. The warning is unconditional anyway.
	if out, err := eval(ctx, fmt.Sprintf(`%s.GetSlot0("%s")`, poolRealm, k.Path())); err == nil {
		p.ObservationCardinality = cardinalityFromSlot0(out)
	}
	return p, nil
}

// cardinalityFromSlot0 pulls observationCardinality out of the Slot0 struct repr.
//
// The struct ends with three uint16 fields in order: observationIndex,
// observationCardinality, observationCardinalityNext. Matching on the tail
// rather than parsing the whole struct, because the head of it is a uint256
// printed as a four-limb array and the middle carries an int32 and a uint8 that
// a positional scan would have to count past correctly forever.
func cardinalityFromSlot0(out string) int {
	fields := slot0Uint16.FindAllStringSubmatch(out, -1)
	if len(fields) < 3 {
		return 0
	}
	v, err := strconv.Atoi(fields[len(fields)-2][1])
	if err != nil {
		return 0
	}
	return v
}

// slot0Uint16 matches the "(N uint16)" groups inside a Slot0 repr. Its own
// pattern rather than ReprInt's, because ReprInt anchors to a whole line and
// these three are commas apart inside one.
var slot0Uint16 = regexp.MustCompile(`\((\d+) uint16\)`)

// QuoteOut asks the router what a given input actually buys, right now.
//
// This is the number that decides a tier, and the reason the tier is credible:
// it is GnoSwap's own execution path answering, not a curve this package
// modelled. Concentrated liquidity makes the modelled version wrong in a way
// that depends on where the ticks sit, which no reserve total reveals.
//
// The trap, and it cost an entire wrong measurement table: `routeArr` is not
// the pool path. It is an ordered route whose FIRST token must equal
// inputToken, so the direction matters and the pool's own stored order is
// irrelevant. Passing the pool path where the route runs the other way returns
// ("0","0") plus [GNOSWAP-ROUTER-014] rather than an error the caller notices,
// which reads as 100% slippage and quietly buries a healthy pool.
func QuoteOut(ctx context.Context, eval Eval, in, out string, fee uint32, amountIn int64) (int64, bool) {
	route := fmt.Sprintf("%s:%s:%d", in, out, fee)
	expr := fmt.Sprintf(`%s.DrySwapRoute("%s","%s","%d","EXACT_IN","%s","100","1")`,
		routerRealm, in, out, amountIn, route)
	resp, err := eval(ctx, expr)
	if err != nil {
		return 0, false
	}
	if ReprErrored(resp) {
		return 0, false
	}
	lines := ReprLines(resp)
	if len(lines) < 2 {
		return 0, false
	}
	s, ok := ReprString(lines[1])
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// MeasureDepth walks the ladder for one hop, buying `token` with wugnot.
//
// Always in the buy direction, and always from wugnot, because that is the
// trade a reader of this page is imagining when they look at a price. The sell
// side of a concentrated pool can be very differently shaped, which is worth
// exposing one day and is not what a tier is for.
func MeasureDepth(ctx context.Context, eval Eval, pool *Pool, token string, usdPerGNOT float64) []DepthPoint {
	if usdPerGNOT <= 0 || pool == nil {
		return nil
	}
	ratio, err := pool.Ratio()
	if err != nil {
		return nil
	}
	// spot is base units of `token` per base unit of wugnot.
	var spot *big.Rat
	switch {
	case pool.Key.Token0 == WUGNOT && pool.Key.Token1 == token:
		spot = ratio
	case pool.Key.Token1 == WUGNOT && pool.Key.Token0 == token:
		spot = new(big.Rat).Inv(ratio)
	default:
		return nil
	}

	points := make([]DepthPoint, 0, len(depthLadder))
	for _, usdNotional := range depthLadder {
		ugnot := int64(float64(usdNotional) / usdPerGNOT * 1e6)
		if ugnot <= 0 {
			continue
		}
		got, ok := QuoteOut(ctx, eval, WUGNOT, token, pool.Key.Fee, ugnot)
		if !ok {
			points = append(points, DepthPoint{NotionalUSD: usdNotional, SlippagePct: 100, Quoted: false})
			continue
		}
		ideal := new(big.Rat).Mul(new(big.Rat).SetInt64(ugnot), spot)
		points = append(points, DepthPoint{
			NotionalUSD: usdNotional,
			SlippagePct: slippagePct(new(big.Rat).SetInt64(got), ideal),
			Quoted:      true,
		})
	}
	return points
}

// Graph is the priced view of the chain: pools indexed by the token they touch.
type Graph struct {
	Pools []*Pool
	byTok map[string][]*Pool
}

// NewGraph indexes pools by token.
func NewGraph(pools []*Pool) *Graph {
	g := &Graph{Pools: pools, byTok: map[string][]*Pool{}}
	for _, p := range pools {
		g.byTok[p.Key.Token0] = append(g.byTok[p.Key.Token0], p)
		g.byTok[p.Key.Token1] = append(g.byTok[p.Key.Token1], p)
	}
	return g
}

// route is one path from a token back to wugnot, with the price it implies.
type route struct {
	hops []*Pool
	// usdPerBase is USD per base unit of the token this route prices.
	usdPerBase *big.Rat
	// minTVL is the shallowest pool on the route, in USD. A route is only as
	// trustworthy as its weakest hop, so this and not the total is what ranks
	// routes and what sets a tier.
	minTVL float64
}

// Routes enumerates every simple path from token to wugnot, up to maxHops.
//
// Exhaustive rather than shortest-first, because the second route is not a
// fallback here: it is the only cross-check this chain offers. PERUN reaches
// wugnot directly and via GNS, and the two disagree by 0.32% (measured
// 2026-09-29). That disagreement is a fact about the market worth printing, and
// a shortest-path search would have thrown it away.
//
// maxHops of 3 is not a tuning parameter so much as an observation: with five
// pools there is nothing longer to find, and each extra hop multiplies the
// uncertainty by another pool's depth.
func (g *Graph) Routes(token string, maxHops int, usd map[string]*big.Rat, tvl map[string]float64) []route {
	if token == WUGNOT {
		return nil
	}
	var out []route
	var walk func(cur string, hops []*Pool, seen map[string]bool)
	walk = func(cur string, hops []*Pool, seen map[string]bool) {
		if len(hops) > maxHops {
			return
		}
		for _, p := range g.byTok[cur] {
			if seen[p.Key.Path()] {
				continue
			}
			next := p.Key.Token0
			if next == cur {
				next = p.Key.Token1
			}
			seen[p.Key.Path()] = true
			hops2 := append(append([]*Pool{}, hops...), p)
			if next == WUGNOT {
				out = append(out, route{hops: hops2})
			} else if len(hops2) < maxHops {
				walk(next, hops2, seen)
			}
			delete(seen, p.Key.Path())
		}
	}
	walk(token, nil, map[string]bool{})

	// Price each route by walking it backwards from the anchor.
	anchorUSD := usd[WUGNOT]
	priced := out[:0]
	for _, r := range out {
		cur := WUGNOT
		px := new(big.Rat).Set(anchorUSD)
		minTVL := 0.0
		ok := true
		for i := len(r.hops) - 1; i >= 0; i-- {
			p := r.hops[i]
			ratio, err := p.Ratio()
			if err != nil {
				ok = false
				break
			}
			var other string
			switch cur {
			case p.Key.Token0:
				// ratio is token1 per token0, so token0's price is token1's
				// price times the ratio.
				other = p.Key.Token1
				px = new(big.Rat).Quo(px, ratio)
			case p.Key.Token1:
				other = p.Key.Token0
				px = new(big.Rat).Mul(px, ratio)
			default:
				ok = false
			}
			if !ok {
				break
			}
			if t := tvl[p.Key.Path()]; t > 0 && (minTVL == 0 || t < minTVL) {
				minTVL = t
			}
			cur = other
		}
		if !ok || cur != token {
			continue
		}
		r.usdPerBase = px
		r.minTVL = minTVL
		priced = append(priced, r)
	}
	// Deepest weakest-hop first: the best route is the one whose worst pool is
	// the least bad.
	sort.SliceStable(priced, func(i, j int) bool { return priced[i].minTVL > priced[j].minTVL })
	return priced
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ugnotUSDRat turns the exchange price of one whole GNOT into USD per base unit.
func ugnotUSDRat(usdPerGNOT float64) *big.Rat {
	r := new(big.Rat).SetFloat64(usdPerGNOT)
	if r == nil {
		return new(big.Rat)
	}
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(ugnotDecimals), nil)
	return new(big.Rat).Quo(r, new(big.Rat).SetInt(den))
}

var _ = time.Now
