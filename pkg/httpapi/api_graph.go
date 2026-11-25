package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// The two edge-graph endpoints are per-chain and say so rather than answering
// for none.
//
// transfer_edges holds denominated values, which are never summed across
// chains, and an address is a different actor on each chain. The underlying
// queries filter on an exact network, so an empty one matches nothing: without
// this guard an all-networks request returns an empty graph, which reads as "no
// activity" rather than "ask a different question".
func (a *API) graphNetwork(w http.ResponseWriter, r *http.Request) (string, bool) {
	network := a.networkParam(r)
	if network == "" {
		jsonError(w, "graphs are per-chain: add ?network=", 400)
		return "", false
	}
	return network, true
}

// graphProvenance is what every graph payload says about when it is true:
// which chain, and how far along it.
//
// Two heights, because they answer different questions and the gap between
// them is the thing a reader cannot otherwise see. IndexedHeight is the newest
// block this instance has stored, the same reading /api/cockpit calls
// indexed_height, and it is what the answer was computed from. LiveTipHeight
// is the height the /api/live tip frame carries, read from the feed rather
// than asked for again (see liveTip), with the moment the feed last checked
// it. Either is absent when unknown, and no lag is computed here: a gap
// derived from a missing or stale half would be a guess presented as a fact.
type graphProvenance struct {
	ChainID       string `json:"chain_id,omitempty"`
	IndexedHeight int    `json:"indexed_height,omitempty"`
	IndexedTime   string `json:"indexed_time,omitempty"`
	LiveTipHeight int    `json:"live_tip_height,omitempty"`
	LiveTipAt     string `json:"live_tip_checked_at,omitempty"`
}

// provenance stamps a payload. Best effort for the same reason stampParked
// is: a stored-height lookup that fails leaves the field absent, which says
// "unknown", rather than failing an answer that was otherwise computed fine.
func (a *API) provenance(network string) graphProvenance {
	p := graphProvenance{ChainID: a.chainID(network)}
	if h, t, ok, err := a.db.IndexedTip(network); err == nil && ok {
		p.IndexedHeight, p.IndexedTime = h, t
	}
	if h, at := liveTip(network); h > 0 {
		p.LiveTipHeight = h
		if !at.IsZero() {
			p.LiveTipAt = at.Format(time.RFC3339)
		}
	}
	return p
}

// graphWindowEcho is the window a graph was computed over, as the handler
// resolved it. Window and granularity are echoed because they are read:
// granularity decides whether days is capped at 365, so a request for 3650
// days answers for 365 unless it says monthly, and the echo is where that
// shows.
type graphWindowEcho struct {
	Days        int    `json:"days"`
	Window      string `json:"window"`
	Granularity string `json:"granularity"`
}

type transferGraphResponse struct {
	Nodes []store.GraphNode `json:"nodes"`
	Edges []store.GraphEdge `json:"edges"`

	Network string `json:"network"`
	graphWindowEcho
	TopN     int    `json:"top_n"`
	MinValue int64  `json:"min_value"`
	Ego      string `json:"ego"`
	// Hops is echoed only with an ego, since it is read only with one.
	Hops int `json:"hops,omitempty"`
	// Dropped is set whenever ego is: how many addresses the walk reached and
	// the topN budget left out. StoppedAt is the hop after which the budget
	// ran out and the walk stopped short of Hops, absent when it reached its
	// radius. The neighbourhood is complete only when both say so.
	Dropped   *int `json:"dropped,omitempty"`
	StoppedAt int  `json:"stopped_at_hop,omitempty"`
	graphProvenance
}

type callerGraphResponse struct {
	Nodes []store.CallerGraphNode `json:"nodes"`
	Edges []store.CallerGraphEdge `json:"edges"`

	Network string `json:"network"`
	graphWindowEcho
	// Until is the last day of the window, today's date when the caller
	// left it out.
	Until    string `json:"until"`
	TopN     int    `json:"top_n"`
	MinCalls int    `json:"min_calls"`
	Ego      string `json:"ego"`
	Dropped  *int   `json:"dropped,omitempty"`
	graphProvenance
}

func windowEcho(r *http.Request, days int, granularity string) graphWindowEcho {
	return graphWindowEcho{Days: days, Window: r.URL.Query().Get("window"), Granularity: granularity}
}

