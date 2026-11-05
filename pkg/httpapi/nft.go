package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gnoverse/gnoscope/pkg/price"
)

// One GRC721 item, read live from the realm that issued it (#405).
//
// The ledger knows a collection and a count and nothing else: a GRC721 rides
// the GRC20 Transfer event, so what moved is "one GNFT" and never which one.
// The item itself only exists behind the realm, and for the collection that
// carries most of the value on mainnet, the GnoSwap position NFT, the id *is*
// the whole object: a pool, a price band, liquidity and fees, none of which an
// id on its own says.
//
// So this is a read-through and not an index. Every figure is a vm/qeval
// against the chain, which records no reads, and nothing is stored.
//
// Two traps the GnoSwap reader exists to avoid, both measured on mainnet on
// 2026-11-05 against position 1:
//
//   - **A staked position is owned by the staker realm.** OwnerOf answers
//     g1q6d4ns7zkr492rgl0pcgf5ajaf2dlz0nnptky3 for every staked LP, so taken
//     literally they all belong to one address. The depositor is
//     staker.GetDepositOwner, and that is the owner this reports; the realm is
//     kept beside it as the custodian.
//   - **Out of range earns nothing.** That is the single most important fact
//     about a position and the id hides it, so IsInRange is read and reported
//     on its own rather than left for a reader to work out from ticks.
const (
	gnoswapNFT      = "gno.land/r/gnoswap/gnft"
	gnoswapPosition = "gno.land/r/gnoswap/position"
	gnoswapStaker   = "gno.land/r/gnoswap/staker"
	gnoswapPool     = "gno.land/r/gnoswap/pool"
)

// nftReadTimeout bounds one item's reads. They run in parallel, each a
// local-state abci_query, so this is a ceiling for a sick node rather than a
// budget anything healthy approaches.
const nftReadTimeout = 15 * time.Second

// nftItemTTL is how long one item's answer is served from memory. A position's
// fees move every swap, so this is short; it exists so a reader refreshing the
// page does not cost the node another dozen reads.
const nftItemTTL = 30 * time.Second

var nftCache = struct {
	sync.Mutex
	items map[string]nftCached
}{items: map[string]nftCached{}}

type nftCached struct {
	item *NFTItem
	at   time.Time
}

