package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Edge rollups behind the network graphs.
//
// These are folded incrementally from rows already in SQLite rather than
// recomputed wholesale like RefreshRollups: bank_sends and calls are the source,
// both are already synced, and an edge table keyed by day only ever gains rows.
// A pass reads everything above its own MAX(last_height) and adds to the running
// totals, so re-running one cannot double-count.

type TransferEdgeRow struct {
	FromAddress string
	ToAddress   string
	Day         string // 'YYYY-MM-DD'
	TotalValue  int64  // ugnot, parsed out of bank_sends' decorated amount string
	TxCount     int
	LastHeight  int
}

type CallerEdgeRow struct {
	Caller     string
	PkgPath    string
	Day        string
	Calls      int
	LastHeight int
}

func (d *DB) UpsertTransferEdges(network string, rows []TransferEdgeRow) error {
	if len(rows) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO transfer_edges (network, from_address, to_address, day, total_value, tx_count, last_height)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (network, from_address, to_address, day) DO UPDATE SET
			total_value = total_value + excluded.total_value,
			tx_count    = tx_count + excluded.tx_count,
			last_height = MAX(last_height, excluded.last_height)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		if _, err := stmt.Exec(network, r.FromAddress, r.ToAddress, r.Day, r.TotalValue, r.TxCount, r.LastHeight); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) UpsertCallerEdges(network string, rows []CallerEdgeRow) error {
	if len(rows) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO caller_edges (network, caller, pkg_path, day, calls, last_height)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (network, caller, pkg_path, day) DO UPDATE SET
			calls       = calls + excluded.calls,
			last_height = MAX(last_height, excluded.last_height)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		if _, err := stmt.Exec(network, r.Caller, r.PkgPath, r.Day, r.Calls, r.LastHeight); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// edgesLastHeight is the rollup cursor for one edge table. The table name is a
// constant from the two callers below, never input.
func (d *DB) edgesLastHeight(table, network string) (int, bool, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var h sql.NullInt64
	if err := d.db.QueryRow(
		`SELECT MAX(last_height) FROM `+table+` WHERE network = ?`, network,
	).Scan(&h); err != nil {
		return 0, false, err
	}
	if !h.Valid {
		return 0, false, nil
	}
	return int(h.Int64), true, nil
}

func (d *DB) TransferEdgesLastHeight(network string) (int, bool, error) {
	return d.edgesLastHeight("transfer_edges", network)
}

func (d *DB) CallerEdgesLastHeight(network string) (int, bool, error) {
	return d.edgesLastHeight("caller_edges", network)
}

func (d *DB) RollupBankSendsSince(network string, sinceHeight int) ([]TransferEdgeRow, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT from_address, to_address, date(block_time) AS day,
		       `+amountExpr+`, COUNT(*), MAX(block_height)
		FROM bank_sends
		WHERE network = ? AND block_height > ? AND success = 1
		GROUP BY from_address, to_address, day`, network, sinceHeight)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TransferEdgeRow
	for rows.Next() {
		var r TransferEdgeRow
		var day sql.NullString
		if err := rows.Scan(&r.FromAddress, &r.ToAddress, &day, &r.TotalValue, &r.TxCount, &r.LastHeight); err != nil {
			return nil, err
		}
		// A row whose block_time will not parse yields a NULL day from date();
		// skip it rather than corrupting a real bucket or failing the pass.
		if !day.Valid || day.String == "" {
			continue
		}
		r.Day = day.String
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) RollupCallsSince(network string, sinceHeight int) ([]CallerEdgeRow, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT caller, pkg_path, date(block_time) AS day, COUNT(*), MAX(block_height)
		FROM calls
		WHERE network = ? AND block_height > ? AND success = 1
		GROUP BY caller, pkg_path, day`, network, sinceHeight)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CallerEdgeRow
	for rows.Next() {
		var r CallerEdgeRow
		var day sql.NullString
		if err := rows.Scan(&r.Caller, &r.PkgPath, &day, &r.Calls, &r.LastHeight); err != nil {
			return nil, err
		}
		if !day.Valid || day.String == "" {
			continue
		}
		r.Day = day.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- graphs read out of the edge tables ---
//
// Every query here is scoped to one network. transfer_edges holds denominated
// values, which are never summed across chains, and an address is a different
// actor on each chain — so the graphs are per-chain by construction and the
// handlers reject an all-networks request rather than silently answering for
// none.

type GraphNode struct {
	ID     string `json:"id"`
	Volume int64  `json:"volume"`
}

type GraphEdge struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Value   int64  `json:"value"`
	TxCount int    `json:"tx_count"`
}

type TransferGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	// Dropped is how many addresses an ego expansion reached inside its hop
	// radius and left out because topN was spent, and StoppedAt the hop after
	// which it stopped walking for the same reason, zero when it reached its
	// radius. Not serialised from here: the handler decides when the numbers
	// are meaningful.
	Dropped   int `json:"-"`
	StoppedAt int `json:"-"`
}

