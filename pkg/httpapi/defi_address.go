package httpapi

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gnoverse/gnoscope/pkg/price"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// An account's money as one picture: what it holds now, what that is worth,
// how the total got here, and every transaction that moved any of it.
//
// The holdings tab drew two ledgers side by side and left the reader to add
// them up. For an account that trades, that is the whole question left
// unanswered: g1qyfled… made 273 router calls on mainnet, and its page listed
// a native table and a GRC20 table that between them never once said "swapped
// 12,000 GNOT for 640,000 GNS".
//
// Everything here is computed server-side over every leg, and the one list is
// paged. The browser never receives a ledger to sum: the old tab fetched up to
// 5,000 legs per side so it could draw a curve, which is exactly the shape that
// grows without bound on the accounts this page is for.
//
// ⚠️ Valued at today's prices, on purpose and out loud. The chain has no price
// history this explorer can read: the price ladder is a live read of the pools
// and an exchange anchor, and a curve priced at the time would need both
// replayed. A line at today's prices answers "how did the holdings change",
// not "what was the account worth that day", and the response says which.

const (
	defiHistoryLimit    = 25
	defiHistoryMaxLimit = 100

	// defiSeriesAssets is how many assets the curve breaks out by name. The
	// rest are summed into one "other" band, because a stacked chart with
	// fifteen bands is a rainbow nobody reads.
	defiSeriesAssets = 6

	// defiSeriesBalances caps the per-asset curves. A realm like a DEX pool
	// touches dozens of tokens; a page draws a handful.
	defiSeriesBalances = 12

	// defiFeeToken is the pseudo-token store.DefiBuckets returns the gas
	// under. It is native money, but not a leg.
	defiFeeToken = "fee"
)

type defiAssetRow struct {
	Token    string `json:"token"`
	Symbol   string `json:"symbol"`
	PkgPath  string `json:"pkg_path,omitempty"`
	Kind     string `json:"kind"`
	Fungible bool   `json:"fungible"`
	// Balance is base units for a fungible asset and an item count for a
	// GRC721, which carries no amount.
	Balance  int64   `json:"balance"`
	Decimals int     `json:"decimals,omitempty"`
	Amount   float64 `json:"amount,omitempty"`
	Priced   bool    `json:"priced"`
	USDPrice float64 `json:"usd_price,omitempty"`
	USDValue float64 `json:"usd_value,omitempty"`
	Tier     string  `json:"tier,omitempty"`
	Verified bool    `json:"verified"`
	// Source says where the balance was read: "chain" is a live bank read,
	// "ledger" a replay of the transfer events this explorer synced, which is
	// a floor whenever the ledger starts after the account's first transfer.
	Source string `json:"source"`
}

type defiLegRow struct {
	store.DefiLeg
	Symbol   string  `json:"symbol"`
	Kind     string  `json:"kind"`
	Decimals int     `json:"decimals,omitempty"`
	Priced   bool    `json:"priced"`
	USD      float64 `json:"usd,omitempty"`
}

type defiHistoryRow struct {
	TxHash      string           `json:"tx_hash"`
	BlockHeight int              `json:"block_height"`
	BlockTime   string           `json:"block_time,omitempty"`
	Action      string           `json:"action"`
	Calls       []store.DefiCall `json:"calls,omitempty"`
	Legs        []defiLegRow     `json:"legs"`
	FeeUgnot    int64            `json:"fee_ugnot,omitempty"`
	// InUSD and OutUSD are the priced legs at today's prices. Kept apart
	// rather than netted: a swap that gave $100 and got $97 is a different
	// row from one that moved $3.
	InUSD  float64 `json:"in_usd"`
	OutUSD float64 `json:"out_usd"`
}

type defiSeriesAsset struct {
	Token  string    `json:"token"`
	Symbol string    `json:"symbol"`
	USD    []float64 `json:"usd"`
}

