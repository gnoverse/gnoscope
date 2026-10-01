package store

import "testing"

// The unified history is only worth having if a swap reads as one row with
// both sides on it, and if the native half counts every send exactly once.
func TestDefiHistory(t *testing.T) {
	db := NewTestDB(t)
	const (
		net    = "alpha"
		me     = "g1trader"
		pool   = "g1pool"
		friend = "g1friend"
		gns    = "gno.land/r/gnoswap/gns.GNS.0000000"
		wug    = "gno.land/r/gnoland/wugnot.wugnot.0000000"
		gnft   = "gno.land/r/gnoswap/gnft.GNFT.0000000"
	)
	day1 := "2026-09-01T10:00:00Z"
	day2 := "2026-09-02T10:00:00Z"
	day3 := "2026-09-03T10:00:00Z"

	mustCall := func(hash string, h int, when, pkg, fn string) {
		t.Helper()
		if err := db.InsertCall(net, hash, h, 0, when, me, pkg, fn, "", "", true); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertTransaction(net, hash, h, when, 0, 0, 1000, true); err != nil {
			t.Fatal(err)
		}
	}
	mustTok := func(hash string, idx int, h int, when, tok, from, to string, v int64) {
		t.Helper()
		if err := db.InsertTokenTransfer(net, hash, idx, TokenTransfer{
			Token: tok, From: from, To: to, Value: v, BlockHeight: h, BlockTime: when,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustCoin := func(hash string, idx, h int, when, from, to string, ug int64) {
		t.Helper()
		if err := db.InsertCoinTransfer(net, hash, idx, CoinTransfer{
			From: from, To: to, Ugnot: ug, BlockHeight: h, BlockTime: when,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// TXFUND: an early bank send with no TransferEvent, the mainnet case.
	if err := db.InsertBankSend(net, "TXFUND", 1, day1, friend, me, "5000ugnot", true); err != nil {
		t.Fatal(err)
	}
	// TXSEND: a bank send that did emit the event. Counted once, not twice.
	if err := db.InsertBankSend(net, "TXSEND", 2, day1, me, friend, "300ugnot", true); err != nil {
		t.Fatal(err)
	}
	mustCoin("TXSEND", 0, 2, day1, me, friend, 300)
	// A failed send moved nothing.
	if err := db.InsertBankSend(net, "TXFAIL", 3, day1, me, friend, "999ugnot", false); err != nil {
		t.Fatal(err)
	}
	// TXSWAP: wugnot out, GNS in, in one transaction: one row, two legs.
	mustCall("TXSWAP", 10, day2, "gno.land/r/gnoswap/router", "ExactInSwapRoute")
	mustTok("TXSWAP", 0, 10, day2, wug, me, pool, 400)
	mustTok("TXSWAP", 1, 10, day2, gns, pool, me, 9000)
	// TXLP: GNS out, an LP NFT in.
	mustCall("TXLP", 20, day3, "gno.land/r/gnoswap/position", "Mint")
	mustTok("TXLP", 0, 20, day3, gns, me, pool, 1000)
	mustTok("TXLP", 1, 20, day3, gnft, "", me, 0)
	// Somebody else's swap, which must not appear.
	mustTok("TXOTHER", 0, 21, day3, gns, pool, friend, 5)

	total, err := db.DefiTxCount(net, me)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("DefiTxCount = %d, want 4 (fund, send, swap, lp; not the failed send or the stranger's)", total)
	}

	page, err := db.DefiTxs(net, me, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(page), page)
	}
	if page[0].TxHash != "TXLP" || page[3].TxHash != "TXFUND" {
		t.Errorf("order = %s..%s, want newest (TXLP) first and TXFUND last", page[0].TxHash, page[3].TxHash)
	}

	byHash := map[string]DefiTx{}
	for _, tx := range page {
		byHash[tx.TxHash] = tx
	}
	swap := byHash["TXSWAP"]
	if len(swap.Legs) != 2 {
		t.Fatalf("swap legs = %+v, want two", swap.Legs)
	}
	if swap.Legs[0].Token != wug || swap.Legs[0].Delta != -400 {
		t.Errorf("first leg = %+v, want what went out (wugnot -400)", swap.Legs[0])
	}
	if swap.Legs[1].Token != gns || swap.Legs[1].Delta != 9000 {
		t.Errorf("second leg = %+v, want what came in (GNS +9000)", swap.Legs[1])
	}
	if swap.FeeUgnot != 1000 {
		t.Errorf("swap fee = %d, want 1000: this account signed it", swap.FeeUgnot)
	}
	if len(swap.Calls) != 1 || swap.Calls[0].Func != "ExactInSwapRoute" {
		t.Errorf("swap calls = %+v", swap.Calls)
	}

	if l := byHash["TXSEND"].Legs; len(l) != 1 || l[0].Delta != -300 {
		t.Errorf("send legs = %+v, want one -300 ugnot leg, counted once", l)
	}
	if l := byHash["TXFUND"].Legs; len(l) != 1 || l[0].Token != NativeKey || l[0].Delta != 5000 {
		t.Errorf("fund legs = %+v, want +5000 ugnot from bank_sends", l)
	}
	if f := byHash["TXFUND"].FeeUgnot; f != 0 {
		t.Errorf("fund fee = %d, want 0: the sender paid it, not us", f)
	}
	var nft *DefiLeg
	for i, l := range byHash["TXLP"].Legs {
		if l.Token == gnft {
			nft = &byHash["TXLP"].Legs[i]
		}
	}
	if nft == nil || nft.Items != 1 || nft.Delta != 0 {
		t.Errorf("lp nft leg = %+v, want one item in and no amount", nft)
	}

	// Paging is by transaction, and the second page picks up where the first
	// stopped.
	p2, err := db.DefiTxs(net, me, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2) != 2 || p2[0].TxHash != "TXSEND" {
		t.Errorf("page 2 = %+v, want TXSEND then TXFUND", p2)
	}

	buckets, err := db.DefiBuckets(net, me, 10)
	if err != nil {
		t.Fatal(err)
	}
	sums := map[string]int64{}
	for _, b := range buckets {
		sums[b.Bucket+" "+b.Token] += b.Delta
	}
	for key, want := range map[string]int64{
		"2026-09-01 ugnot":  4700, // +5000 -300
		"2026-09-02 " + wug: -400,
		"2026-09-02 " + gns: 9000,
		"2026-09-02 fee":    -1000,
		"2026-09-03 " + gns: -1000,
		"2026-09-03 fee":    -1000,
	} {
		if sums[key] != want {
			t.Errorf("bucket %q = %d, want %d", key, sums[key], want)
		}
	}
	// Gross halves, not just the net: day 1 received 5000 and sent 300.
	for _, b := range buckets {
		if b.Bucket == "2026-09-01" && b.Token == NativeKey && (b.In != 5000 || b.Out != 300) {
			t.Errorf("day 1 ugnot in/out = %d/%d, want 5000/300", b.In, b.Out)
		}
	}
	if _, err := db.DefiBuckets(net, me, 16); err != nil {
		t.Errorf("minute buckets refused: %v", err)
	}
	if _, err := db.DefiBuckets(net, me, 7); err == nil {
		t.Error("a bucket prefix that is neither a day nor an hour was accepted")
	}

	first, last, err := db.DefiSpan(net, me)
	if err != nil || first != day1 || last != day3 {
		t.Errorf("span = %q..%q (%v), want %q..%q", first, last, err, day1, day3)
	}

	// The reconciliation's missing term: only the send with no coin-ledger leg,
	// and never the failed one.
	net2, sends, err := db.UnledgeredBankSends(net, me)
	if err != nil || net2 != 5000 || sends != 1 {
		t.Errorf("UnledgeredBankSends = %d over %d (%v), want 5000 over 1", net2, sends, err)
	}
}