const (
	// DefaultTransferTopN and DefaultCallerTopN are what each graph answers
	// with when the caller names no size, and MaxGraphTopN what neither will
	// exceed. Exported so the handlers can echo the size actually used.
	DefaultTransferTopN = 100
	DefaultCallerTopN   = 200
	MaxGraphTopN        = 1000

	// MaxTransferHops bounds an ego neighbourhood's radius. Five hops on a
	// chain where most addresses touch the faucet is already most of the
	// chain; topN is what actually keeps the answer small.
	MaxTransferHops = 5
)

// GraphTopN resolves a requested graph size to the one a query will use.
func GraphTopN(topN, def int) int {
	if topN <= 0 {
		return def
	}
	if topN > MaxGraphTopN {
		return MaxGraphTopN
	}
	return topN
}

// GetTransferGraph is the top-N transfer graph, or with ego set, the
// neighbourhood of one address out to `hops` hops. hops <= 1 is the immediate
// counterparties, which is what ego always meant before hops existed.
func (d *DB) GetTransferGraph(network string, days, topN int, minValue int64, ego string, hops int) (TransferGraph, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	start := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")

	if ego != "" {
		if hops <= 1 {
			return d.egoTransferGraph(network, start, ego, minValue)
		}
		return d.egoTransferNeighbourhood(network, start, ego, minValue, hops, GraphTopN(topN, DefaultTransferTopN))
	}
	return d.topNTransferGraph(network, start, GraphTopN(topN, DefaultTransferTopN), minValue)
}

func (d *DB) topNTransferGraph(network, start string, topN int, minValue int64) (TransferGraph, error) {
	addrRows, err := d.db.Query(`
		SELECT addr, SUM(vol) FROM (
			SELECT from_address AS addr, total_value AS vol FROM transfer_edges WHERE network = ? AND day >= ?
			UNION ALL
			SELECT to_address AS addr, total_value AS vol FROM transfer_edges WHERE network = ? AND day >= ?
		) GROUP BY addr ORDER BY SUM(vol) DESC LIMIT ?`,
		network, start, network, start, topN)
	if err != nil {
		return TransferGraph{}, err
	}
	nodeVol := map[string]int64{}
	var order []string
	for addrRows.Next() {
		var addr string
		var vol int64
		if err := addrRows.Scan(&addr, &vol); err != nil {
			addrRows.Close()
			return TransferGraph{}, err
		}
		nodeVol[addr] = vol
		order = append(order, addr)
	}
	if err := addrRows.Err(); err != nil {
		addrRows.Close()
		return TransferGraph{}, err
	}
	addrRows.Close()

	if len(order) == 0 {
		return TransferGraph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}, nil
	}

	// Parallel-edge collapse: same-pair edges across multiple days in the
	// window sum at read time. Both endpoints must be in the top-N set, or a
	// high-volume node would drag in every low-volume address it ever touched.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(order)), ",")
	args := make([]any, 0, 2+2*len(order)+1)
	args = append(args, network, start)
	for _, a := range order {
		args = append(args, a)
	}
	for _, a := range order {
		args = append(args, a)
	}
	args = append(args, minValue)

	q := fmt.Sprintf(`
		SELECT from_address, to_address, SUM(total_value), SUM(tx_count)
		FROM transfer_edges
		WHERE network = ? AND day >= ? AND from_address IN (%s) AND to_address IN (%s)
		GROUP BY from_address, to_address
		HAVING SUM(total_value) >= ?
		ORDER BY SUM(total_value) DESC`, placeholders, placeholders)

	edges, err := scanTransferEdges(d.db.Query(q, args...))
	if err != nil {
		return TransferGraph{}, err
	}

	nodes := make([]GraphNode, 0, len(order))
	for _, a := range order {
		nodes = append(nodes, GraphNode{ID: a, Volume: nodeVol[a]})
	}
	return TransferGraph{Nodes: nodes, Edges: edges}, nil
}

