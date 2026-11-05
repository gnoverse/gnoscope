package store

import (
	"reflect"
	"testing"
	"time"
)

// The edge rollups are incremental, which is the whole risk: a pass folds rows
// above its own cursor and *adds* to the running totals, so a cursor that does
// not advance turns every re-run into a double count.
func TestEdgeRollupsAreIncrementalAndPerChain(t *testing.T) {
	db := NewTestDB(t)

	when := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)

	send := func(network, hash string, height int, from, to, amount string) {
		t.Helper()
		if err := db.InsertBankSend(network, hash, height, when, from, to, amount, true); err != nil {
			t.Fatalf("InsertBankSend: %v", err)
		}
	}
	call := func(network, hash string, height int, caller, pkg string) {
		t.Helper()
		if err := db.InsertCall(network, hash, height, 0, when, caller, pkg, "Post", "", "", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}

	// Same address pair on two chains: two distinct actors, two distinct edges.
	send("live", "l-1", 10, "g1a", "g1b", "100ugnot")
	send("live", "l-2", 11, "g1a", "g1b", "50ugnot")
	send("other", "o-1", 10, "g1a", "g1b", "900ugnot")
	call("live", "l-1", 10, "g1a", "gno.land/r/demo/boards")
	call("other", "o-1", 10, "g1a", "gno.land/r/demo/boards")

	roll := func(network string) {
		t.Helper()
		last, _, err := db.TransferEdgesLastHeight(network)
		if err != nil {
			t.Fatalf("TransferEdgesLastHeight: %v", err)
		}
		rows, err := db.RollupBankSendsSince(network, last)
		if err != nil {
			t.Fatalf("RollupBankSendsSince: %v", err)
		}
		if err := db.UpsertTransferEdges(network, rows); err != nil {
			t.Fatalf("UpsertTransferEdges: %v", err)
		}
		cLast, _, err := db.CallerEdgesLastHeight(network)
		if err != nil {
			t.Fatalf("CallerEdgesLastHeight: %v", err)
		}
		cRows, err := db.RollupCallsSince(network, cLast)
		if err != nil {
			t.Fatalf("RollupCallsSince: %v", err)
		}
		if err := db.UpsertCallerEdges(network, cRows); err != nil {
			t.Fatalf("UpsertCallerEdges: %v", err)
		}
	}
	roll("live")
	roll("other")

	edgeValue := func(network string) int64 {
		t.Helper()
		g, err := db.GetTransferGraph(network, 7, 0, 0, "", 0)
		if err != nil {
			t.Fatalf("GetTransferGraph: %v", err)
		}
		var total int64
		for _, e := range g.Edges {
			total += e.Value
		}
		return total
	}

	if got := edgeValue("live"); got != 150 {
		t.Errorf("live transfer volume = %d, want 150", got)
	}
	if got := edgeValue("other"); got != 900 {
		t.Errorf("other transfer volume = %d, want 900; the two chains share an address pair and must not merge", got)
	}

	// Re-running with no new rows must change nothing.
	roll("live")
	if got := edgeValue("live"); got != 150 {
		t.Errorf("re-running the rollup double-counted: volume = %d, want 150", got)
	}

	// A new row above the cursor is folded in, and only it.
	send("live", "l-3", 12, "g1a", "g1b", "7ugnot")
	roll("live")
	if got := edgeValue("live"); got != 157 {
		t.Errorf("volume after one new send = %d, want 157", got)
	}
	// And again with nothing new. This is the pass that catches a cursor which
	// fails to advance on the ON CONFLICT path: the first re-run above inserts
	// a fresh row, so only a re-run *after* an incremental update re-reads the
	// whole history and doubles the totals.
	roll("live")
	if got := edgeValue("live"); got != 157 {
		t.Errorf("rollup double-counted after an incremental update: volume = %d, want 157", got)
	}

	// Node volumes, not just edges: the node query ranks addresses by summed
	// volume in its own statement, so an unscoped one blends the chains even
	// while the edge query stays correct.
	g, err := db.GetTransferGraph("live", 7, 0, 0, "", 0)
	if err != nil {
		t.Fatalf("GetTransferGraph: %v", err)
	}
	vol := map[string]int64{}
	for _, n := range g.Nodes {
		vol[n.ID] = n.Volume
	}
	if vol["g1a"] != 157 {
		t.Errorf("g1a node volume = %d, want 157; the other chain's 900 must not be counted", vol["g1a"])
	}

	cg, err := db.GetCallerGraph("live", 7, 0, 0)
	if err != nil {
		t.Fatalf("GetCallerGraph: %v", err)
	}
	if len(cg.Edges) != 1 || cg.Edges[0].Calls != 1 {
		t.Errorf("caller edges = %+v, want one edge with 1 call on live only", cg.Edges)
	}
	var realms, callers int
	for _, n := range cg.Nodes {
		switch n.Type {
		case "realm":
			realms++
		case "caller":
			callers++
		}
	}
	if realms != 1 || callers != 1 {
		t.Errorf("caller graph has %d callers and %d realms, want 1 and 1", callers, realms)
	}

}

