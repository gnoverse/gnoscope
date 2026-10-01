package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/price"
	"github.com/gnoverse/gnoscope/pkg/store"
)

func TestClassifyDefi(t *testing.T) {
	const (
		gns  = "gno.land/r/gnoswap/gns.GNS.0000000"
		wug  = "gno.land/r/gnoland/wugnot.wugnot.0000000"
		gnft = "gno.land/r/gnoswap/gnft.GNFT.0000000"
	)
	call := func(pkg, fn string) []store.DefiCall { return []store.DefiCall{{PkgPath: pkg, Func: fn}} }
	out := func(tok string, v int64) store.DefiLeg { return store.DefiLeg{Token: tok, Delta: -v} }
	in := func(tok string, v int64) store.DefiLeg { return store.DefiLeg{Token: tok, Delta: v} }
	for _, tt := range []struct {
		name  string
		calls []store.DefiCall
		legs  []store.DefiLeg
		want  string
	}{
		{"router swap", call("gno.land/r/gnoswap/router", "ExactInSwapRoute"), []store.DefiLeg{out(wug, 1), in(gns, 2)}, "swap"},
		{"wrap", call("gno.land/r/gnoland/wugnot", "Deposit"), []store.DefiLeg{out("ugnot", 1), in(wug, 1)}, "wrap"},
		{"unwrap", call("gno.land/r/gnoland/wugnot", "Withdraw"), []store.DefiLeg{out(wug, 1), in("ugnot", 1)}, "unwrap"},
		// The shape alone would call this a swap: two assets out, one in. The
		// function is what says it is liquidity.
		{"lp mint", call("gno.land/r/gnoswap/position", "Mint"),
			[]store.DefiLeg{out(gns, 1), out(wug, 1), {Token: gnft, Items: 1}}, "add liquidity"},
		// Wrapped and minted in one transaction: the mint is what it was for.
		{"lp mint funded in gnot", []store.DefiCall{
			{PkgPath: "gno.land/r/gnoland/wugnot", Func: "Deposit"},
			{PkgPath: "gno.land/r/gnoswap/position", Func: "Mint"},
		}, []store.DefiLeg{out("ugnot", 1), out(gns, 1), {Token: gnft, Items: 1}}, "add liquidity"},
		{"lp exit", call("gno.land/r/gnoswap/position", "DecreaseLiquidity"), []store.DefiLeg{in(gns, 1)}, "remove liquidity"},
		{"claim", call("gno.land/r/gnoswap/staker", "CollectReward"), []store.DefiLeg{in(gns, 1)}, "claim"},
		{"stake gns", call("gno.land/r/gnoswap/gov/staker", "Delegate"), []store.DefiLeg{out(gns, 1)}, "stake"},
		// No call this list knows: the shape decides.
		{"unknown realm, two assets", call("gno.land/r/x/dex", "Trade"), []store.DefiLeg{out(gns, 1), in(wug, 1)}, "swap"},
		{"plain receive", nil, []store.DefiLeg{in("ugnot", 5)}, "receive"},
		{"plain send", nil, []store.DefiLeg{out("ugnot", 5)}, "send"},
		{"same token both ways", nil, []store.DefiLeg{out(gns, 1), in(gns, 1)}, "mixed"},
		{"nothing", nil, nil, "other"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDefi(tt.calls, tt.legs); got != tt.want {
				t.Errorf("classifyDefi = %q, want %q", got, tt.want)
			}
		})
	}
}