// The collection and the id are spliced into a qeval expression, so both are
// held to a shape that cannot close the string or the call they sit in. A
// realm path is what pkg_path already is everywhere else; a token id is what
// GRC721 calls a TokenID, a string, and every collection on mainnet uses
// digits. Anything outside these is refused rather than escaped.
var (
	nftCollectionRe = regexp.MustCompile(`^gno\.land/r/[a-z0-9_/]+$`)
	nftIDRe         = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	reprQuoted      = regexp.MustCompile(`^\("((?:[^"\\]|\\.)*)" [^)]+\)$`)
	reprAnyQuoted   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// NFTItem is one item of one collection. Every field past Collection and ID is
// optional: a collection that does not implement a getter simply has no value
// for it, which is not an error.
type NFTItem struct {
	Network    string `json:"network"`
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Symbol     string `json:"symbol,omitempty"`
	// Owner is who the item belongs to. For a staked GnoSwap position that is
	// the depositor, not the realm holding it; Custodian says who holds it.
	Owner     string `json:"owner,omitempty"`
	Custodian string `json:"custodian,omitempty"`
	TokenURI  string `json:"token_uri,omitempty"`
	// Kind names the renderer: "gnoswap-position", or "grc721" for the
	// generic view.
	Kind     string           `json:"kind"`
	Position *GnoswapPosition `json:"position,omitempty"`
	// Errors are the reads that failed, by getter. A missing figure with a
	// reason beside it is a different thing to say than a missing figure.
	Errors map[string]string `json:"errors,omitempty"`
	ReadAt time.Time         `json:"read_at"`
}

// GnoswapPosition is a GnoSwap concentrated-liquidity position.
//
// Amounts are base units, as the chain returns them. Prices are base units of
// Token1 per base unit of Token0, 1.0001^tick, and PriceScaled is the same
// figure in whole tokens where both sides' decimals are curated.
type GnoswapPosition struct {
	Pool          string `json:"pool"`
	Token0        string `json:"token0"`
	Token1        string `json:"token1"`
	Token0Symbol  string `json:"token0_symbol"`
	Token1Symbol  string `json:"token1_symbol"`
	Token0Decimal int    `json:"token0_decimals,omitempty"`
	Token1Decimal int    `json:"token1_decimals,omitempty"`
	// Fee is the tier in hundredths of a basis point, as GnoSwap keys it:
	// 3000 is 0.3%.
	Fee         uint32  `json:"fee"`
	TickLower   int32   `json:"tick_lower"`
	TickUpper   int32   `json:"tick_upper"`
	TickCurrent *int32  `json:"tick_current,omitempty"`
	PriceLower  float64 `json:"price_lower"`
	PriceUpper  float64 `json:"price_upper"`
	PriceNow    float64 `json:"price_current,omitempty"`
	// PriceScale multiplies a base-unit price into whole tokens, and is 0 when
	// either side's decimals are not curated.
	PriceScale float64 `json:"price_scale,omitempty"`
	Liquidity  string  `json:"liquidity"`
	InRange    *bool   `json:"in_range,omitempty"`
	Burned     bool    `json:"burned"`
	Amount0    *int64  `json:"amount0,omitempty"`
	Amount1    *int64  `json:"amount1,omitempty"`
	Fee0       string  `json:"unclaimed_fee0,omitempty"`
	Fee1       string  `json:"unclaimed_fee1,omitempty"`
	Staked     bool    `json:"staked"`
	// Reward is the GNS emission the depositor could claim now, base units.
	Reward *int64 `json:"claimable_reward,omitempty"`
}

// HandleNFT answers GET /api/nft/{collection...}?id=N.
func (a *API) HandleNFT(w http.ResponseWriter, r *http.Request) {
	network := a.singleNetwork(r)
	if network == "" {
		jsonError(w, "no network configured", 404)
		return
	}
	collection, err := url.PathUnescape(r.PathValue("collection"))
	if err != nil || !nftCollectionRe.MatchString(collection) {
		jsonError(w, "collection must be a realm path, gno.land/r/...", 400)
		return
	}
	id := r.URL.Query().Get("id")
	if !nftIDRe.MatchString(id) {
		jsonError(w, "id must be 1-64 letters, digits or _.:-", 400)
		return
	}
	rpcURL := a.rpcURLFor(network)
	if rpcURL == "" {
		jsonError(w, "no verified RPC endpoint for network "+network, 503)
		return
	}

	key := network + "|" + collection + "|" + id
	nftCache.Lock()
	c, ok := nftCache.items[key]
	nftCache.Unlock()
	if ok && time.Since(c.at) < nftItemTTL {
		writeJSON(w, c.item)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), nftReadTimeout)
	defer cancel()
	eval := func(ctx context.Context, expr string) (string, error) {
		return fetchABCIQuery(ctx, rpcURL, "vm/qeval", expr)
	}
	item, err := readNFT(ctx, eval, collection, id, a.tokenDecimals)
	if err != nil {
		jsonError(w, err.Error(), 404)
		return
	}
	item.Network = network

	nftCache.Lock()
	nftCache.items[key] = nftCached{item: item, at: time.Now()}
	for k, v := range nftCache.items {
		if time.Since(v.at) > nftItemTTL {
			delete(nftCache.items, k)
		}
	}
	nftCache.Unlock()
	writeJSON(w, item)
}

// tokenDecimals finds the curated symbol and decimals for a pool-path token
// (`<realm>.<name>`), which the registry keys by event key
// (`<realm>.<name>.<id>`).
func (a *API) tokenDecimals(poolToken string) (string, int) {
	if a.registry != nil {
		for k, t := range a.registry.Tokens {
			if price.PoolToken(k) == poolToken {
				return t.Symbol, t.Decimals
			}
		}
	}
	return "", 0
}

// readNFT reads one item. The only error is "there is no such item": every
// other failed read is recorded on the item and the rest of it still answers.
func readNFT(ctx context.Context, eval price.Eval, collection, id string, meta func(string) (string, int)) (*NFTItem, error) {
	item := &NFTItem{Collection: collection, ID: id, Kind: "grc721", ReadAt: time.Now().UTC()}
	var mu sync.Mutex
	fail := func(getter string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if item.Errors == nil {
			item.Errors = map[string]string{}
		}
		item.Errors[getter] = err.Error()
	}
	tid := strconv.Quote(id)

	// OwnerOf first and alone: it is the one read that says whether the item
	// exists, and a dozen parallel reads of an id that does not is a dozen
	// error strings for one fact.
	owner, err := qevalText(ctx, eval, fmt.Sprintf("%s.OwnerOf(%s)", collection, tid))
	if err != nil {
		return nil, fmt.Errorf("%s has no item %s: %w", collection, id, err)
	}
	item.Owner = owner

	var wg sync.WaitGroup
	text := func(getter, expr string, into *string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := qevalText(ctx, eval, expr)
			if err != nil {
				fail(getter, err)
				return
			}
			mu.Lock()
			*into = v
			mu.Unlock()
		}()
	}
	text("Name", collection+".Name()", &item.Name)
	text("Symbol", collection+".Symbol()", &item.Symbol)
	text("TokenURI", fmt.Sprintf("%s.TokenURI(%s)", collection, tid), &item.TokenURI)

	if collection == gnoswapNFT {
		if n, err := strconv.ParseUint(id, 10, 64); err == nil {
			item.Kind = "gnoswap-position"
			item.Position = readGnoswapPosition(ctx, eval, n, item, &mu, fail, meta)
		}
	}
	wg.Wait()
	return item, nil
}

