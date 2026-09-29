package price

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// The USD leg.
//
// There is no oracle on gno.land, and there is no honest way to invent one, so
// this is the one place where the explorer reaches off the chain for a number
// it then multiplies everything else by. Two venues rather than one, because a
// single feed is a single point of being wrong and a disagreement between two
// is information a reader should see.
//
// Both endpoints are public, keyless, and were verified on 2026-09-29:
//
//	Kraken  GNOTUSD   last 0.07072, 24h VWAP 0.070872, 24h volume 1,091,174 GNOT
//	KuCoin  GNOT-USDT last 0.07072, bid 0.07072 / ask 0.07073
//
// Kraken is preferred where both answer, for one reason: it publishes a
// 24-hour volume-weighted average, and KuCoin's level-1 endpoint publishes only
// the last trade. A VWAP cannot be moved by one small print, and a last trade
// can. CoinGecko also carries this pair (as `gno-land`) and is deliberately not
// used: it is an aggregator over these same venues, so adding it would look
// like a third opinion while being a restatement of the first two.

// FetchAnchor reads GNOT/USD from the public venues.
//
// Never fatal on a single venue: one exchange being unreachable degrades the
// anchor's confidence, it does not remove the price. It is fatal when none
// answer, and the caller must then show no prices at all rather than a stale
// one, because a stale anchor propagates into every row on the page with
// nothing marking it.
func FetchAnchor(ctx context.Context, hc *http.Client) (*Anchor, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	type result struct {
		src AnchorSource
		ok  bool
	}
	ch := make(chan result, 2)
	go func() { s, ok := fetchKraken(ctx, hc); ch <- result{s, ok} }()
	go func() { s, ok := fetchKuCoin(ctx, hc); ch <- result{s, ok} }()

	a := &Anchor{FetchedAt: time.Now().UTC()}
	for i := 0; i < 2; i++ {
		r := <-ch
		if r.ok && r.src.USD > 0 {
			a.Sources = append(a.Sources, r.src)
		}
	}
	if len(a.Sources) == 0 {
		return nil, fmt.Errorf("no GNOT/USD venue answered")
	}
	sort.Slice(a.Sources, func(i, j int) bool {
		// A published VWAP outranks a last trade; ties keep venue order stable.
		if (a.Sources[i].Kind == "vwap-24h") != (a.Sources[j].Kind == "vwap-24h") {
			return a.Sources[i].Kind == "vwap-24h"
		}
		return a.Sources[i].Venue < a.Sources[j].Venue
	})
	a.USDPerGNOT = a.Sources[0].USD

	lo, hi := a.Sources[0].USD, a.Sources[0].USD
	for _, s := range a.Sources {
		lo = math.Min(lo, s.USD)
		hi = math.Max(hi, s.USD)
	}
	if lo > 0 {
		a.SpreadPct = (hi - lo) / lo * 100
	}
	return a, nil
}

// krakenTicker is the subset of Kraken's /0/public/Ticker shape this needs.
// The fields are single letters by Kraken's own design: `c` is the last trade
// as [price, lot volume], `p` is [today VWAP, last-24h VWAP], `v` the matching
// volumes. Index 1 of p and v is the rolling 24 hours, which is the one that
// does not reset at midnight UTC.
type krakenTicker struct {
	Error  []string `json:"error"`
	Result map[string]struct {
		C []string `json:"c"`
		P []string `json:"p"`
		V []string `json:"v"`
	} `json:"result"`
}

func fetchKraken(ctx context.Context, hc *http.Client) (AnchorSource, bool) {
	var body krakenTicker
	if err := getJSON(ctx, hc, "https://api.kraken.com/0/public/Ticker?pair=GNOTUSD", &body); err != nil {
		return AnchorSource{}, false
	}
	if len(body.Error) > 0 {
		return AnchorSource{}, false
	}
	for _, t := range body.Result {
		src := AnchorSource{Venue: "Kraken", Pair: "GNOT/USD", Kind: "vwap-24h"}
		if len(t.P) > 1 {
			if v, err := strconv.ParseFloat(t.P[1], 64); err == nil && v > 0 {
				src.USD = v
			}
		}
		if src.USD == 0 && len(t.C) > 0 {
			// Fall back to the last trade and say so, rather than dropping the
			// venue: one venue answering weakly still beats one venue.
			if v, err := strconv.ParseFloat(t.C[0], 64); err == nil {
				src.USD, src.Kind = v, "last"
			}
		}
		if len(t.V) > 1 {
			if v, err := strconv.ParseFloat(t.V[1], 64); err == nil {
				src.Volume24h = v
			}
		}
		if src.USD > 0 {
			return src, true
		}
	}
	return AnchorSource{}, false
}

type kucoinTicker struct {
	Code string `json:"code"`
	Data struct {
		Price string `json:"price"`
	} `json:"data"`
}

func fetchKuCoin(ctx context.Context, hc *http.Client) (AnchorSource, bool) {
	var body kucoinTicker
	url := "https://api.kucoin.com/api/v1/market/orderbook/level1?symbol=GNOT-USDT"
	if err := getJSON(ctx, hc, url, &body); err != nil || body.Code != "200000" {
		return AnchorSource{}, false
	}
	v, err := strconv.ParseFloat(body.Data.Price, 64)
	if err != nil || v <= 0 {
		return AnchorSource{}, false
	}
	// USDT, not USD. Treated as one dollar here, which is the ordinary
	// convention and is wrong by whatever USDT's own peg is off by. Named in
	// the pair so a reader can see the substitution rather than infer it.
	return AnchorSource{Venue: "KuCoin", Pair: "GNOT/USDT", USD: v, Kind: "last"}, true
}

func getJSON(ctx context.Context, hc *http.Client, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gnoscope")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}