// The curve is walked back from today's balance, so it ends where the chain
// says the account is even when the ledger cannot explain how it got there.
// What the ledger cannot explain is reported once, at the start, never spread
// over the line.
func TestBuildDefiSeriesWalksBackFromToday(t *testing.T) {
	const gns = "gno.land/r/gnoswap/gns.GNS.0000000"
	pr := defiPricer{
		usdPerGNOT: 2, // $2 per GNOT, so 1e6 ugnot is $2
		quotes: map[string]*price.Quote{
			gns: {Token: gns, USDPerToken: 1, USDPerBaseUnit: "1/1000000", Decimals: 6},
		},
		at: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
	}
	buckets := []store.DefiBucket{
		{Bucket: "2026-09-01", Token: "ugnot", Delta: 1_000_000, In: 1_000_000},
		{Bucket: "2026-09-02", Token: "ugnot", Delta: -500_000, Out: 500_000},
		{Bucket: "2026-09-02", Token: gns, Delta: 3_000_000, In: 3_000_000},
		{Bucket: "2026-09-02", Token: "fee", Delta: -100_000, Out: 100_000},
	}
	// Live is 2.4 GNOT, the ledger explains 0.4 of it: 2.0 arrived unrecorded.
	held := map[string]int64{"ugnot": 2_400_000, gns: 3_000_000}
	assets := []defiAssetRow{
		{Token: gns, Symbol: "GNS", Priced: true, Fungible: true},
		{Token: "ugnot", Symbol: "GNOT", Priced: true, Fungible: true},
	}
	s := buildDefiSeries(pr, buckets, held, true, assets, "day")

	// Labels run on to today, with the gap days filled.
	if len(s.Labels) < 2 || s.Labels[0] != "2026-09-01" || s.Labels[1] != "2026-09-02" {
		t.Fatalf("labels = %v", s.Labels)
	}
	// End of day 1: 2.4 - (-0.5 - 0.1) = 3.0 GNOT ($6), no GNS yet.
	if got := s.TotalUSD[0]; got != 6 {
		t.Errorf("day 1 total = %v, want 6", got)
	}
	// End of day 2: today's balances, 2.4 GNOT ($4.8) + 3 GNS ($3).
	if got := s.TotalUSD[1]; got < 7.799 || got > 7.801 {
		t.Errorf("day 2 total = %v, want 7.8", got)
	}
	if s.NativeStartUgnot != 2_000_000 {
		t.Errorf("native start = %d, want 2000000: what the ledger cannot explain", s.NativeStartUgnot)
	}
	// The per-asset curve is in base units, fee included, and is what the
	// realm tab's charts draw instead of summing legs in the browser.
	var native *defiSeriesBalance
	for i := range s.Balances {
		if s.Balances[i].Token == "ugnot" {
			native = &s.Balances[i]
		}
	}
	if native == nil || native.Balance[0] != 3_000_000 || native.Balance[1] != 2_400_000 || native.Delta[1] != -600_000 {
		t.Fatalf("native curve = %+v, want 3.0 then 2.4 GNOT with -0.6 moved on day 2", native)
	}
	if native.In[0] != 1_000_000 || native.Out[1] != 600_000 {
		t.Errorf("native in/out = %v / %v, want 1.0 in on day 1 and 0.6 out (fee included) on day 2", native.In, native.Out)
	}
	// Bands are ranked by peak value, not by today's holding: GNOT peaked at
	// $6 and GNS at $3, so GNOT is the first band even though the holdings
	// table would list them in some other order.
	if len(s.Assets) != 2 || s.Assets[0].Token != "ugnot" || s.Assets[1].Token != gns {
		t.Errorf("assets = %+v, want GNOT then GNS, by peak", s.Assets)
	}
}