// A chain reset has to take the edge rollups with it, and the row count is only
// half of why.
//
// The sync cursor for these tables is MAX(last_height) over their own rows.
// Survive a reset and they hold a dead chain's edges *and* a cursor above the
// replacement chain's tip, so every transfer on the new chain is skipped
// forever: RollupBankSendsSince asks for block_height > 500 on a chain that has
// only reached 40. The graphs would go on showing the old chain, with no error
// anywhere. Wiping the tables resets the cursor to zero as a side effect, which
// is what makes the re-sync from the new genesis work.
func TestChainResetClearsTheEdgeCursor(t *testing.T) {
	db := NewTestDB(t)

	if err := db.UpsertTransferEdges("staging", []TransferEdgeRow{{
		FromAddress: "g1from", ToAddress: "g1to", Day: "2026-01-01",
		TotalValue: 100, TxCount: 2, LastHeight: 500,
	}}); err != nil {
		t.Fatalf("UpsertTransferEdges: %v", err)
	}
	if err := db.UpsertCallerEdges("staging", []CallerEdgeRow{{
		Caller: "g1caller", PkgPath: "gno.land/r/demo/foo", Day: "2026-01-01",
		Calls: 3, LastHeight: 500,
	}}); err != nil {
		t.Fatalf("UpsertCallerEdges: %v", err)
	}

	h, ok, err := db.TransferEdgesLastHeight("staging")
	if err != nil {
		t.Fatalf("TransferEdgesLastHeight: %v", err)
	}
	if !ok || h != 500 {
		t.Fatalf("cursor before reset = (%d, %v), want (500, true)", h, ok)
	}

	if _, err := db.DeleteNetworkData("staging"); err != nil {
		t.Fatalf("DeleteNetworkData: %v", err)
	}

	for _, tc := range []struct {
		name string
		get  func(string) (int, bool, error)
	}{
		{"transfer", db.TransferEdgesLastHeight},
		{"caller", db.CallerEdgesLastHeight},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ok, err := tc.get("staging")
			if err != nil {
				t.Fatalf("cursor: %v", err)
			}
			if ok || h != 0 {
				t.Errorf("cursor after reset = (%d, %v), want (0, false): the new chain's rows would be skipped", h, ok)
			}
		})
	}
}

// The reset is scoped: another chain's rollups are not collateral damage.
func TestChainResetLeavesOtherChainsEdgesAlone(t *testing.T) {
	db := NewTestDB(t)

	for _, net := range []string{"staging", "mainnet"} {
		if err := db.UpsertTransferEdges(net, []TransferEdgeRow{{
			FromAddress: "g1from", ToAddress: "g1to", Day: "2026-01-01",
			TotalValue: 100, TxCount: 1, LastHeight: 500,
		}}); err != nil {
			t.Fatalf("UpsertTransferEdges(%s): %v", net, err)
		}
	}

	if _, err := db.DeleteNetworkData("staging"); err != nil {
		t.Fatalf("DeleteNetworkData: %v", err)
	}

	h, ok, err := db.TransferEdgesLastHeight("mainnet")
	if err != nil {
		t.Fatalf("TransferEdgesLastHeight: %v", err)
	}
	if !ok || h != 500 {
		t.Errorf("mainnet cursor = (%d, %v), want (500, true): staging's reset took another chain's rollup with it", h, ok)
	}
}