// readGnoswapPosition reads the position behind a GNFT. Errors land on the item
// through fail; what answered is returned.
func readGnoswapPosition(ctx context.Context, eval price.Eval, id uint64, item *NFTItem, mu *sync.Mutex, fail func(string, error), meta func(string) (string, int)) *GnoswapPosition {
	p := &GnoswapPosition{}
	var wg sync.WaitGroup
	read := func(getter, realm string, fn func(lines []string) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines, err := qevalLines(ctx, eval, fmt.Sprintf("%s.%s(%d)", realm, getter, id))
			if err == nil {
				mu.Lock()
				err = fn(lines)
				mu.Unlock()
			}
			if err != nil {
				fail(getter, err)
			}
		}()
	}

	read("GetPositionPoolKey", gnoswapPosition, func(l []string) error {
		v, ok := reprText(l[0])
		if !ok {
			return badRepr(l[0])
		}
		return p.setPool(v)
	})
	read("GetPositionTicks", gnoswapPosition, func(l []string) error {
		if len(l) < 2 {
			return badRepr(strings.Join(l, " "))
		}
		lo, ok1 := price.ReprInt(l[0])
		hi, ok2 := price.ReprInt(l[1])
		if !ok1 || !ok2 {
			return badRepr(strings.Join(l, " "))
		}
		p.TickLower, p.TickUpper = int32(lo), int32(hi)
		return nil
	})
	read("GetPositionLiquidity", gnoswapPosition, func(l []string) error {
		v, ok := reprText(l[0])
		if !ok {
			return badRepr(l[0])
		}
		p.Liquidity = v
		return nil
	})
	read("IsInRange", gnoswapPosition, func(l []string) error {
		v, ok := price.ReprBool(l[0])
		if !ok {
			return badRepr(l[0])
		}
		p.InRange = &v
		return nil
	})
	read("IsBurned", gnoswapPosition, func(l []string) error {
		v, ok := price.ReprBool(l[0])
		if !ok {
			return badRepr(l[0])
		}
		p.Burned = v
		return nil
	})
	read("GetPositionTokenBalances", gnoswapPosition, func(l []string) error {
		if len(l) < 2 {
			return badRepr(strings.Join(l, " "))
		}
		a0, ok1 := price.ReprInt(l[0])
		a1, ok2 := price.ReprInt(l[1])
		if !ok1 || !ok2 {
			return badRepr(strings.Join(l, " "))
		}
		p.Amount0, p.Amount1 = &a0, &a1
		return nil
	})
	read("GetUnclaimedFee", gnoswapPosition, func(l []string) error {
		if len(l) < 2 {
			return badRepr(strings.Join(l, " "))
		}
		f0, ok1 := reprText(l[0])
		f1, ok2 := reprText(l[1])
		if !ok1 || !ok2 {
			return badRepr(strings.Join(l, " "))
		}
		p.Fee0, p.Fee1 = f0, f1
		return nil
	})
	read("IsStaked", gnoswapStaker, func(l []string) error {
		v, ok := price.ReprBool(l[0])
		if !ok {
			return badRepr(l[0])
		}
		p.Staked = v
		return nil
	})
	wg.Wait()

	// The second round depends on the first: the depositor and the reward
	// only exist for a staked position, and the current tick needs the pool.
	if p.Staked {
		read("GetDepositOwner", gnoswapStaker, func(l []string) error {
			v, ok := reprText(l[0])
			if !ok || v == "" {
				return badRepr(l[0])
			}
			item.Custodian, item.Owner = item.Owner, v
			return nil
		})
		read("CollectableEmissionReward", gnoswapStaker, func(l []string) error {
			v, ok := price.ReprInt(l[0])
			if !ok {
				return badRepr(l[0])
			}
			p.Reward = &v
			return nil
		})
	}
	if p.Pool != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines, err := qevalLines(ctx, eval, fmt.Sprintf("%s.GetSlot0Tick(%q)", gnoswapPool, p.Pool))
			if err != nil {
				fail("GetSlot0Tick", err)
				return
			}
			if v, ok := price.ReprInt(lines[0]); ok {
				t := int32(v)
				mu.Lock()
				p.TickCurrent = &t
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	p.PriceLower = ratioFloat(price.TickToRatio(p.TickLower))
	p.PriceUpper = ratioFloat(price.TickToRatio(p.TickUpper))
	if p.TickCurrent != nil {
		p.PriceNow = ratioFloat(price.TickToRatio(*p.TickCurrent))
	}
	if meta != nil && p.Pool != "" {
		var s0, s1 string
		s0, p.Token0Decimal = meta(p.Token0)
		s1, p.Token1Decimal = meta(p.Token1)
		if s0 != "" {
			p.Token0Symbol = s0
		}
		if s1 != "" {
			p.Token1Symbol = s1
		}
		if p.Token0Decimal > 0 && p.Token1Decimal > 0 {
			p.PriceScale = pow10(p.Token0Decimal - p.Token1Decimal)
		}
	}
	return p
}

