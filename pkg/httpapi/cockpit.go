package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/gnoverse/gnoscope/pkg/config"
	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
	"github.com/gnoverse/gnoscope/pkg/syncer"
	"github.com/gnoverse/gnoscope/pkg/traffic"
)

// The cockpit: one answer to the two questions an operator has at once, which
// is whether gnoscope is healthy and whether the chain is moving.
//
// Everything here already existed, in six places. /sanity knows the chain
// diagnosis, /traffic knows the request volume, /storage knows the footprint,
// the rail's about block knows the build, the syncer knows whether its passes
// land, and nothing joined them, so answering "is it all fine" meant opening
// six pages and holding the answers in your head. This endpoint is that join
// and nothing else: no new table, no new index, no figure computed a second
// way. Where a number here disagrees with the page that owns it, the page is
// right and this is the bug.
//
// It is one endpoint rather than six because of what reads it. /cockpit paints
// one screen and six round trips would be six chances to paint half of it;
// /widget is a wall display polling on a timer, and a verdict assembled in the
// browser from six responses is a verdict that differs between the two pages
// the moment one of them lands late. The grading is therefore done here, once,
// and both pages draw the same findings in the same order.

// BuildInfo is what the linker stamped in, plus when this process started.
//
// Held here so /api/version and /api/cockpit cannot drift: the version endpoint
// used to build its payload in main.go, and the cockpit would have been a
// second copy of the same six fields.
type BuildInfo struct {
	GitHash   string
	BuildTime string
	Version   string
	GoVersion string
	// StartedAt is the only one not fixed at link time, and the only one that
	// answers whether the instance in front of you has been restarted since
	// whatever you are reporting happened.
	StartedAt time.Time
}

// SetBuildInfo hands the API the facts main.go holds.
//
// A setter, like SetSyncHealth and for the same reason: the values come from
// -ldflags variables that live in package main, and every test and tool that
// constructs an API supplies none of them. The zero value is a build that says
// it does not know, which is the truth in those cases.
func (a *API) SetBuildInfo(b BuildInfo) { a.build = b }