// The time-lapse asks who was calling what in the window that ended on a past
// day. A call after that day must not count, and a call inside it must, or the
// settlement replays today's traffic under an old date.
func TestCallerGraphUntilEndsTheWindowOnThatDay(t *testing.T) {
	db := NewTestDB(t)
	if err := db.UpsertCallerEdges("live", []CallerEdgeRow{
		{Caller: "g1early", PkgPath: "gno.land/r/a/x", Day: "2026-09-10", Calls: 5, LastHeight: 1},
		{Caller: "g1early", PkgPath: "gno.land/r/a/x", Day: "2026-09-20", Calls: 7, LastHeight: 2},
		{Caller: "g1late", PkgPath: "gno.land/r/b/y", Day: "2026-09-20", Calls: 9, LastHeight: 3},
		{Caller: "g1ancient", PkgPath: "gno.land/r/a/x", Day: "2026-08-01", Calls: 4, LastHeight: 0},
	}); err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	g, err := db.GetCallerGraphUntil("live", 30, until, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Edges) != 1 || g.Edges[0].Caller != "g1early" || g.Edges[0].Calls != 5 {
		t.Fatalf("edges until 2026-09-12 = %+v, want only g1early with the 5 calls of 09-10", g.Edges)
	}
	// The window is 30 days back from until, so 08-01 is outside it too.
	for _, n := range g.Nodes {
		if n.ID == "g1ancient" || n.ID == "g1late" {
			t.Fatalf("node %s is outside the window ending 2026-09-12", n.ID)
		}
	}
	// Zero until is today, the old behaviour, and sees both later calls.
	now, err := db.GetCallerGraphUntil("live", 3650, time.Time{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(now.Edges) != 3 {
		t.Fatalf("edges until today = %d, want 3", len(now.Edges))
	}
}

// seedNeighbourhood is a small transfer graph with a known shape around g1ego:
//
//	g1ego -> g1a (100), g1b -> g1ego (50)        hop 1
//	g1a -> g1c (70), g1b -> g1f (10)             hop 2
//	g1a -> g1b (5)                               between two hop-1 nodes
//	g1c -> g1d (40)                              hop 3
func seedNeighbourhood(t *testing.T) *DB {
	t.Helper()
	db := NewTestDB(t)
	day := time.Now().UTC().Format("2006-01-02")
	var rows []TransferEdgeRow
	for _, e := range []struct {
		from, to string
		v        int64
	}{
		{"g1ego", "g1a", 100}, {"g1b", "g1ego", 50},
		{"g1a", "g1c", 70}, {"g1b", "g1f", 10},
		{"g1a", "g1b", 5},
		{"g1c", "g1d", 40},
	} {
		rows = append(rows, TransferEdgeRow{FromAddress: e.from, ToAddress: e.to, Day: day, TotalValue: e.v, TxCount: 1, LastHeight: 1})
	}
	if err := db.UpsertTransferEdges("live", rows); err != nil {
		t.Fatal(err)
	}
	return db
}

// hops=1 is what ego answered before hops existed, and stays on that code
// path. The neighbourhood walk has to agree with it at one hop anyway, or the
// two halves of the feature disagree about what a hop is.
func TestEgoNeighbourhoodAtOneHopIsTheEgoGraph(t *testing.T) {
	db := seedNeighbourhood(t)
	start := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")

	old, err := db.egoTransferGraph("live", start, "g1ego", 0)
	if err != nil {
		t.Fatal(err)
	}
	walk, err := db.egoTransferNeighbourhood("live", start, "g1ego", 0, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old, walk) {
		t.Fatalf("one-hop walk disagrees with the ego graph:\n ego  %+v\n walk %+v", old, walk)
	}
	viaAPI, err := db.GetTransferGraph("live", 7, 0, 0, "g1ego", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old, viaAPI) {
		t.Fatalf("hops=1 is not the ego graph:\n ego   %+v\n hops1 %+v", old, viaAPI)
	}
}

func TestEgoNeighbourhood(t *testing.T) {
	db := seedNeighbourhood(t)

	tests := []struct {
		name        string
		hops, topN  int
		wantNodes   []string // ego first, then by volume
		wantEdges   int
		wantDropped int
		wantStopped int
	}{
		{"one hop is the counterparties only", 1, 0, []string{"g1ego", "g1a", "g1b"}, 2, 0, 0},
		// a->b joins two hop-1 nodes and appears once a is a frontier node.
		// Volume is the sum of returned edges: a is 100+70+5, c 70, b 50+10+5.
		{"two hops reaches c and f", 2, 0, []string{"g1ego", "g1a", "g1c", "g1b", "g1f"}, 5, 0, 0},
		{"three hops reaches d", 3, 0, []string{"g1ego", "g1a", "g1c", "g1b", "g1d", "g1f"}, 6, 0, 0},
		{"five hops on a smaller graph stops when the frontier is empty", 5, 0, []string{"g1ego", "g1a", "g1c", "g1b", "g1d", "g1f"}, 6, 0, 0},
		// Budget 4: hop 1 keeps 3, one place left, c (70) beats f (10). The
		// radius is reached, so the walk did not stop early.
		{"topN cuts the ring and counts the cut", 2, 4, []string{"g1ego", "g1a", "g1c", "g1b"}, 4, 1, 0},
		// The budget is spent at hop 2 of 3: the walk stops and says so rather
		// than running a third query to count what it may not draw.
		{"a spent budget stops the walk and says where", 3, 4, []string{"g1ego", "g1a", "g1c", "g1b"}, 4, 1, 2},
		// Hop 1 is never cut, so a budget below it still returns all of hop 1,
		// and the walk stops there: hop 2's frontier would be over budget.
		{"hop one is never cut, and an over-budget hop one is where it stops", 2, 1, []string{"g1ego", "g1a", "g1b"}, 2, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := db.GetTransferGraph("live", 7, tt.topN, 0, "g1ego", tt.hops)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, n := range g.Nodes {
				ids = append(ids, n.ID)
			}
			if !reflect.DeepEqual(ids, tt.wantNodes) {
				t.Errorf("nodes = %v, want %v", ids, tt.wantNodes)
			}
			if len(g.Edges) != tt.wantEdges {
				t.Errorf("edges = %+v, want %d of them", g.Edges, tt.wantEdges)
			}
			if g.Dropped != tt.wantDropped {
				t.Errorf("dropped = %d, want %d", g.Dropped, tt.wantDropped)
			}
			if g.StoppedAt != tt.wantStopped {
				t.Errorf("stopped at hop %d, want %d", g.StoppedAt, tt.wantStopped)
			}
			for _, e := range g.Edges {
				if e.From == "g1c" && e.To == "g1d" && tt.hops < 3 {
					t.Errorf("edge c->d is outside a %d-hop radius", tt.hops)
				}
			}
		})
	}
}

func TestCallerEgoGraph(t *testing.T) {
	db := NewTestDB(t)
	day := time.Now().UTC().Format("2006-01-02")
	if err := db.UpsertCallerEdges("live", []CallerEdgeRow{
		{Caller: "g1a", PkgPath: "gno.land/r/x", Day: day, Calls: 5, LastHeight: 1},
		{Caller: "g1a", PkgPath: "gno.land/r/y", Day: day, Calls: 3, LastHeight: 1},
		{Caller: "g1b", PkgPath: "gno.land/r/x", Day: day, Calls: 9, LastHeight: 1},
		{Caller: "g1c", PkgPath: "gno.land/r/x", Day: day, Calls: 1, LastHeight: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCallerEdges("other", []CallerEdgeRow{
		{Caller: "g1a", PkgPath: "gno.land/r/z", Day: day, Calls: 50, LastHeight: 1},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		ego         string
		topN        int
		want        []CallerGraphNode
		wantDropped int
	}{
		{"an address fans out to the realms it called", "g1a", 0, []CallerGraphNode{
			{ID: "g1a", Type: "caller", Calls: 8},
			{ID: "gno.land/r/x", Type: "realm", Calls: 5},
			{ID: "gno.land/r/y", Type: "realm", Calls: 3},
		}, 0},
		// The ego's count is all of its calls, the dropped neighbour's included.
		{"a realm fans out to its callers, cut at topN", "gno.land/r/x", 2, []CallerGraphNode{
			{ID: "gno.land/r/x", Type: "realm", Calls: 15},
			{ID: "g1b", Type: "caller", Calls: 9},
			{ID: "g1a", Type: "caller", Calls: 5},
		}, 1},
		{"an unknown ego is drawn alone", "g1nobody", 0, []CallerGraphNode{
			{ID: "g1nobody", Type: "caller", Calls: 0},
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := db.GetCallerEgoGraph("live", 7, time.Time{}, tt.ego, tt.topN, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g.Nodes, tt.want) {
				t.Errorf("nodes = %+v, want %+v", g.Nodes, tt.want)
			}
			if len(g.Edges) != len(tt.want)-1 {
				t.Errorf("edges = %+v, want one per neighbour", g.Edges)
			}
			if g.Dropped != tt.wantDropped {
				t.Errorf("dropped = %d, want %d", g.Dropped, tt.wantDropped)
			}
		})
	}
}