func (d *DB) egoTransferGraph(network, start, ego string, minValue int64) (TransferGraph, error) {
	edges, err := scanTransferEdges(d.db.Query(`
		SELECT from_address, to_address, SUM(total_value), SUM(tx_count)
		FROM transfer_edges
		WHERE network = ? AND day >= ? AND (from_address = ? OR to_address = ?)
		GROUP BY from_address, to_address
		HAVING SUM(total_value) >= ?
		ORDER BY SUM(total_value) DESC`, network, start, ego, ego, minValue))
	if err != nil {
		return TransferGraph{}, err
	}

	nodeVol := map[string]int64{}
	for _, e := range edges {
		other := e.To
		if e.From != ego {
			other = e.From
		}
		nodeVol[other] += e.Value
		nodeVol[ego] += e.Value
	}

	// The ego node renders even with no matching edges.
	nodes := []GraphNode{{ID: ego, Volume: nodeVol[ego]}}
	others := make([]string, 0, len(nodeVol))
	for addr := range nodeVol {
		if addr != ego {
			others = append(others, addr)
		}
	}
	// Map iteration order is random; sort so the response is stable between
	// identical requests.
	sort.Slice(others, func(i, j int) bool {
		if nodeVol[others[i]] != nodeVol[others[j]] {
			return nodeVol[others[i]] > nodeVol[others[j]]
		}
		return others[i] < others[j]
	})
	for _, addr := range others {
		nodes = append(nodes, GraphNode{ID: addr, Volume: nodeVol[addr]})
	}
	return TransferGraph{Nodes: nodes, Edges: edges}, nil
}

