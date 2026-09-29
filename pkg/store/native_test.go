package store

import (
	"testing"
	"time"
)

// The native coin as an asset, and the two places it is NOT the same shape as a
// GRC20. Both differences are the point of this file: the arithmetic is trivial,
// and what breaks is a query that quietly counts the wrong population.

func nativeFixture(t *testing.T) *DB {
	t.Helper()
	db := NewTestDB(t)
	now := time.Now().UTC()
	day := func(d int) string { return now.AddDate(0, 0, -d).Format(time.RFC3339) }

	sends := []struct {
		hash          string
		height        int
		at            string
		from, to, amt string
		ok            bool
	}{
		{"h1", 10, day(5), "g1alice", "g1bob", "1000ugnot", true},
		{"h2", 20, day(3), "g1alice", "g1carol", "2500ugnot", true},
		{"h3", 30, day(3), "g1bob", "g1alice", "500ugnot", true},
		// Within 24h, so it counts toward transfers_24h.
		{"h4", 40, now.Add(-2 * time.Hour).Format(time.RFC3339), "g1carol", "g1bob", "750ugnot", true},
		// A failed send moved nothing. Counting it would make the volume figure
		// describe attempts rather than transfers.
		{"h5", 50, day(1), "g1alice", "g1bob", "99999ugnot", false},
	}
	for _, s := range sends {
		if err := db.InsertBankSend("testnet", s.hash, s.height, s.at, s.from, s.to, s.amt, s.ok); err != nil {
			t.Fatalf("insert bank send: %v", err)
		}
	}

	// The sweep: three addresses read, one of them holding nothing. That gap is
	// what HoldersSwept exists to expose.
	if err := db.UpsertBalances("testnet", []BalanceRow{
		{Address: "g1alice", Network: "testnet", Amount: "8000ugnot", Ugnot: 8000, Height: 50},
		{Address: "g1bob", Network: "testnet", Amount: "1250ugnot", Ugnot: 1250, Height: 50},
		{Address: "g1empty", Network: "testnet", Amount: "0ugnot", Ugnot: 0, Height: 50},
	}); err != nil {
		t.Fatalf("upsert balances: %v", err)
	}
	return db
}

func TestNativeSummary(t *testing.T) {
	db := nativeFixture(t)
	s, err := db.NativeSummary("testnet")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		got, want int64
		why       string
	}{
		{"transfers counts successes only", int64(s.Transfers), 4,
			"the failed send moved nothing"},
		{"volume excludes the failed send", s.Volume, 1000 + 2500 + 500 + 750,
			"99999ugnot never moved and must not be in the total"},
		{"transfers_24h", int64(s.Transfers24h), 1, ""},
		{"holders counts positive balances", int64(s.Holders), 2,
			"g1empty was read and holds nothing"},
		{"holders_swept counts every read", int64(s.HoldersSwept), 3,
			"without this the holder count reads as a chain total"},
		{"unique senders", int64(s.UniqueSenders), 3, ""},
		{"first block", int64(s.FirstBlock), 10, ""},
		{"last block", int64(s.LastBlock), 40, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("= %d, want %d. %s", tt.got, tt.want, tt.why)
			}
		})
	}
}

// Holders and holders-swept must not be the same number, or the honest framing
// collapses into the misleading one it exists to prevent.
func TestNativeSummaryKeepsTheSweepVisible(t *testing.T) {
	db := nativeFixture(t)
	s, _ := db.NativeSummary("testnet")
	if s.HoldersSwept <= s.Holders {
		t.Fatalf("swept %d must exceed holders %d in this fixture; an address that was read and holds nothing is what makes the sample visible",
			s.HoldersSwept, s.Holders)
	}
}

func TestNativeTopHolders(t *testing.T) {
	db := nativeFixture(t)
	got, err := db.NativeTopHolders("testnet", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d holders, want 2 (the zero-balance address is not a holder)", len(got))
	}
	if got[0].Address != "g1alice" || got[0].Balance != 8000 {
		t.Fatalf("rank 1 = %+v, want g1alice with 8000", got[0])
	}
	if got[1].Address != "g1bob" {
		t.Fatalf("rank 2 = %s, want g1bob", got[1].Address)
	}
}