// service renders the build facts for a response, with uptime computed now.
//
// Uptime is derived rather than sent as a timestamp alone so a reader with a
// skewed clock still gets the right number; started_at goes out beside it for
// anything that wants to tick it.
func (a *API) service() CockpitService {
	b := a.build
	if b.GoVersion == "" {
		b.GoVersion = runtime.Version()
	}
	s := CockpitService{
		GitHash:   orUnknown(b.GitHash),
		Version:   orUnknown(b.Version),
		BuildTime: orUnknown(b.BuildTime),
		GoVersion: b.GoVersion,
	}
	if !b.StartedAt.IsZero() {
		s.StartedAt = b.StartedAt.UTC().Format(time.RFC3339)
		s.UptimeSeconds = int64(time.Since(b.StartedAt).Seconds())
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// HandleVersion answers /api/version.
//
// Registered here rather than in main.go, which is where it lived until the
// cockpit needed the same facts: a route registered on the mux directly is also
// a route missing from /api/endpoints, which is the one list that claims to be
// complete.
func (a *API) HandleVersion(w http.ResponseWriter, r *http.Request) {
	s := a.service()
	JSONResponse(w, map[string]any{
		"git_hash":   s.GitHash,
		"build_time": s.BuildTime,
		"version":    s.Version,
		"go_version": s.GoVersion,
		"started_at": s.StartedAt,
		// Uptime in seconds, beside the timestamp above: see service().
		"uptime_seconds": s.UptimeSeconds,
		// shots tells the frontend whether to put any realm <img> on the page.
		// It asks once and decides for the whole session, so a deployment
		// without a capture service never requests an image it cannot get.
		"shots": a.ShotsEnabled(),
	})
}

// Where the thresholds live, printed in every finding that uses one.
//
// Same rule as alerts.go: a threshold a reader cannot see is an assertion, and
// the first thing anyone asks of "the indexer is behind" is how far behind
// counts as behind.
const (
	// cockpitLagWarn and cockpitLagDown are how many blocks the stored tip may
	// trail the chain head by. gno.land blocks are a few seconds apart, so 50
	// is a couple of minutes of catching up, which a sync pass does routinely,
	// and 1000 is most of an hour, which it does not.
	cockpitLagWarn = 50
	cockpitLagDown = 1000

	// cockpitSyncFailDown is how many consecutive failed passes stop being a
	// bad minute and start being an outage. One failure is a flap and the
	// registry already reports it as a warning.
	cockpitSyncFailDown = 5

	// cockpitErrorRateWarn is the share of served requests that may answer 4xx
	// or 5xx before it is worth a look, and cockpitErrorRateMin is the volume
	// below which the share means nothing: one 404 out of three requests is
	// 33% and is not a finding.
	cockpitErrorRateWarn = 0.05
	cockpitErrorRateMin  = 200

	// cockpitColdStart is how long a fresh process is given before "nothing is
	// indexed yet" is reported as a problem rather than as a backfill in
	// progress. The first pass of a cold database legitimately has no block
	// stored for a while.
	cockpitColdStart = 10 * time.Minute

	// cockpitWindow is the span every counter on this page is over. One window
	// for all of them, because the page's whole claim is that its numbers can
	// be read side by side.
	cockpitWindow = 24 * time.Hour
)

// Verdict states. Three, because they are three different days: nothing to do,
// something to look at when convenient, and something that is broken now.
const (
	CockpitOK       = "ok"
	CockpitDegraded = "degraded"
	CockpitDown     = "down"
)

// CockpitService is the build in front of you.
type CockpitService struct {
	GitHash       string `json:"git_hash"`
	Version       string `json:"version"`
	BuildTime     string `json:"build_time"`
	GoVersion     string `json:"go_version"`
	StartedAt     string `json:"started_at,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// CockpitNetwork is one chain's health as both halves see it: what the node
// says, what the indexer says, and how far behind our own copy is.
type CockpitNetwork struct {
	Network   string         `json:"network"`
	Diagnosis ChainDiagnosis `json:"diagnosis"`

	// ChainHeight is the tip the indexer reports, IndexedHeight the newest
	// block this instance has stored. Indexed is false when it has stored
	// none, which is a real state (a cold database, or block storage switched
	// off) and not the same as height zero.
	ChainHeight   int  `json:"chain_height"`
	IndexedHeight int  `json:"indexed_height"`
	Indexed       bool `json:"indexed"`
	// Reachable is the indexer's, not the node's: the node has its own entry
	// inside Diagnosis. False means the chain head is unknown, so LagBlocks is
	// absent rather than zero.
	Reachable bool `json:"reachable"`
	// LagBlocks is only meaningful when both heights are known, and LagSeconds
	// is the age of the newest stored block, which is readable even when the
	// indexer is gone.
	LagBlocks  int `json:"lag_blocks"`
	LagSeconds int `json:"lag_seconds"`

	LastBlockTime     string `json:"last_block_time,omitempty"`
	SecondsSinceBlock int    `json:"seconds_since_block"`

	// Sync is this process's own record of whether its passes for this network
	// land. Absent when no sync loop runs here.
	Sync *syncer.NetworkHealth `json:"sync,omitempty"`

	// Error is why the indexed tip could not be read, when it could not be.
	// Same shape and the same reason as HeartbeatRow.Error: a failed local
	// read and an empty table are indistinguishable in the fields above, and
	// reporting the first as the second would grade a database problem as
	// "nothing has been synced yet".
	Error string `json:"error,omitempty"`
}

// CockpitChain is the pulse of the one chain the page is about.
//
// One chain, never a blend: there is no such thing as the height of four chains
// at once, and the same goes for every counter beside it. Which chain is the
// usual ?network=, defaulting to the first configured one.
type CockpitChain struct {
	Network string `json:"network"`
	Height  int    `json:"height"`

	// Each counter is over cockpitWindow, with Prev the equal window before it
	// so a figure can be read as a direction and not only as a number.
	Txs             int  `json:"txs"`
	PrevTxs         int  `json:"prev_txs"`
	Calls           int  `json:"calls"`
	FailedCalls     int  `json:"failed_calls"`
	ActiveAddresses int  `json:"active_addresses"`
	PrevActive      int  `json:"prev_active_addresses"`
	NewPackages     int  `json:"new_packages"`
	PrevNewPackages int  `json:"prev_new_packages"`
	HasPrev         bool `json:"has_prev"`

	// StorageBytes is the chain's whole footprint and StorageBytesWindow what
	// it gained inside the window. The second can be negative: a window that
	// freed more than it stored is an answer, not a bug.
	StorageBytes       int64 `json:"storage_bytes"`
	StorageBytesWindow int64 `json:"storage_bytes_window"`
	StorageRealms      int   `json:"storage_realms"`
}

// CockpitRequests is what this instance served, from its own request log.
//
// Absent entirely when -traffic-db is unset, which is the default: an explorer
// that records nothing should report nothing rather than report zeroes that
// read as "nobody came".
type CockpitRequests struct {
	Requests  int64   `json:"requests"`
	Visitors  int64   `json:"visitors"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	CacheHits int64   `json:"cache_hits"`
	// Dropped is the writer's own counter and is not windowed: rows the buffer
	// threw away under load, since this process started. Non-zero means the
	// log is incomplete and every figure above understates.
	Dropped int64 `json:"dropped"`
}

// CockpitFinding is one thing that is wrong, named.
//
// Named is the whole point. "degraded" on a wall display tells a passer-by
// nothing they can act on; "gnoland1 is wedged" sends them to the right person.
type CockpitFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Network  string `json:"network,omitempty"`
	// Title is the finding in a handful of words, sized for a television.
	Title string `json:"title"`
	// Detail is the same finding with its numbers and its threshold.
	Detail string `json:"detail"`
	// Href is the page that owns the number, so /cockpit can link every line.
	Href string `json:"href,omitempty"`
}

// CockpitVerdict is the one sentence, plus the counts behind it.
type CockpitVerdict struct {
	State    string `json:"state"`
	Headline string `json:"headline"`
	Down     int    `json:"down"`
	Warn     int    `json:"warn"`
}

type cockpitResponse struct {
	GeneratedAt string `json:"generated_at"`
	// WindowSeconds is what every counter under Chain and Requests is over,
	// stated rather than implied: this response is cached and may be served a
	// minute old, and a reader can only tell what "24 hours" ended when if the
	// boundaries are on the page.
	WindowSeconds int              `json:"window_seconds"`
	Verdict       CockpitVerdict   `json:"verdict"`
	Findings      []CockpitFinding `json:"findings"`
	Service       CockpitService   `json:"service"`
	Networks      []CockpitNetwork `json:"networks"`
	Chain         CockpitChain     `json:"chain"`
	Requests      *CockpitRequests `json:"requests,omitempty"`
}

// HandleCockpit assembles the roll-up.
//
// Phased, because it joins three readers of wildly different cost — a node
// probe per network, a pile of range scans, and the request log — and the
// first question about a slow reading will be which of them was slow. See
// pkg/httpapi/observability.go.
func (a *API) HandleCockpit(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	resp := cockpitResponse{
		GeneratedAt:   now.Format(time.RFC3339),
		WindowSeconds: int(cockpitWindow / time.Second),
		Service:       a.service(),
	}

	tNets := time.Now()
	resp.Networks = a.cockpitNetworks(r.Context(), now)
	Phase(r.Context(), "networks", tNets)

	network := a.singleNetwork(r)
	tChain := time.Now()
	chain, err := a.cockpitChain(network, now, resp.Networks)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp.Chain = chain
	Phase(r.Context(), "chain", tChain)

	tReq := time.Now()
	resp.Requests = a.cockpitRequests(now)
	Phase(r.Context(), "requests", tReq)

	resp.Findings = cockpitFindings(resp)
	resp.Verdict = cockpitVerdict(resp.Findings)
	JSONResponse(w, resp)
}

// cockpitNetworks reports every configured chain, never only the selected one.
//
// Same reason as the heartbeat strip: a chain is most interesting here
// precisely when it is not the one you were looking at, and an operator asking
// "is anything broken" has not already decided where.
func (a *API) cockpitNetworks(ctx context.Context, now time.Time) []CockpitNetwork {
	type netLive struct {
		id   string
		live store.SanityLiveness
	}
	results := fanOut(ctx, a.networks, a.clients, a.health,
		func(ctx context.Context, n config.NetworkConfig, c *indexer.Client) (netLive, error) {
			return netLive{id: n.ID, live: livenessOf(ctx, c)}, nil
		})
	live := make(map[string]store.SanityLiveness, len(a.networks))
	for _, res := range results {
		live[res.id] = res.live
	}
	// fanOut drops the networks it skipped — no client, or an open breaker —
	// and this is the view whose job is to report exactly those. Fill the gaps
	// in as unreachable rather than letting a chain vanish from the list.
	for _, n := range a.networks {
		if _, ok := live[n.ID]; !ok {
			live[n.ID] = store.SanityLiveness{}
		}
	}

	_, diags := a.probeNetworks(ctx, a.networks, live)
	sync := a.syncHealth.Snapshot()

	out := make([]CockpitNetwork, 0, len(a.networks))
	for _, n := range a.networks {
		l := live[n.ID]
		row := CockpitNetwork{
			Network:           n.ID,
			Diagnosis:         diags[n.ID],
			ChainHeight:       l.ChainHeight,
			Reachable:         l.Reachable,
			LastBlockTime:     l.LastBlockTime,
			SecondsSinceBlock: l.SecondsSinceBlock,
		}
		if h, ok := sync[n.ID]; ok {
			row.Sync = &h
		}
		height, blockTime, indexed, err := a.db.IndexedTip(n.ID)
		if err != nil {
			row.Error = err.Error()
		}
		if err == nil && indexed {
			row.Indexed, row.IndexedHeight = true, height
			if l.Reachable && l.ChainHeight > height {
				row.LagBlocks = l.ChainHeight - height
			}
			if t, perr := time.Parse(time.RFC3339, blockTime); perr == nil {
				// Clamped at zero for the same reason the consensus round age
				// is: a node a shade ahead of our clock would otherwise put a
				// negative number on the page, which reads as a bug here
				// rather than as the rounding it is.
				if secs := int(now.Sub(t).Seconds()); secs > 0 {
					row.LagSeconds = secs
				}
			}
		}
		out = append(out, row)
	}
	return out
}

// cockpitChain reads the pulse of one chain.
func (a *API) cockpitChain(network string, now time.Time, nets []CockpitNetwork) (CockpitChain, error) {
	out := CockpitChain{Network: network}
	for _, n := range nets {
		if n.Network != network {
			continue
		}
		// The indexer's tip where there is one, ours where there is not: a
		// height that disappears because somebody else's service is down is
		// worse than a height that is a few blocks old and says so.
		out.Height = n.ChainHeight
		if out.Height == 0 {
			out.Height = n.IndexedHeight
		}
	}
	if network == "" {
		return out, nil
	}

	since := now.Add(-cockpitWindow)
	window, prev, hasPrev, err := a.db.GetPulseCounts(store.PulseParams{
		Network:   network,
		Since:     since.Format(time.RFC3339),
		PrevSince: since.Add(-cockpitWindow).Format(time.RFC3339),
	})
	if err != nil {
		return out, fmt.Errorf("pulse counts: %w", err)
	}
	out.Txs, out.PrevTxs = window.Txs, prev.Txs
	out.Calls, out.FailedCalls = window.Calls, window.FailedCalls
	out.ActiveAddresses, out.PrevActive = window.ActiveAddrs, prev.ActiveAddrs
	out.NewPackages, out.PrevNewPackages = window.NewPackages, prev.NewPackages
	out.StorageBytesWindow = window.StorageBytes
	out.HasPrev = hasPrev

	foot, err := a.db.StorageFootprintTotal(network)
	if err != nil {
		return out, fmt.Errorf("storage footprint: %w", err)
	}
	out.StorageBytes, out.StorageRealms = int64(foot.Bytes), foot.Realms
	return out, nil
}

// cockpitRequests reads this instance's own request log, or nothing at all.
func (a *API) cockpitRequests(now time.Time) *CockpitRequests {
	if a.traffic == nil {
		return nil
	}
	totals, err := a.traffic.Totals(traffic.Query{
		Window: traffic.Window{Key: "24h", Duration: cockpitWindow},
		// Crawlers excluded, which is traffic's own default: an error rate
		// computed over scanner traffic is a measure of the internet, not of
		// this instance.
		Who: traffic.WhoNonCrawlers,
		Now: now,
	})
	if err != nil {
		// Best-effort by construction. The request log is a side observation
		// about this process, and failing to read it must not take down a page
		// whose subject is whether the process is healthy.
		return nil
	}
	out := &CockpitRequests{
		Requests:  totals.Requests,
		Visitors:  totals.Visitors,
		Errors:    totals.Errors,
		CacheHits: totals.CacheHits,
		Dropped:   a.traffic.Stats().Dropped,
	}
	if totals.Requests > 0 {
		out.ErrorRate = float64(totals.Errors) / float64(totals.Requests)
	}
	return out
}

// cockpitFindings grades the response. Nothing is computed here that is not
// already in it, so /cockpit can show the finding beside the number it came
// from and the two cannot disagree.
//
// The order the rules are evaluated in is the order they are reported in within
// a severity: the chain first, because a chain that has stopped is the one
// thing nobody needs any other number to act on.
func cockpitFindings(resp cockpitResponse) []CockpitFinding {
	var out []CockpitFinding

	for _, n := range resp.Networks {
		if n.Diagnosis.State != "" && !n.Diagnosis.Healthy {
			out = append(out, CockpitFinding{
				ID: "chain", Severity: cockpitChainSeverity(n.Diagnosis.State), Network: n.Network,
				Title:  cockpitChainTitle(n.Network, n.Diagnosis.State),
				Detail: n.Diagnosis.Detail,
				Href:   "/sanity?network=" + n.Network,
			})
		}
	}

	for _, n := range resp.Networks {
		switch {
		case n.Error != "":
			out = append(out, CockpitFinding{
				ID: "index-unreadable", Severity: CockpitDown, Network: n.Network,
				Title:  "the index cannot be read for " + n.Network,
				Detail: "reading the stored tip failed: " + n.Error,
				Href:   "/sanity?network=" + n.Network,
			})
		case !n.Indexed:
			// A cold process has legitimately stored nothing yet, so this only
			// becomes a finding once the backfill has had a while.
			if resp.Service.UptimeSeconds < int64(cockpitColdStart.Seconds()) {
				continue
			}
			out = append(out, CockpitFinding{
				ID: "not-indexed", Severity: CockpitWarnSeverity, Network: n.Network,
				Title: "no " + n.Network + " block is indexed",
				Detail: fmt.Sprintf("this instance has stored no block for %s, %s after it started",
					n.Network, humanDuration(int(resp.Service.UptimeSeconds))),
				Href: "/sanity?network=" + n.Network,
			})
		case n.LagBlocks >= cockpitLagWarn:
			sev := CockpitWarnSeverity
			if n.LagBlocks >= cockpitLagDown {
				sev = CockpitDown
			}
			out = append(out, CockpitFinding{
				ID: "lag", Severity: sev, Network: n.Network,
				Title: fmt.Sprintf("%s is %s blocks behind", n.Network, fmtThousands(int64(n.LagBlocks))),
				Detail: fmt.Sprintf("the index is at %s and the chain at %s, a gap of %s blocks and %s; "+
					"a gap of %d blocks is worth a look and %d is an outage",
					fmtThousands(int64(n.IndexedHeight)), fmtThousands(int64(n.ChainHeight)),
					fmtThousands(int64(n.LagBlocks)), humanDuration(n.LagSeconds),
					cockpitLagWarn, cockpitLagDown),
				Href: "/sanity?network=" + n.Network,
			})
		}
	}

	for _, n := range resp.Networks {
		if n.Sync == nil || n.Sync.ConsecutiveFailures == 0 {
			continue
		}
		sev := CockpitWarnSeverity
		if n.Sync.ConsecutiveFailures >= cockpitSyncFailDown {
			sev = CockpitDown
		}
		detail := fmt.Sprintf("%d sync passes in a row have failed for %s (%d is an outage)",
			n.Sync.ConsecutiveFailures, n.Network, cockpitSyncFailDown)
		if n.Sync.LastError != "" {
			detail += ": " + n.Sync.LastError
		}
		out = append(out, CockpitFinding{
			ID: "sync", Severity: sev, Network: n.Network,
			Title:  "sync is failing on " + n.Network,
			Detail: detail,
			Href:   "/sanity?network=" + n.Network,
		})
	}

	if rq := resp.Requests; rq != nil {
		if rq.Requests >= cockpitErrorRateMin && rq.ErrorRate > cockpitErrorRateWarn {
			out = append(out, CockpitFinding{
				ID: "errors", Severity: CockpitWarnSeverity,
				Title: fmt.Sprintf("%.1f%% of requests are failing", 100*rq.ErrorRate),
				Detail: fmt.Sprintf("%s of %s requests in the last 24 hours answered 4xx or 5xx; "+
					"the rule is more than %.0f%% over at least %d requests, crawlers excluded",
					fmtThousands(rq.Errors), fmtThousands(rq.Requests), 100*cockpitErrorRateWarn, cockpitErrorRateMin),
				Href: "/traffic",
			})
		}
		if rq.Dropped > 0 {
			out = append(out, CockpitFinding{
				ID: "log-dropped", Severity: CockpitWarnSeverity,
				Title: "the request log is dropping rows",
				Detail: fmt.Sprintf("%s rows have been dropped under load since this process started, "+
					"so every traffic figure here understates", fmtThousands(rq.Dropped)),
				Href: "/traffic",
			})
		}
	}

	// Stable, so the rule order above survives the ranking and two readers a
	// second apart see the same list in the same order.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Severity == CockpitDown && out[j].Severity != CockpitDown
	})
	return out
}