func (a *API) HandleGraphTransfers(w http.ResponseWriter, r *http.Request) {
	network, ok := a.graphNetwork(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	days, granularity := a.resolveTimeseriesParams(r, network)
	topN, _ := strconv.Atoi(q.Get("topN"))
	minValue, _ := strconv.ParseInt(q.Get("min_value"), 10, 64)
	ego := q.Get("ego")

	// hops is refused rather than clamped, the way until refuses a malformed
	// date: a hops=50 quietly answered at 5 is a neighbourhood that looks
	// like the one asked for and is not.
	hops := 1
	if raw := q.Get("hops"); raw != "" {
		h, err := strconv.Atoi(raw)
		if err != nil || h < 1 || h > store.MaxTransferHops {
			jsonError(w, "hops must be an integer from 1 to 5", 400)
			return
		}
		hops = h
	}

	g, err := a.db.GetTransferGraph(network, days, topN, minValue, ego, hops)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	resp := transferGraphResponse{
		Nodes:           g.Nodes,
		Edges:           g.Edges,
		Network:         network,
		graphWindowEcho: windowEcho(r, days, granularity),
		TopN:            store.GraphTopN(topN, store.DefaultTransferTopN),
		MinValue:        minValue,
		Ego:             ego,
		graphProvenance: a.provenance(network),
	}
	if ego != "" {
		resp.Hops = hops
		resp.Dropped = &g.Dropped
		resp.StoppedAt = g.StoppedAt
	}
	JSONResponse(w, resp)
}

func (a *API) HandleGraphCallers(w http.ResponseWriter, r *http.Request) {
	network, ok := a.graphNetwork(w, r)
	if !ok {
		return
	}
	days, granularity := a.resolveTimeseriesParams(r, network)
	topN, _ := strconv.Atoi(r.URL.Query().Get("topN"))
	minCalls, _ := strconv.Atoi(r.URL.Query().Get("min_calls"))
	ego := r.URL.Query().Get("ego")

	// until=YYYY-MM-DD ends the window on that day instead of today, for the
	// cartography time-lapse. Anything else is refused rather than read as
	// "today": a malformed date silently answering for now would draw a
	// historical picture out of current traffic.
	var until time.Time
	if u := r.URL.Query().Get("until"); u != "" {
		t, err := time.Parse("2006-01-02", u)
		if err != nil {
			jsonError(w, "until must be YYYY-MM-DD", 400)
			return
		}
		until = t
	}

	var (
		g   store.CallerGraph
		err error
	)
	if ego != "" {
		g, err = a.db.GetCallerEgoGraph(network, days, until, ego, topN, minCalls)
	} else {
		g, err = a.db.GetCallerGraphUntil(network, days, until, topN, minCalls)
	}
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	last := until
	if last.IsZero() {
		last = time.Now()
	}
	resp := callerGraphResponse{
		Nodes:           g.Nodes,
		Edges:           g.Edges,
		Network:         network,
		graphWindowEcho: windowEcho(r, days, granularity),
		Until:           last.UTC().Format("2006-01-02"),
		TopN:            store.GraphTopN(topN, store.DefaultCallerTopN),
		MinCalls:        minCalls,
		Ego:             ego,
		graphProvenance: a.provenance(network),
	}
	if ego != "" {
		resp.Dropped = &g.Dropped
	}
	JSONResponse(w, resp)
}

type activePathsResponse struct {
	Network string   `json:"network"`
	Window  string   `json:"window"`
	Paths   []string `json:"paths"`
}

// HandleGraphActive answers "which contracts has anyone touched lately", as a
// path set every graph on the site can filter itself against. Touched means
// called in the window or deployed in it, the same two tests the contracts map
// applies to its own nodes.
//
// It exists because the answer is not derivable from what the graph endpoints
// already return. /api/deps draws the dependency graph out of `dependencies`,
// a table that records what a package imports and nothing about whether the
// chain still runs it; the contracts map computes windowed call counts for its
// own nodes and cannot lend them to anyone else. One indexed DISTINCT serves
// both, and anything added later.
//
// Not routed through graphNetwork: this returns paths, not per-chain
// quantities, so an absent network is the union over every configured chain
// rather than an error. That matches /api/deps, which is the caller and which
// answers for the union too.
func (a *API) HandleGraphActive(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	windowName := r.URL.Query().Get("window")
	since, ok := parseContractWindow(windowName)
	if !ok {
		jsonError(w, "unknown window: use all, 24h, 7d, 30d or 90d", 400)
		return
	}
	if windowName == "" {
		windowName = "all"
	}

	paths, err := a.db.ActivePaths(network, since)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, activePathsResponse{Network: network, Window: windowName, Paths: paths})
}