func TestAddressDefiEndpoint(t *testing.T) {
	db := store.NewTestDB(t)
	const net = "mainnet"
	const me = "g1me00000000000000000000000000000000000"
	const tok = "gno.land/r/demo/tok.TOK.0"
	const nft = "gno.land/r/demo/nft.NFT.0"
	for i, x := range []store.TokenTransfer{
		{Token: tok, From: "", To: me, Value: 900},
		{Token: tok, From: me, To: "g1other", Value: 400},
		{Token: nft, From: "", To: me},
		{Token: nft, From: "", To: me},
		{Token: nft, From: me, To: "g1other"},
	} {
		x.BlockHeight = 10 + i
		x.BlockTime = "2026-01-0" + string(rune('1'+i)) + "T00:00:00Z"
		if err := db.InsertTokenTransfer(net, "TX"+string(rune('A'+i)), 0, x); err != nil {
			t.Fatal(err)
		}
	}

	get := func(q string) addressDefiResponse {
		t.Helper()
		api := NewAPI(db, nil, nil, nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/address/"+me+"/defi?network="+net+q, nil)
		req.SetPathValue("addr", me)
		api.HandleAddressDefi(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var got addressDefiResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := get("")
	if got.HistoryTotal != 5 || len(got.History) != 5 {
		t.Fatalf("history %d of %d, want 5 of 5", len(got.History), got.HistoryTotal)
	}
	held := map[string]int64{}
	for _, as := range got.Assets {
		held[as.Token] = as.Balance
	}
	if held[tok] != 500 {
		t.Errorf("TOK balance = %d, want 500", held[tok])
	}
	// Two in, one out: an NFT position is a count, and TokenPositions alone
	// would have reported zero.
	if held[nft] != 1 {
		t.Errorf("NFT items = %d, want 1", held[nft])
	}
	if got.Series == nil || len(got.Series.Labels) == 0 {
		t.Errorf("no series: %+v", got.Series)
	}

	// The page is the only thing that changes with the page.
	p := get("&limit=2&offset=4")
	if len(p.History) != 1 || p.HistoryOffset != 4 || p.HistoryTotal != 5 {
		t.Errorf("last page = %d rows at offset %d of %d, want 1 at 4 of 5", len(p.History), p.HistoryOffset, p.HistoryTotal)
	}
	if p.History[0].TxHash != "TXA" {
		t.Errorf("oldest row = %s, want TXA", p.History[0].TxHash)
	}
	// Past the end is clamped, not an empty walk forever.
	if e := get("&offset=99"); e.HistoryOffset != 5 || len(e.History) != 0 {
		t.Errorf("past the end: offset %d, %d rows", e.HistoryOffset, len(e.History))
	}
	// The ceiling holds whatever is asked.
	if c := get("&limit=100000"); c.HistoryLimit != defiHistoryMaxLimit {
		t.Errorf("limit = %d, want the ceiling %d", c.HistoryLimit, defiHistoryMaxLimit)
	}

	api := NewAPI(db, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/address/"+me+"/defi", nil)
	req.SetPathValue("addr", me)
	api.HandleAddressDefi(rec, req)
	if rec.Code != 400 {
		t.Errorf("all-networks: status %d, want 400", rec.Code)
	}
}

// The case the peak rule exists for: a token bought and sold back to zero is
// most of a trader's curve, and today's holdings do not list it at all.
func TestDefiSeriesNamesWhatWasTradedThrough(t *testing.T) {
	const gns = "gno.land/r/gnoswap/gns.GNS.0000000"
	pr := defiPricer{quotes: map[string]*price.Quote{
		gns: {Token: gns, USDPerToken: 1, USDPerBaseUnit: "1/1000000", Decimals: 6},
	}}
	var buckets []store.DefiBucket
	buckets = append(buckets,
		store.DefiBucket{Bucket: "2026-09-01", Token: gns, Delta: 50_000_000, In: 50_000_000},
		store.DefiBucket{Bucket: "2026-09-03", Token: gns, Delta: -50_000_000, Out: 50_000_000})
	// Six small tokens that are held today, each worth a dollar, which the old
	// rule would have named first.
	var assets []defiAssetRow
	held := map[string]int64{gns: 0}
	for i := 0; i < defiSeriesAssets; i++ {
		tok := "gno.land/r/x/t" + string(rune('a'+i)) + ".T.0"
		pr.quotes[tok] = &price.Quote{Token: tok, USDPerToken: 1, USDPerBaseUnit: "1/1000000", Decimals: 6}
		buckets = append(buckets, store.DefiBucket{Bucket: "2026-09-02", Token: tok, Delta: 1_000_000, In: 1_000_000})
		held[tok] = 1_000_000
		assets = append(assets, defiAssetRow{Token: tok, Symbol: "T", Priced: true, Fungible: true})
	}
	s := buildDefiSeries(pr, buckets, held, true, assets, "day")
	if len(s.Assets) == 0 || s.Assets[0].Token != gns {
		t.Fatalf("first band = %+v, want GNS: it peaked at $50 and the rest at $1", s.Assets)
	}
	if s.Assets[len(s.Assets)-1].Token != "other" {
		t.Errorf("seven priced assets and no other band: %+v", s.Assets)
	}
}