// CockpitWarnSeverity is the severity of a finding worth a look but not an
// outage. Exported alongside the three verdict states so a caller grading its
// own display has the same four words this does.
const CockpitWarnSeverity = "warn"

// cockpitChainSeverity splits the diagnosis states by whether the explorer has
// stopped working or has only stopped being able to see.
//
// The one worth arguing about is node_unreachable, which is a warning. It means
// we cannot *ask* the node, not that anything has stopped: the indexer is still
// answering, history and search still work, and the chain may be perfectly
// fine. Grading it an outage would also paint a permanent red banner on every
// instance run without -rpc, which is most local ones, and a page that is
// always red is a page nobody reads.
func cockpitChainSeverity(state string) string {
	switch state {
	case DiagWedged, DiagIndexerDown, DiagUnknown:
		return CockpitDown
	default:
		return CockpitWarnSeverity
	}
}

// cockpitChainTitle names the finding. The diagnosis states are named for who
// has to act (see chainhealth.go), and two of them are not adjectives, so this
// writes the whole clause rather than appending a word to "<network> is".
func cockpitChainTitle(network, state string) string {
	switch state {
	case DiagWedged:
		return network + " is wedged"
	case DiagStale:
		return network + " has stopped producing blocks"
	case DiagSyncing:
		return network + "'s node is catching up"
	case DiagIndexerDown:
		return network + "'s indexer is not answering"
	case DiagNodeUnreachable:
		return network + "'s node is not answering"
	default:
		return network + " cannot be reached at all"
	}
}

// cockpitVerdict is the one sentence, and it counts rather than names.
//
// The naming is the findings' job, and it is right under this on both pages. A
// headline that also named the worst one would say the same thing twice on the
// common case of a single finding, and would have to pick a winner on the case
// where that choice matters least.
func cockpitVerdict(findings []CockpitFinding) CockpitVerdict {
	v := CockpitVerdict{State: CockpitOK, Headline: "all clear"}
	for _, f := range findings {
		if f.Severity == CockpitDown {
			v.Down++
		} else {
			v.Warn++
		}
	}
	switch {
	case v.Down > 0:
		v.State = CockpitDown
		v.Headline = cockpitCount(v.Down, "thing is down", "things are down")
	case v.Warn > 0:
		v.State = CockpitDegraded
		v.Headline = cockpitCount(v.Warn, "thing needs a look", "things need a look")
	}
	return v
}

// cockpitCount writes "one thing is down" or "3 things are down". Not badges.go's
// plural(), which agrees a noun with its count; here the whole clause changes.
func cockpitCount(n int, one, many string) string {
	if n == 1 {
		return "one " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