// setPool splits a pool path, `<token0>:<token1>:<fee>`.
func (p *GnoswapPosition) setPool(path string) error {
	parts := strings.Split(path, ":")
	if len(parts) != 3 {
		return fmt.Errorf("pool path %q is not token0:token1:fee", path)
	}
	fee, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil {
		return fmt.Errorf("pool path %q: fee: %w", path, err)
	}
	p.Pool, p.Token0, p.Token1, p.Fee = path, parts[0], parts[1], uint32(fee)
	p.Token0Symbol, p.Token1Symbol = poolTokenSymbol(parts[0]), poolTokenSymbol(parts[1])
	return nil
}

// poolTokenSymbol is the `<name>` of `<realm>.<name>`, which is what a pool
// path carries and what a reader calls the token when the registry has no
// better word for it.
func poolTokenSymbol(t string) string {
	if i := strings.LastIndexByte(t, '.'); i >= 0 && i < len(t)-1 {
		return t[i+1:]
	}
	return t
}

// qevalLines runs one expression and returns its repr lines, or the error the
// realm returned in its last position.
func qevalLines(ctx context.Context, eval price.Eval, expr string) ([]string, error) {
	out, err := eval(ctx, expr)
	if err != nil {
		return nil, err
	}
	lines := price.ReprLines(out)
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty answer")
	}
	if msg, ok := reprError(lines); ok {
		return nil, fmt.Errorf("%s", msg)
	}
	return lines, nil
}

// qevalText runs one expression whose first result is a string or an address.
func qevalText(ctx context.Context, eval price.Eval, expr string) (string, error) {
	lines, err := qevalLines(ctx, eval, expr)
	if err != nil {
		return "", err
	}
	v, ok := reprText(lines[0])
	if !ok {
		return "", badRepr(lines[0])
	}
	return v, nil
}

// reprText unwraps a quoted repr of any type. A string prints as
// `("x" string)` and an address as `("g1…" .uverse.address)`, and the
// difference does not matter to anything that reads them here.
func reprText(line string) (string, bool) {
	m := reprQuoted.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", false
	}
	s, err := strconv.Unquote(`"` + m[1] + `"`)
	if err != nil {
		return m[1], true
	}
	return s, true
}

// reprError reports the error a multi-return call ended on. GRC721's OwnerOf on
// an id that does not exist answers `("" .uverse.address)` and then
// `(&(struct{("invalid token id" string)} errors.errorString) *errors.errorString)`:
// the first line alone reads as an item owned by nobody.
func reprError(lines []string) (string, bool) {
	if len(lines) < 2 {
		return "", false
	}
	last := lines[len(lines)-1]
	if last == "(undefined)" || !strings.HasPrefix(last, "(&") {
		return "", false
	}
	if m := reprAnyQuoted.FindStringSubmatch(last); m != nil {
		return m[1], true
	}
	return truncate(last, 120), true
}

func badRepr(s string) error { return fmt.Errorf("unexpected answer: %s", truncate(s, 120)) }

func ratioFloat(r interface{ Float64() (float64, bool) }) float64 {
	f, _ := r.Float64()
	return f
}

func pow10(n int) float64 {
	f := 1.0
	for ; n > 0; n-- {
		f *= 10
	}
	for ; n < 0; n++ {
		f /= 10
	}
	return f
}