// A bank send always has both ends. An empty from or to means "mint" or "burn"
// on a GRC20 and cannot happen here, so nothing downstream may read these rows
// that way.
func TestNativeTransfersNeverLookLikeMints(t *testing.T) {
	db := nativeFixture(t)
	got, err := db.NativeTransfers("testnet", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d transfers, want 4 successful ones", len(got))
	}
	for _, tr := range got {
		if tr.From == "" || tr.To == "" {
			t.Errorf("%s has an empty end, which a GRC20 reader would take for a mint or burn: %+v", tr.TxHash, tr)
		}
		if tr.Token != NativeKey {
			t.Errorf("token = %q, want %q so one table component can draw both ledgers", tr.Token, NativeKey)
		}
	}
	// Newest first, like the GRC20 list.
	if got[0].BlockHeight != 40 {
		t.Errorf("first row is block %d, want 40: newest first", got[0].BlockHeight)
	}
}

func TestNativeFlowOverTime(t *testing.T) {
	db := nativeFixture(t)
	pts, err := db.NativeFlowOverTime("testnet", 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) == 0 {
		t.Fatal("no points")
	}
	var transfers int
	var volume int64
	ascending := true
	for i, p := range pts {
		transfers += p.Transfers
		volume += p.Volume
		if i > 0 && pts[i-1].Time > p.Time {
			ascending = false
		}
	}
	if transfers != 4 {
		t.Errorf("transfers across the series = %d, want 4", transfers)
	}
	if volume != 4750 {
		t.Errorf("volume across the series = %d, want 4750; the failed send must stay out", volume)
	}
	if !ascending {
		t.Error("points must be oldest-first, the way a chart reads them")
	}
}