// egoTransferNeighbourhood walks out from ego one hop at a time, and topN is
// the node budget for the whole walk, not per hop.
//
// The first hop is never cut: it is exactly what ego answered before hops
// existed, and an address with more counterparties than topN gets all of them,
// as it always did. From the second hop on, each ring's new addresses are
// ranked by the value linking them to what is already kept, the budget takes
// the top of that list, and the rest are counted in Dropped.
//
// Once the budget is spent the walk stops and StoppedAt says after which hop.
// It does not go on to count what lies further out: that count is the same
// query as fetching it, over a frontier the budget no longer bounds, and the
// budget exists to keep that query from running. So Dropped is what the walk
// saw and cut, and StoppedAt is how a reader knows there was more past it.
//
// An edge is returned when both its ends are kept and at least one end is
// nearer than the radius, the same rule hops=1 follows: the ego's edges, and
// not the edges between two counterparties. So a node's volume is the sum of the
// edges returned for it, as it is at one hop.
//
// Every ring is one query over the frontier, and a ring is only walked while
// fewer than topN nodes are kept, so no frontier holds more than topN
// addresses: at most MaxTransferHops queries of at most MaxGraphTopN
// addresses each, whatever the chain looks like. That includes an ego whose
// first hop alone is over budget: it gets its first hop and nothing more.
// The rows a ring reads are the frontier's degree, which at worst is every
// edge in the window: the same read topNTransferGraph makes to rank its nodes
// on every overview request. A LIMIT on the ring would bound it further only
// by cutting edges without counting them, which is what Dropped exists to
// prevent.
func (d *DB) egoTransferNeighbourhood(network, start, ego string, minValue int64, hops, budget int) (TransferGraph, error) {
	if hops > MaxTransferHops {
		hops = MaxTransferHops
	}
	kept := map[string]bool{ego: true}
	dropped := 0
	type pair struct{ from, to string }
	seen := map[pair]bool{}
	edges := []GraphEdge{}

	stoppedAt := 0
	frontier := []string{ego}
	for hop := 1; hop <= hops && len(frontier) > 0; hop++ {
		ring, err := d.transferEdgesTouching(network, start, frontier, minValue)
		if err != nil {
			return TransferGraph{}, err
		}

		link := map[string]int64{}
		for _, e := range ring {
			for _, end := range [2]string{e.From, e.To} {
				if !kept[end] {
					link[end] += e.Value
				}
			}
		}
		next := make([]string, 0, len(link))
		for addr := range link {
			next = append(next, addr)
		}
		sort.Slice(next, func(i, j int) bool {
			if link[next[i]] != link[next[j]] {
				return link[next[i]] > link[next[j]]
			}
			return next[i] < next[j]
		})
		// A ring past the first is only walked with room left, so room is at
		// least one here, and a cut spends the budget and ends the walk below.
		if room := budget - len(kept); hop > 1 && len(next) > room {
			dropped += len(next) - room
			next = next[:room]
		}
		for _, addr := range next {
			kept[addr] = true
		}

		for _, e := range ring {
			k := pair{e.From, e.To}
			if kept[e.From] && kept[e.To] && !seen[k] {
				seen[k] = true
				edges = append(edges, e)
			}
		}
		if hop < hops && len(kept) >= budget {
			stoppedAt = hop
			break
		}
		frontier = next
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Value != edges[j].Value {
			return edges[i].Value > edges[j].Value
		}
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	vol := map[string]int64{}
	for _, e := range edges {
		vol[e.From] += e.Value
		if e.To != e.From {
			vol[e.To] += e.Value
		}
	}
	others := make([]string, 0, len(kept))
	for addr := range kept {
		if addr != ego {
			others = append(others, addr)
		}
	}
	sort.Slice(others, func(i, j int) bool {
		if vol[others[i]] != vol[others[j]] {
			return vol[others[i]] > vol[others[j]]
		}
		return others[i] < others[j]
	})
	nodes := []GraphNode{{ID: ego, Volume: vol[ego]}}
	for _, addr := range others {
		nodes = append(nodes, GraphNode{ID: addr, Volume: vol[addr]})
	}
	return TransferGraph{Nodes: nodes, Edges: edges, Dropped: dropped, StoppedAt: stoppedAt}, nil
}

// transferEdgesTouching is every windowed edge with at least one end in addrs.
func (d *DB) transferEdgesTouching(network, start string, addrs []string, minValue int64) ([]GraphEdge, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(addrs)), ",")
	args := make([]any, 0, 3+2*len(addrs))
	args = append(args, network, start)
	for _, a := range addrs {
		args = append(args, a)
	}
	for _, a := range addrs {
		args = append(args, a)
	}
	args = append(args, minValue)
	return scanTransferEdges(d.db.Query(fmt.Sprintf(`
		SELECT from_address, to_address, SUM(total_value), SUM(tx_count)
		FROM transfer_edges
		WHERE network = ? AND day >= ? AND (from_address IN (%s) OR to_address IN (%s))
		GROUP BY from_address, to_address
		HAVING SUM(total_value) >= ?`, placeholders, placeholders), args...))
}