// defiSeriesBalance is one asset's own balance per bucket, in base units (an
// item count for a GRC721), and its net movement inside each bucket. It is
// what a per-asset curve draws, and the reason no page has to fetch the legs
// to draw one.
type defiSeriesBalance struct {
	// Unit and Labels are set only when this asset has its own axis: its
	// moves all fell inside one bucket of the account's, so it is re-read at
	// a finer unit over its own span. Empty means the series' own axis.
	Unit     string   `json:"unit,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Token    string   `json:"token"`
	Symbol   string   `json:"symbol"`
	Fungible bool     `json:"fungible"`
	Delta    []int64  `json:"delta"`
	// In and Out are the gross halves of Delta, Out as a positive figure.
	In      []int64 `json:"in"`
	Out     []int64 `json:"out"`
	Balance []int64 `json:"balance"`
}

type defiSeries struct {
	// Unit is "hour" or "day", chosen from the span so a one-day-old account
	// still draws a curve rather than one point.
	Unit   string   `json:"unit"`
	Labels []string `json:"labels"`
	// TotalUSD is every priced asset, at today's prices, at the end of each
	// bucket. Assets breaks the same figure out by the largest holdings.
	TotalUSD []float64         `json:"total_usd"`
	Assets   []defiSeriesAsset `json:"assets"`
	// Balances is every asset's own curve, the most active first, capped at
	// defiSeriesBalances. ugnot's includes the gas this account paid.
	Balances []defiSeriesBalance `json:"balances"`
	// PricedAt is the moment the prices were read, which is the date every
	// point on the curve is valued at.
	PricedAt string `json:"priced_at,omitempty"`
	// NativeStartUgnot is what walking the native balance back from today
	// leaves at the start. Zero means the ledger explains the whole balance;
	// anything else is money that arrived without a recorded leg (a genesis
	// allocation, or a send this explorer has not synced).
	NativeStartUgnot int64 `json:"native_start_ugnot"`
}

type addressDefiResponse struct {
	Network string `json:"network"`
	Address string `json:"address"`

	TotalUSD     float64        `json:"total_usd"`
	Assets       []defiAssetRow `json:"assets"`
	PricedAssets int            `json:"priced_assets"`
	// UnpricedAssets is how many held assets have no price, so a total that
	// leaves them out says it does.
	UnpricedAssets    int    `json:"unpriced_assets"`
	PricesUnavailable string `json:"prices_unavailable,omitempty"`
	BalanceKnown      bool   `json:"balance_known"`

	Series *defiSeries `json:"series"`

	History       []defiHistoryRow `json:"history"`
	HistoryTotal  int              `json:"history_total"`
	HistoryOffset int              `json:"history_offset"`
	HistoryLimit  int              `json:"history_limit"`

	FeesUgnot       int64  `json:"fees_ugnot"`
	TokenLedgerFrom string `json:"token_ledger_from,omitempty"`
	FirstActivity   string `json:"first_activity,omitempty"`
	LastActivity    string `json:"last_activity,omitempty"`
}

// HandleAddressDefi answers the address page's defi tab.
//
// `?limit=` (default 25, max 100) and `?offset=` page the history. Everything
// else is over every leg regardless of the page, so turning a page never
// changes a figure above the table.
func (a *API) HandleAddressDefi(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	addr := r.PathValue("addr")
	if network == "" {
		jsonError(w, "balances are denominated per chain: select a network", 400)
		return
	}
	q := r.URL.Query()
	limit := intParam(q, "limit", defiHistoryLimit, defiHistoryMaxLimit)
	offset := intParam(q, "offset", 0, 0)

	resp := addressDefiResponse{
		Network: network, Address: addr, HistoryLimit: limit,
		Assets: []defiAssetRow{}, History: []defiHistoryRow{},
	}

	// Prices are best-effort, as on /holders: losing them costs the dollar
	// figures, never the page.
	pr := defiPricer{quotes: map[string]*price.Quote{}}
	if res, err := a.pricesFor(r.Context(), network); err == nil && res != nil {
		if res.Unavailable != "" {
			resp.PricesUnavailable = res.Unavailable
		}
		if res.Anchor != nil {
			pr.usdPerGNOT = res.Anchor.USDPerGNOT
			pr.at = res.Anchor.FetchedAt
		}
		for i := range res.Quotes {
			if res.Quotes[i].USDPerToken > 0 {
				pr.quotes[res.Quotes[i].Token] = &res.Quotes[i]
			}
		}
	} else if err != nil {
		resp.PricesUnavailable = err.Error()
	}

	// The native balance is the chain's own answer. The ledger cannot give
	// it for a signing account: see NativeStartUgnot.
	bal, balErr := fetchBalanceErr(r.Context(), addr, a.rpcURLFor(network))
	resp.BalanceKnown = balErr == nil
	liveUgnot := store.ParseUgnot(bal)

	positions, err := a.db.TokenPositions(network, addr)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	held := map[string]int64{store.NativeKey: liveUgnot}
	if resp.BalanceKnown {
		resp.Assets = append(resp.Assets, a.defiAsset(pr, store.NativeKey, "", true, liveUgnot, "chain"))
	}
	for _, p := range positions {
		bal := p.Balance
		if !p.Fungible {
			// An NFT position is a count of legs in minus legs out, which is
			// what TokenPositions cannot see because it sums values.
			bal = 0
		}
		held[p.Token] = bal
		if p.Fungible && bal == 0 {
			continue
		}
		resp.Assets = append(resp.Assets, a.defiAsset(pr, p.Token, p.PkgPath, p.Fungible, bal, "ledger"))
	}

	first, last, err := a.db.DefiSpan(network, addr)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	resp.FirstActivity, resp.LastActivity = first, last

	if first != "" {
		unit, prefix := defiBucketUnit(first, last)
		buckets, err := a.db.DefiBuckets(network, addr, prefix)
		if err != nil {
			jsonError(w, err.Error(), 500)
			return
		}
		for _, b := range buckets {
			if b.Token == defiFeeToken {
				resp.FeesUgnot -= b.Delta
			}
		}
		items := nftItems(buckets)
		for i := range resp.Assets {
			if !resp.Assets[i].Fungible {
				resp.Assets[i].Balance = int64(items[resp.Assets[i].Token])
			}
		}
		resp.Series = buildDefiSeries(pr, buckets, held, resp.BalanceKnown, resp.Assets, unit)
		a.refineDefiBalances(network, addr, resp.Series, held)
	}
	// Drop the NFT collections this account no longer holds anything of, now
	// that the item counts are known. Kept until here because the count
	// comes from the buckets.
	kept := resp.Assets[:0]
	for _, as := range resp.Assets {
		if !as.Fungible && as.Balance <= 0 {
			continue
		}
		kept = append(kept, as)
	}
	resp.Assets = kept
	sort.SliceStable(resp.Assets, func(i, j int) bool { return defiAssetLess(resp.Assets[i], resp.Assets[j]) })
	for _, as := range resp.Assets {
		if as.Priced {
			resp.TotalUSD += as.USDValue
			resp.PricedAssets++
		} else {
			resp.UnpricedAssets++
		}
	}

	total, err := a.db.DefiTxCount(network, addr)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	// Clamped and reported back, so a pager adding what it has to what it
	// was given terminates rather than asking for nothing forever.
	if offset > total {
		offset = total
	}
	txs, err := a.db.DefiTxs(network, addr, limit, offset)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	resp.HistoryTotal, resp.HistoryOffset = total, offset
	for _, tx := range txs {
		resp.History = append(resp.History, a.defiHistoryRow(pr, tx))
	}
	resp.TokenLedgerFrom = a.db.EarliestTokenTransfer(network)
	JSONResponse(w, resp)
}

// defiPricer is the price read, held once per request.
type defiPricer struct {
	quotes     map[string]*price.Quote
	usdPerGNOT float64
	at         time.Time
}

// usd values a base-unit amount. ugnot is priced off the anchor directly, as
// the chain's own coin with six decimals by protocol.
func (p defiPricer) usd(token string, amount int64) (float64, bool) {
	if token == store.NativeKey {
		if p.usdPerGNOT <= 0 {
			return 0, false
		}
		return float64(amount) / 1e6 * p.usdPerGNOT, true
	}
	q, ok := p.quotes[token]
	if !ok {
		return 0, false
	}
	if amount < 0 {
		return -usdValue(-amount, q), true
	}
	return usdValue(amount, q), true
}

func (a *API) tokenMeta(pr defiPricer, token string) (symbol string, decimals int, verified bool) {
	if token == store.NativeKey {
		return "GNOT", 6, true
	}
	_, symbol = store.TokenKeyParts(token)
	if meta, ok := a.registry.Tokens[token]; ok {
		if meta.Symbol != "" {
			symbol = meta.Symbol
		}
		decimals, verified = meta.Decimals, meta.Verified
	}
	if q, ok := pr.quotes[token]; ok && q.DecimalsKnown && decimals == 0 {
		decimals = q.Decimals
	}
	return symbol, decimals, verified
}

func defiKind(token string, fungible bool) string {
	switch {
	case token == store.NativeKey:
		return store.KindNative
	case !fungible:
		return store.KindGRC721
	}
	return store.KindGRC20
}

func (a *API) defiAsset(pr defiPricer, token, pkgPath string, fungible bool, bal int64, source string) defiAssetRow {
	sym, dec, ver := a.tokenMeta(pr, token)
	row := defiAssetRow{
		Token: token, Symbol: sym, PkgPath: pkgPath, Kind: defiKind(token, fungible),
		Fungible: fungible, Balance: bal, Decimals: dec, Verified: ver, Source: source,
	}
	if fungible && dec > 0 {
		row.Amount = scaled(bal, dec)
	}
	if !fungible {
		return row
	}
	if v, ok := pr.usd(token, bal); ok {
		row.Priced, row.USDValue = true, v
		if token == store.NativeKey {
			row.USDPrice, row.Tier = pr.usdPerGNOT, "market"
		} else if q := pr.quotes[token]; q != nil {
			row.USDPrice, row.Tier = q.USDPerToken, string(q.Tier)
		}
	}
	return row
}

// defiAssetLess orders the holdings: priced by value, then the unpriced by
// kind and symbol. Never raw balances across tokens, which would rank a
// 1e18-scaled token above a real one.
func defiAssetLess(a, b defiAssetRow) bool {
	if a.Priced != b.Priced {
		return a.Priced
	}
	if a.Priced && a.USDValue != b.USDValue {
		return a.USDValue > b.USDValue
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Symbol < b.Symbol
}

func (a *API) defiHistoryRow(pr defiPricer, tx store.DefiTx) defiHistoryRow {
	row := defiHistoryRow{
		TxHash: tx.TxHash, BlockHeight: tx.BlockHeight, BlockTime: tx.BlockTime,
		Calls: tx.Calls, FeeUgnot: tx.FeeUgnot, Legs: []defiLegRow{},
	}
	for _, l := range tx.Legs {
		fungible := l.Delta != 0
		sym, dec, _ := a.tokenMeta(pr, l.Token)
		lr := defiLegRow{DefiLeg: l, Symbol: sym, Kind: defiKind(l.Token, fungible), Decimals: dec}
		if fungible {
			if v, ok := pr.usd(l.Token, l.Delta); ok {
				lr.Priced, lr.USD = true, v
				if v > 0 {
					row.InUSD += v
				} else {
					row.OutUSD -= v
				}
			}
		}
		row.Legs = append(row.Legs, lr)
	}
	row.Action = classifyDefi(tx.Calls, tx.Legs)
	return row
}

// classifyDefi names what a transaction did, from the functions called first
// and the shape of the legs second.
//
// Function names win where they are unambiguous, because the shape alone
// cannot tell adding liquidity (two out, an NFT in) from buying an NFT with
// two tokens. The shape is the fallback for everything this list does not
// know, which on an open chain is most realms.
func classifyDefi(calls []store.DefiCall, legs []store.DefiLeg) string {
	// By priority over every call, not by the first one: an LP mint funded in
	// GNOT is a wugnot Deposit followed by a position Mint, and the reader's
	// answer is "added liquidity", not "wrapped".
	best, rank := "", len(defiActionRank)
	for _, c := range calls {
		if a := callAction(c); a != "" {
			for i, r := range defiActionRank {
				if r == a && i < rank {
					best, rank = a, i
				}
			}
		}
	}
	if best != "" {
		return best
	}
	ins, outs := map[string]bool{}, map[string]bool{}
	for _, l := range legs {
		switch legSign(l) {
		case 1:
			ins[l.Token] = true
		case -1:
			outs[l.Token] = true
		}
	}
	switch {
	case len(ins) > 0 && len(outs) > 0:
		for t := range ins {
			if !outs[t] {
				return "swap"
			}
		}
		return "mixed"
	case len(ins) > 0:
		return "receive"
	case len(outs) > 0:
		return "send"
	}
	return "other"
}

// defiActionRank orders the actions a transaction can carry several of, the
// one a reader would name it by first.
var defiActionRank = []string{
	"add liquidity", "remove liquidity", "swap", "claim", "stake", "unstake", "wrap", "unwrap",
}

func callAction(c store.DefiCall) string {
	fn := strings.ToLower(c.Func)
	wugnot := strings.HasSuffix(c.PkgPath, "/wugnot")
	switch {
	case wugnot && fn == "deposit":
		return "wrap"
	case wugnot && fn == "withdraw":
		return "unwrap"
	case strings.Contains(fn, "swap"):
		return "swap"
	case fn == "mint" && strings.Contains(c.PkgPath, "/position"),
		fn == "increaseliquidity":
		return "add liquidity"
	case fn == "decreaseliquidity":
		return "remove liquidity"
	case strings.HasPrefix(fn, "collect"), strings.HasPrefix(fn, "claim"):
		return "claim"
	case fn == "staketoken", fn == "delegate", fn == "stake":
		return "stake"
	case fn == "unstaketoken", fn == "undelegate", fn == "unstake":
		return "unstake"
	}
	return ""
}

func legSign(l store.DefiLeg) int {
	switch {
	case l.Delta < 0 || (l.Delta == 0 && l.Items < 0):
		return -1
	case l.Delta > 0 || l.Items > 0:
		return 1
	}
	return 0
}

// defiBucketUnit picks the bucket from the span of the account's activity:
// minutes under three hours, hours under three days, days otherwise. A
// day-old account bucketed by day is one point, a year-old one bucketed by
// hour is 8,760, and a realm that lived for twenty minutes needs minutes to
// draw anything at all.
func defiBucketUnit(first, last string) (string, int) {
	f, err1 := time.Parse(time.RFC3339Nano, first)
	l, err2 := time.Parse(time.RFC3339Nano, last)
	if err1 != nil || err2 != nil {
		return "day", 10
	}
	switch span := l.Sub(f); {
	case span < 3*time.Hour:
		return "minute", 16
	case span < 72*time.Hour:
		return "hour", 13
	}
	return "day", 10
}

// refineDefiBalances gives an asset its own, finer axis when the account's is
// too coarse to draw it: every move it ever made fell inside one bucket.
//
// The account's axis is chosen from the account's whole span, which is right
// for the value curve and wrong for an asset traded in one burst: a realm that
// lived two days and moved its token inside one hour drew that token as a
// single point. Re-read from the store over the asset's own span, which is
// one small query per asset and only for the assets that need it.
func (a *API) refineDefiBalances(network, addr string, s *defiSeries, held map[string]int64) {
	for i := range s.Balances {
		b := &s.Balances[i]
		active := 0
		for j := range b.Delta {
			if b.Delta[j] != 0 || b.In[j] != 0 || b.Out[j] != 0 {
				active++
			}
		}
		if active != 1 || b.Token == store.NativeKey {
			continue
		}
		first, last, err := a.db.DefiTokenSpan(network, addr, b.Token)
		if err != nil || first == "" {
			continue
		}
		unit, prefix := defiBucketUnit(first, last)
		if unit == s.Unit {
			continue
		}
		buckets, err := a.db.DefiTokenBuckets(network, addr, b.Token, prefix)
		if err != nil || len(buckets) < 2 {
			continue
		}
		own := buildDefiSeries(defiPricer{}, buckets, map[string]int64{b.Token: held[b.Token]}, true,
			[]defiAssetRow{{Token: b.Token, Symbol: b.Symbol, Fungible: b.Fungible}}, unit)
		for _, ob := range own.Balances {
			if ob.Token == b.Token {
				ob.Unit, ob.Labels = unit, own.Labels
				*b = ob
			}
		}
	}
}

// defiLabels is every bucket from the first one with activity to the last,
// gaps included, so the x axis is time rather than "days something happened".
//
// A daily series runs on to today, because a flat line since the last trade
// is the truth about an account that stopped. An hourly one stops at its last
// bucket: it was chosen because the account's whole life fits in three days,
// and extending it to a now months later would be thousands of empty hours.
func defiLabels(buckets []store.DefiBucket, unit string, now time.Time) []string {
	seen := map[string]bool{}
	var raw []string
	for _, b := range buckets {
		if !seen[b.Bucket] {
			seen[b.Bucket] = true
			raw = append(raw, b.Bucket)
		}
	}
	sort.Strings(raw)
	if len(raw) == 0 {
		return raw
	}
	layout, step := "2006-01-02", 24*time.Hour
	switch unit {
	case "hour":
		layout, step = "2006-01-02T15", time.Hour
	case "minute":
		layout, step = "2006-01-02T15:04", time.Minute
	}
	start, err1 := time.Parse(layout, raw[0])
	end, err2 := time.Parse(layout, raw[len(raw)-1])
	if err1 != nil || err2 != nil {
		return raw
	}
	if unit == "day" {
		if today, err := time.Parse(layout, now.Format(layout)); err == nil && today.After(end) {
			end = today
		}
	}
	// A guard, not a limit anyone should meet: daily since genesis is a few
	// hundred points. If a clock is wrong by a decade, return what was seen.
	if end.Sub(start)/step > 5000 {
		return raw
	}
	out := []string{}
	for t := start; !t.After(end); t = t.Add(step) {
		out = append(out, t.Format(layout))
	}
	return out
}

// nftItems sums the signed leg counts per token over every bucket, which is
// an NFT position: legs in minus legs out.
func nftItems(buckets []store.DefiBucket) map[string]int {
	out := map[string]int{}
	for _, b := range buckets {
		out[b.Token] += b.Items
	}
	return out
}

// buildDefiSeries walks every asset's balance back from today through the
// bucketed deltas, and values each bucket at today's prices.
//
// Walked back rather than summed forward, for the native coin especially: the
// ledger cannot see a genesis allocation, so a forward sum starts at zero and
// ends somewhere the account has never been. Anchored on the live balance, the
// line ends where the chain says it is, and whatever the ledger cannot explain
// shows up once, at the start, as NativeStartUgnot.
func buildDefiSeries(pr defiPricer, buckets []store.DefiBucket, held map[string]int64, balanceKnown bool, assets []defiAssetRow, unit string) *defiSeries {
	labels := defiLabels(buckets, unit, time.Now().UTC())
	labelIdx := make(map[string]int, len(labels))
	for i, l := range labels {
		labelIdx[l] = i
	}
	n := len(labels)

	// Per-asset deltas per bucket. The fee is native money and folds into
	// ugnot here, which is the one place it belongs.
	deltas := map[string][]int64{}
	itemDeltas := map[string][]int64{}
	ins, outs := map[string][]int64{}, map[string][]int64{}
	for _, b := range buckets {
		tok := b.Token
		if tok == defiFeeToken {
			tok = store.NativeKey
		}
		if deltas[tok] == nil {
			deltas[tok] = make([]int64, n)
			itemDeltas[tok] = make([]int64, n)
			ins[tok] = make([]int64, n)
			outs[tok] = make([]int64, n)
		}
		i := labelIdx[b.Bucket]
		deltas[tok][i] += b.Delta
		itemDeltas[tok][i] += int64(b.Items)
		ins[tok][i] += b.In
		outs[tok][i] += b.Out
	}
	symbols := map[string]string{}
	fungible := map[string]bool{store.NativeKey: true}
	for _, as := range assets {
		symbols[as.Token] = as.Symbol
		fungible[as.Token] = as.Fungible
	}

	s := &defiSeries{Unit: unit, Labels: labels, TotalUSD: make([]float64, n),
		Assets: []defiSeriesAsset{}, Balances: []defiSeriesBalance{}}
	if !pr.at.IsZero() {
		s.PricedAt = pr.at.UTC().Format(time.RFC3339)
	}

	// The native end point is the live balance when the chain answered, and
	// the ledger's own sum when it did not, so the curve still draws.
	if !balanceKnown {
		var sum int64
		for _, d := range deltas[store.NativeKey] {
			sum += d
		}
		held[store.NativeKey] = sum
	}

	// Each priced asset's value per bucket, before any of them is named.
	valued := map[string][]float64{}

	// Sorted so the walk, and therefore the response, is the same on every
	// request: map order would shuffle the per-asset curves between reloads.
	toks := make([]string, 0, len(deltas))
	for tok := range deltas {
		toks = append(toks, tok)
	}
	sort.Strings(toks)
	for _, tok := range toks {
		ds := deltas[tok]
		isFungible, known := fungible[tok]
		if !known {
			// Seen in the history and absent from the holdings: an asset the
			// account touched and emptied. Fungible unless its legs say not.
			isFungible = false
			for _, d := range ds {
				if d != 0 {
					isFungible = true
					break
				}
			}
		}
		if !isFungible {
			ds = itemDeltas[tok]
		}
		bal := held[tok]
		// Walk back: the balance at the end of bucket i is today's minus
		// everything that moved after it.
		balAt := make([]int64, n)
		for i := n - 1; i >= 0; i-- {
			balAt[i] = bal
			bal -= ds[i]
		}
		if tok == store.NativeKey {
			s.NativeStartUgnot = bal
		}
		sym := symbols[tok]
		if sym == "" {
			_, sym = store.TokenKeyParts(tok)
		}
		s.Balances = append(s.Balances, defiSeriesBalance{
			Token: tok, Symbol: sym, Fungible: isFungible, Delta: ds,
			In: ins[tok], Out: outs[tok], Balance: balAt,
		})
		if !isFungible {
			continue
		}
		if _, ok := pr.usd(tok, 1); !ok {
			continue
		}
		usd := make([]float64, n)
		for i := 0; i < n; i++ {
			v, ok := pr.usd(tok, balAt[i])
			if !ok {
				continue
			}
			// A balance the walk drives below zero is the ledger missing an
			// inflow, not a short position; it is drawn as nothing rather
			// than as a negative dollar figure.
			v = math.Max(v, 0)
			s.TotalUSD[i] += v
			usd[i] = v
		}
		valued[tok] = usd
	}

	// Bands are named by their peak, not by today's holding. A trader's page
	// is about what it held on the way: on g1qyfled… the GNS and WUGNOT it
	// cycled through are near zero today, and naming bands by the current
	// balance put the whole trading spike into "other".
	peak := func(xs []float64) float64 {
		m := 0.0
		for _, x := range xs {
			m = math.Max(m, x)
		}
		return m
	}
	ranked := make([]string, 0, len(valued))
	for tok, xs := range valued {
		if peak(xs) > 0 {
			ranked = append(ranked, tok)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if pi, pj := peak(valued[ranked[i]]), peak(valued[ranked[j]]); pi != pj {
			return pi > pj
		}
		return ranked[i] < ranked[j]
	})
	other := defiSeriesAsset{Token: "other", Symbol: "other", USD: make([]float64, n)}
	for k, tok := range ranked {
		if k < defiSeriesAssets {
			sym := symbols[tok]
			if sym == "" {
				_, sym = store.TokenKeyParts(tok)
			}
			s.Assets = append(s.Assets, defiSeriesAsset{Token: tok, Symbol: sym, USD: valued[tok]})
			continue
		}
		for i, v := range valued[tok] {
			other.USD[i] += v
		}
	}
	if len(ranked) > defiSeriesAssets {
		s.Assets = append(s.Assets, other)
	}
	// Most active first: the curve worth drawing is the one that moves.
	active := func(b defiSeriesBalance) int {
		c := 0
		for _, d := range b.Delta {
			if d != 0 {
				c++
			}
		}
		return c
	}
	sort.SliceStable(s.Balances, func(i, j int) bool {
		if ai, aj := active(s.Balances[i]), active(s.Balances[j]); ai != aj {
			return ai > aj
		}
		return s.Balances[i].Token < s.Balances[j].Token
	})
	if len(s.Balances) > defiSeriesBalances {
		s.Balances = s.Balances[:defiSeriesBalances]
	}
	return s
}