// The GRC20 counterpart. Its one judgement call is that a mint counts as a
// transfer and does not count as volume: an issuance is activity, and summing
// it into value moved makes minting look like trading.
func TestTokenFlowOverTimeExcludesMintsFromVolume(t *testing.T) {
	db := NewTestDB(t)
	now := time.Now().UTC()
	at := now.AddDate(0, 0, -1).Format(time.RFC3339)
	const tok = "gno.land/r/demo/tok.TOK.0000000"

	rows := []TokenTransfer{
		// A mint: no sender.
		{Token: tok, PkgPath: "gno.land/r/demo/tok", From: "", To: "g1alice", Value: 1000, TxHash: "m1", BlockHeight: 10, BlockTime: at},
		// Real movement.
		{Token: tok, PkgPath: "gno.land/r/demo/tok", From: "g1alice", To: "g1bob", Value: 300, TxHash: "t1", BlockHeight: 11, BlockTime: at},
		// A burn: no recipient.
		{Token: tok, PkgPath: "gno.land/r/demo/tok", From: "g1bob", To: "", Value: 100, TxHash: "b1", BlockHeight: 12, BlockTime: at},
	}
	for i, r := range rows {
		if err := db.InsertTokenTransfer("testnet", r.TxHash, i, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	pts, err := db.TokenFlowOverTime("testnet", tok, 90)
	if err != nil {
		t.Fatal(err)
	}
	var transfers int
	var volume int64
	for _, p := range pts {
		transfers += p.Transfers
		volume += p.Volume
	}
	if transfers != 3 {
		t.Errorf("transfers = %d, want 3: a mint and a burn are both activity", transfers)
	}
	if volume != 300 {
		t.Errorf("volume = %d, want 300: only the holder-to-holder leg moved value between holders", volume)
	}
}

// An NFT's holders were invisible: its Transfer legs carry no amount, so a
// balance-based reconstruction sums every one of them to zero and the HAVING
// clause drops the row. The page said nobody holds it, with no error to notice.
func TestTopHoldersCountsNFTItems(t *testing.T) {
	db := NewTestDB(t)
	at := time.Now().UTC().Format(time.RFC3339)
	const nft = "gno.land/r/demo/pics.PIC.0000000"

	rows := []TokenTransfer{
		// Three mints to g1collector, one to g1other, all with no amount.
		{Token: nft, From: "", To: "g1collector", Value: 0, TxHash: "n1", BlockHeight: 1, BlockTime: at},
		{Token: nft, From: "", To: "g1collector", Value: 0, TxHash: "n2", BlockHeight: 2, BlockTime: at},
		{Token: nft, From: "", To: "g1collector", Value: 0, TxHash: "n3", BlockHeight: 3, BlockTime: at},
		{Token: nft, From: "", To: "g1other", Value: 0, TxHash: "n4", BlockHeight: 4, BlockTime: at},
		{Token: nft, From: "", To: "g1collector", Value: 0, TxHash: "n5", BlockHeight: 5, BlockTime: at},
		// One handed on, so the counts are not just mint tallies.
		{Token: nft, From: "g1collector", To: "g1other", Value: 0, TxHash: "n6", BlockHeight: 6, BlockTime: at},
	}
	for i, r := range rows {
		if err := db.InsertTokenTransfer("testnet", r.TxHash, i, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := db.TopHolders("testnet", nft, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d holders of an NFT collection, want 2: %+v", len(got), got)
	}
	// g1collector received 4 and sent 1 away; g1other received 1 and 1.
	// Deliberately unequal: with both on 2 the ranking is a tie and the test
	// asserts nothing about the ordering it claims to check.
	if got[0].Address != "g1collector" || got[0].Balance != 3 {
		t.Errorf("rank 1 = %+v, want g1collector holding 3 items", got[0])
	}
	if got[1].Address != "g1other" || got[1].Balance != 2 {
		t.Errorf("rank 2 = %+v, want g1other holding 2 items", got[1])
	}
}

// Ties must break the same way every time. On an NFT collection they are the
// common case, not the edge: item counts are small integers, so most holders
// share one.
func TestTopHoldersBreaksTiesStably(t *testing.T) {
	db := NewTestDB(t)
	at := time.Now().UTC().Format(time.RFC3339)
	const nft = "gno.land/r/demo/pics.PIC.0000000"
	for i, addr := range []string{"g1delta", "g1alpha", "g1charlie", "g1bravo"} {
		r := TokenTransfer{Token: nft, From: "", To: addr, Value: 0,
			TxHash: "tie" + string(rune('a'+i)), BlockHeight: i + 1, BlockTime: at}
		if err := db.InsertTokenTransfer("testnet", r.TxHash, i, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	want := []string{"g1alpha", "g1bravo", "g1charlie", "g1delta"}
	for pass := 0; pass < 3; pass++ {
		got, err := db.TopHolders("testnet", nft, 10)
		if err != nil {
			t.Fatal(err)
		}
		for i := range want {
			if got[i].Address != want[i] {
				t.Fatalf("pass %d: rank %d = %s, want %s (all four hold one item, so the order must come from the address)",
					pass, i+1, got[i].Address, want[i])
			}
		}
	}
}

// And the fungible path must not start counting legs instead of value.
func TestTopHoldersStillSumsValueForAFungibleToken(t *testing.T) {
	db := NewTestDB(t)
	at := time.Now().UTC().Format(time.RFC3339)
	const tok = "gno.land/r/demo/tok.TOK.0000000"

	rows := []TokenTransfer{
		{Token: tok, From: "", To: "g1a", Value: 1000, TxHash: "t1", BlockHeight: 1, BlockTime: at},
		{Token: tok, From: "g1a", To: "g1b", Value: 250, TxHash: "t2", BlockHeight: 2, BlockTime: at},
	}
	for i, r := range rows {
		if err := db.InsertTokenTransfer("testnet", r.TxHash, i, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	got, err := db.TopHolders("testnet", tok, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Balance != 750 || got[1].Balance != 250 {
		t.Fatalf("got %+v, want 750 and 250 by value, not leg counts", got)
	}
}

// The same blind spot in the chain-wide view: an NFT position is a count, and a
// balance-based filter drops every one of them.
func TestAllPositionsKeepsNFTHoldings(t *testing.T) {
	db := NewTestDB(t)
	at := time.Now().UTC().Format(time.RFC3339)
	rows := []TokenTransfer{
		{Token: "gno.land/r/demo/pics.PIC.0000000", PkgPath: "gno.land/r/demo/pics", From: "", To: "g1c", Value: 0, TxHash: "n1", BlockHeight: 1, BlockTime: at},
		{Token: "gno.land/r/demo/tok.TOK.0000000", PkgPath: "gno.land/r/demo/tok", From: "", To: "g1c", Value: 900, TxHash: "t1", BlockHeight: 2, BlockTime: at},
	}
	for i, r := range rows {
		if err := db.InsertTokenTransfer("testnet", r.TxHash, i, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	got, _, err := db.AllPositions("testnet", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d positions, want 2 (one fungible, one NFT): %+v", len(got), got)
	}
	byTok := map[string]Position{}
	for _, p := range got {
		byTok[p.Token] = p
	}
	nft := byTok["gno.land/r/demo/pics.PIC.0000000"]
	if nft.Fungible || nft.Balance != 1 {
		t.Errorf("nft position = %+v, want 1 item and fungible=false", nft)
	}
	tok := byTok["gno.land/r/demo/tok.TOK.0000000"]
	if !tok.Fungible || tok.Balance != 900 {
		t.Errorf("fungible position = %+v, want 900 and fungible=true", tok)
	}
}

// The defi home's chart. Its one judgement call is that fungibility is a
// property of the TOKEN, decided over its whole history, not of the day's rows:
// a collection whose only transfer today happens to be amountless is still
// whatever it has always been, and deciding per day would flip a token between
// series as its traffic changed.
func TestAssetActivityOverTime(t *testing.T) {
	db := NewTestDB(t)
	now := time.Now().UTC()
	d1 := now.AddDate(0, 0, -2).Format(time.RFC3339)
	d2 := now.AddDate(0, 0, -1).Format(time.RFC3339)

	if err := db.InsertBankSend("testnet", "b1", 1, d1, "g1a", "g1b", "1000ugnot", true); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertBankSend("testnet", "b2", 2, d1, "g1b", "g1a", "40ugnot", false); err != nil {
		t.Fatal(err)
	}

	rows := []TokenTransfer{
		// A fungible token: one amountless leg on day 2 must NOT reclassify it.
		{Token: "gno.land/r/demo/tok.TOK.0000000", From: "", To: "g1a", Value: 500, TxHash: "t1", BlockHeight: 3, BlockTime: d1},
		{Token: "gno.land/r/demo/tok.TOK.0000000", From: "g1a", To: "g1b", Value: 0, TxHash: "t2", BlockHeight: 4, BlockTime: d2},
		// A collection: amountless throughout.
		{Token: "gno.land/r/demo/pics.PIC.0000000", From: "", To: "g1a", Value: 0, TxHash: "n1", BlockHeight: 5, BlockTime: d2},
	}
	for i, r := range rows {
		if err := db.InsertTokenTransfer("testnet", r.TxHash, i, r); err != nil {
			t.Fatal(err)
		}
	}

	pts, err := db.AssetActivityOverTime("testnet", 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("got %d days, want 2: %+v", len(pts), pts)
	}
	byDay := map[string]AssetActivityPoint{}
	for _, p := range pts {
		byDay[p.Time] = p
	}
	day1 := byDay[now.AddDate(0, 0, -2).Format("2006-01-02")]
	day2 := byDay[now.AddDate(0, 0, -1).Format("2006-01-02")]

	if day1.NativeTransfers != 1 || day1.NativeVolume != 1000 {
		t.Errorf("day1 native = %d/%d, want 1/1000: the failed send must stay out", day1.NativeTransfers, day1.NativeVolume)
	}
	if day1.GRC20Transfers != 1 || day1.GRC721Transfers != 0 {
		t.Errorf("day1 token split = %d/%d, want 1/0", day1.GRC20Transfers, day1.GRC721Transfers)
	}
	// The whole point: TOK's amountless leg on day 2 still counts as grc20.
	if day2.GRC20Transfers != 1 {
		t.Errorf("day2 grc20 = %d, want 1: a fungible token with one amountless transfer is still fungible", day2.GRC20Transfers)
	}
	if day2.GRC721Transfers != 1 {
		t.Errorf("day2 grc721 = %d, want 1", day2.GRC721Transfers)
	}
	// Day 1: TOK plus the native coin. Day 2: TOK and PIC, no native.
	if day1.ActiveAssets != 2 {
		t.Errorf("day1 active = %d, want 2 (TOK + native)", day1.ActiveAssets)
	}
	if day2.ActiveAssets != 2 {
		t.Errorf("day2 active = %d, want 2 (TOK + PIC)", day2.ActiveAssets)
	}
	if pts[0].Time > pts[1].Time {
		t.Error("points must be oldest-first")
	}
}