func scanTransferEdges(rows *sql.Rows, err error) ([]GraphEdge, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	edges := []GraphEdge{}
	for rows.Next() {
		var e GraphEdge
		if err := rows.Scan(&e.From, &e.To, &e.Value, &e.TxCount); err != nil {
			return nil, err
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

type CallerGraphNode struct {
	ID    string `json:"id"`
	Type  string `json:"type"` // "caller" | "realm"
	Calls int    `json:"calls"`
}

type CallerGraphEdge struct {
	Caller  string `json:"caller"`
	PkgPath string `json:"pkg_path"`
	Calls   int    `json:"calls"`
}

type CallerGraph struct {
	Nodes []CallerGraphNode `json:"nodes"`
	Edges []CallerGraphEdge `json:"edges"`
	// Dropped is how many neighbours an ego graph left out because topN was
	// spent. Same reason as TransferGraph.Dropped for not serialising it.
	Dropped int `json:"-"`
}

// GetCallerGraph is the address-to-realm graph over the last `days` days.
func (d *DB) GetCallerGraph(network string, days, topN, minCalls int) (CallerGraph, error) {
	return d.GetCallerGraphUntil(network, days, time.Time{}, topN, minCalls)
}

// GetCallerGraphUntil is the same graph over the `days` days that end on
// `until` (inclusive), or on today when until is zero.
//
// caller_edges is rolled up per day, so a window that ends in the past costs
// one more bound on the same index and nothing else. It is what lets the
// cartography time-lapse show who was calling what *then*, rather than
// filtering today's callers down to the realms that happened to exist.
func (d *DB) GetCallerGraphUntil(network string, days int, until time.Time, topN, minCalls int) (CallerGraph, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	topN = GraphTopN(topN, DefaultCallerTopN)
	start, last := callerWindow(days, until)

	callerRows, err := d.db.Query(`
		SELECT caller, SUM(calls) FROM caller_edges WHERE network = ? AND day >= ? AND day <= ?
		GROUP BY caller ORDER BY SUM(calls) DESC LIMIT ?`, network, start, last, topN)
	if err != nil {
		return CallerGraph{}, err
	}
	callerCalls := map[string]int{}
	var callers []string
	for callerRows.Next() {
		var c string
		var n int
		if err := callerRows.Scan(&c, &n); err != nil {
			callerRows.Close()
			return CallerGraph{}, err
		}
		callerCalls[c] = n
		callers = append(callers, c)
	}
	if err := callerRows.Err(); err != nil {
		callerRows.Close()
		return CallerGraph{}, err
	}
	callerRows.Close()

	if len(callers) == 0 {
		return CallerGraph{Nodes: []CallerGraphNode{}, Edges: []CallerGraphEdge{}}, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(callers)), ",")
	args := make([]any, 0, 3+len(callers)+1)
	args = append(args, network, start, last)
	for _, c := range callers {
		args = append(args, c)
	}
	args = append(args, minCalls)

	edgeRows, err := d.db.Query(fmt.Sprintf(`
		SELECT caller, pkg_path, SUM(calls)
		FROM caller_edges
		WHERE network = ? AND day >= ? AND day <= ? AND caller IN (%s)
		GROUP BY caller, pkg_path
		HAVING SUM(calls) >= ?
		ORDER BY SUM(calls) DESC`, placeholders), args...)
	if err != nil {
		return CallerGraph{}, err
	}
	defer edgeRows.Close()

	realmCalls := map[string]int{}
	edges := []CallerGraphEdge{}
	for edgeRows.Next() {
		var e CallerGraphEdge
		if err := edgeRows.Scan(&e.Caller, &e.PkgPath, &e.Calls); err != nil {
			return CallerGraph{}, err
		}
		edges = append(edges, e)
		realmCalls[e.PkgPath] += e.Calls
	}
	if err := edgeRows.Err(); err != nil {
		return CallerGraph{}, err
	}

	nodes := make([]CallerGraphNode, 0, len(callers)+len(realmCalls))
	for _, c := range callers {
		nodes = append(nodes, CallerGraphNode{ID: c, Type: "caller", Calls: callerCalls[c]})
	}
	realms := make([]string, 0, len(realmCalls))
	for pkg := range realmCalls {
		realms = append(realms, pkg)
	}
	// Same reason as the transfer graph: stable output for identical requests.
	sort.Slice(realms, func(i, j int) bool {
		if realmCalls[realms[i]] != realmCalls[realms[j]] {
			return realmCalls[realms[i]] > realmCalls[realms[j]]
		}
		return realms[i] < realms[j]
	})
	for _, pkg := range realms {
		nodes = append(nodes, CallerGraphNode{ID: pkg, Type: "realm", Calls: realmCalls[pkg]})
	}
	return CallerGraph{Nodes: nodes, Edges: edges}, nil
}

// callerWindow is the [start, last] day range of a caller graph: `days` days
// ending on until, or on today when until is zero.
func callerWindow(days int, until time.Time) (string, string) {
	end := until
	if end.IsZero() {
		end = time.Now()
	}
	end = end.UTC()
	return end.AddDate(0, 0, -days).Format("2006-01-02"), end.Format("2006-01-02")
}

// GetCallerEgoGraph is one node of the caller graph and its direct
// neighbours: the realms an address called, or the addresses that called a
// realm. Which side ego is on is read from its shape, since a realm is a path
// and an address never contains a slash.
//
// One hop only, and topN is the neighbour budget: a realm everybody calls has
// thousands of callers, and the ones past the budget are counted in Dropped
// rather than returned. The ego's own count is every call it made or took in
// the window, dropped neighbours included, because that number is known
// exactly and leaving part of it out would understate it for no reason.
func (d *DB) GetCallerEgoGraph(network string, days int, until time.Time, ego string, topN, minCalls int) (CallerGraph, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	topN = GraphTopN(topN, DefaultCallerTopN)
	start, last := callerWindow(days, until)

	isRealm := strings.Contains(ego, "/")
	col := "caller"
	if isRealm {
		col = "pkg_path"
	}
	rows, err := d.db.Query(`
		SELECT caller, pkg_path, SUM(calls)
		FROM caller_edges
		WHERE network = ? AND day >= ? AND day <= ? AND `+col+` = ?
		GROUP BY caller, pkg_path
		HAVING SUM(calls) >= ?
		ORDER BY SUM(calls) DESC, caller, pkg_path`, network, start, last, ego, minCalls)
	if err != nil {
		return CallerGraph{}, err
	}
	defer rows.Close()

	edges := []CallerGraphEdge{}
	total, dropped := 0, 0
	for rows.Next() {
		var e CallerGraphEdge
		if err := rows.Scan(&e.Caller, &e.PkgPath, &e.Calls); err != nil {
			return CallerGraph{}, err
		}
		total += e.Calls
		if len(edges) < topN {
			edges = append(edges, e)
		} else {
			dropped++
		}
	}
	if err := rows.Err(); err != nil {
		return CallerGraph{}, err
	}

	egoType, otherType := "caller", "realm"
	if isRealm {
		egoType, otherType = "realm", "caller"
	}
	// The ego is drawn even with no edges, as the transfer ego is.
	nodes := []CallerGraphNode{{ID: ego, Type: egoType, Calls: total}}
	for _, e := range edges {
		other := e.PkgPath
		if isRealm {
			other = e.Caller
		}
		nodes = append(nodes, CallerGraphNode{ID: other, Type: otherType, Calls: e.Calls})
	}
	return CallerGraph{Nodes: nodes, Edges: edges, Dropped: dropped}, nil
}
