package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gnoverse/gnoscope/pkg/config"
	"github.com/gnoverse/gnoscope/pkg/healthlog"
	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// The availability history: what each chain looked like over the last week,
// rather than what it looks like this second.
//
// /sanity graded a point-in-time probe and kept nothing, so a chain down for
// three days and one that blipped thirty seconds ago drew the same amber dot.
// Measured on the production instance over 24 hours: 38 breaker trips on one
// network, every one of them invisible on the page whose job is to show them.
//
// Three pieces, deliberately separate. pkg/healthlog stores one sample per
// network per probe and derives the runs on read, as a pure function over a
// sample series. probeNetworks below writes the samples, which is why every
// probe path records and none has to remember to. This file turns the runs
// into a strip and a sentence.
//
// The sentence matters as much as the strip. "onyx was unreachable from 07:44
// to 07:49, 5 minutes" is the thing a reader came for, and a row of coloured
// squares makes them count cells to get it.

// SetHealthLog hands the API its availability store.
//
// A setter, like SetTraffic and SetSyncHealth and for the same reason: the API
// is built before the flags that open a second database are resolved, and
// every test and tool that constructs one supplies nothing. Nil is an instance
// without -health-db, and every use of it is nil-safe.
func (a *API) SetHealthLog(s *healthlog.Store) { a.healthLog = s }

// HealthLogEnabled says whether this instance is keeping a history.
func (a *API) HealthLogEnabled() bool { return a.healthLog != nil }

// recordHealthSamples writes one sample per probed network.
//
// Called from probeNetworks, which is the one choke point every health probe
// goes through: the two /api/sanity/overview paths, /api/cockpit and the
// background sampler all reach it. Putting the write anywhere else would mean
// a probe path that silently records nothing, which is how a history ends up
// with holes that look like outages.
//
// The throttle lives in the store (healthlog.MinGap), not here, because the
// callers include an endpoint a wall display polls every few seconds.
func (a *API) recordHealthSamples(nodes map[string]NodeState, diags map[string]ChainDiagnosis, live map[string]store.SanityLiveness) {
	if a.healthLog == nil {
		return
	}
	now := time.Now().UTC()
	for id, diag := range diags {
		if diag.State == "" {
			continue
		}
		node := nodes[id]
		// The node's own height when it answered, the indexer's when it did
		// not. Zero means neither did, which is itself worth storing: a row
		// with a state and no height is a chain nobody could read.
		height := node.Height
		if height == 0 {
			height = int64(live[id].ChainHeight)
		}
		a.healthLog.Record(healthlog.Sample{
			At:      now,
			Network: id,
			State:   diag.State,
			Height:  height,
			Detail:  diag.Detail,
		})
	}
}

// RunHealthSampler probes every network on a timer and records the verdicts.
//
// Without it the history would be a record of when somebody had the page open,
// which is the opposite of useful: an outage at 04:00 is exactly the one
// nobody was watching. The cadence is healthlog.SampleInterval; see the
// constant for why a minute.
//
// It runs the same probe the page runs, rather than a cheaper one of its own.
// A second code path would eventually grade a chain differently from /sanity,
// and then the strip and the dot above it would disagree with nothing to say
// which was right.
func (a *API) RunHealthSampler(ctx context.Context, every time.Duration) {
	if a.healthLog == nil {
		return
	}
	if every <= 0 {
		every = healthlog.SampleInterval
	}
	pass := func() {
		pctx, cancel := context.WithTimeout(ctx, healthSamplerTimeout)
		defer cancel()
		a.chainStates(pctx)
	}
	pass()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		}
	}
}

// healthSamplerTimeout bounds one sampling pass. Generous against
// nodeProbeTimeout and the per-network indexer deadline, because a pass that
// gives up early would record "unknown" for a network that was merely slow.
const healthSamplerTimeout = 30 * time.Second

// chainStates probes every configured network: what the indexer says, what the
// node says, and the verdict joining them.
//
// Extracted so that /api/sanity/overview, /api/cockpit and the sampler all ask
// the same question the same way. Each used to assemble it inline, and the
// third copy would have been the one that drifted.
func (a *API) chainStates(ctx context.Context) (map[string]store.SanityLiveness, map[string]NodeState, map[string]ChainDiagnosis) {
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
	// fanOut drops the networks it skipped (no client, or an open breaker) and
	// those are exactly the ones this view exists to report. Fill the gaps in
	// as unreachable rather than letting a chain vanish from the list.
	for _, n := range a.networks {
		if _, ok := live[n.ID]; !ok {
			live[n.ID] = store.SanityLiveness{}
		}
	}
	nodes, diags := a.probeNetworks(ctx, a.networks, live)
	return live, nodes, diags
}

// The strip. One row per network, one cell per bucket, coloured by the worst
// state seen in that bucket.
//
// Three windows rather than one, the same shape as the heartbeat strip above
// it: the cell count is fixed so the strip is always the same width, and the
// window picks what a cell means. 7d is the default because that is the span
// over which "has this been happening all week" becomes answerable.
type healthWindow struct {
	Name  string
	Span  time.Duration
	Cells int
}

var healthWindows = []healthWindow{
	{"24h", 24 * time.Hour, 144},      // 10 minutes a cell
	{"7d", 7 * 24 * time.Hour, 168},   // one hour a cell
	{"30d", 30 * 24 * time.Hour, 180}, // four hours a cell
}

const defaultHealthWindow = "7d"

func healthWindowByName(name string) (healthWindow, bool) {
	for _, w := range healthWindows {
		if w.Name == name {
			return w, true
		}
	}
	return healthWindow{}, false
}

// maxHealthIncidents bounds the incident list in one response. A flapping
// network can produce hundreds of runs in a week; the counts stay complete and
// only the list is cut, newest kept.
const maxHealthIncidents = 50

// HealthCell is one bucket of one network's strip.
type HealthCell struct {
	Start string `json:"start"`
	// State is the worst state seen in the cell. Empty means the cell holds no
	// sample at all, which the page draws as a third colour: a bucket with no
	// evidence must never render as green or as red.
	State   string `json:"state,omitempty"`
	Samples int    `json:"samples"`
	Healthy int    `json:"healthy"`
}

// HealthSpan is one run of one state, which is what a reader actually reads.
type HealthSpan struct {
	State   string `json:"state"`
	Healthy bool   `json:"healthy"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Seconds int    `json:"seconds"`
	Samples int    `json:"samples"`
	Ongoing bool   `json:"ongoing"`
	Detail  string `json:"detail,omitempty"`
}

// HealthHistoryRow is one network's week.
type HealthHistoryRow struct {
	Network string       `json:"network"`
	Cells   []HealthCell `json:"cells"`

	Samples        int     `json:"samples"`
	HealthySamples int     `json:"healthy_samples"`
	UptimePercent  float64 `json:"uptime_percent"`
	// RecordingSince is the oldest sample this instance holds for the network,
	// over all time rather than over the window. It is what keeps "100%
	// available" honest: over seven days and over the eleven minutes since the
	// process started, that figure reads identically and means nothing alike.
	RecordingSince string `json:"recording_since,omitempty"`
	FirstSample    string `json:"first_sample,omitempty"`
	LastSample     string `json:"last_sample,omitempty"`

	Outages       int `json:"outages"`
	DownSeconds   int `json:"down_seconds"`
	NoDataSeconds int `json:"no_data_seconds"`

	// Current is the run in progress, which may itself be "not being sampled".
	Current *HealthSpan `json:"current,omitempty"`
	// LastOutage answers the question the page could not: when was it last
	// unreachable, and for how long.
	LastOutage *HealthSpan `json:"last_outage,omitempty"`
	// Incidents is every unhealthy or unobserved run in the window, newest
	// first, capped at maxHealthIncidents. Outages above is the full count.
	Incidents []HealthSpan `json:"incidents"`

	// Summary is the sentence. Written here rather than in the browser so that
	// the page, the widget and any agent reading this endpoint say the same
	// thing about the same week.
	Summary string `json:"summary"`
	Error   string `json:"error,omitempty"`
}

type healthHistoryResponse struct {
	// Enabled is false on an instance without -health-db, which records
	// nothing. The rows are still present and still empty, so the page draws
	// its usual shape with an honest note rather than a missing section.
	Enabled bool   `json:"enabled"`
	Note    string `json:"note,omitempty"`

	Window        string   `json:"window"`
	Windows       []string `json:"windows"`
	CellSeconds   int      `json:"cell_seconds"`
	SampleSeconds int      `json:"sample_seconds"`
	RetentionDays int      `json:"retention_days"`
	GeneratedAt   string   `json:"generated_at"`

	Rows []HealthHistoryRow `json:"rows"`
}

const healthLogOffNote = "availability history is off on this instance (-health-db unset), so there is " +
	"nothing to show for any window. The diagnosis above is still live."

// HandleHealthHistory serves the availability strip and the incidents behind it.
//
// Every configured network by default, never only the selected one, for the
// same reason as the heartbeat strip: a chain is most interesting here exactly
// when it is not the one you were looking at. ?network= narrows it for the
// per-network view.
func (a *API) HandleHealthHistory(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("window")
	if name == "" {
		name = defaultHealthWindow
	}
	win, ok := healthWindowByName(name)
	if !ok {
		http.Error(w, "unknown window: use 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	cell := win.Span / time.Duration(win.Cells)

	networks := a.networks
	if id := a.networkParam(r); id != "" {
		if cfg, found := a.networkConfig(id); found {
			networks = []config.NetworkConfig{cfg}
		}
	}

	now := time.Now().UTC()
	resp := healthHistoryResponse{
		Enabled:       a.healthLog != nil,
		Window:        win.Name,
		CellSeconds:   int(cell.Seconds()),
		SampleSeconds: int(healthlog.SampleInterval.Seconds()),
		RetentionDays: a.healthLog.RetentionDays(),
		GeneratedAt:   now.Format(time.RFC3339),
	}
	for _, v := range healthWindows {
		resp.Windows = append(resp.Windows, v.Name)
	}
	if !resp.Enabled {
		resp.Note = healthLogOffNote
	}

	since := now.Add(-win.Span)
	for _, n := range networks {
		resp.Rows = append(resp.Rows, a.healthHistoryRow(n.ID, since, now, cell, win))
	}
	JSONResponse(w, resp)
}

func (a *API) healthHistoryRow(network string, since, now time.Time, cell time.Duration, win healthWindow) HealthHistoryRow {
	row := HealthHistoryRow{
		Network:   network,
		Cells:     make([]HealthCell, 0, win.Cells),
		Incidents: []HealthSpan{},
	}
	var samples []healthlog.Sample
	if a.healthLog != nil {
		var err error
		samples, err = a.healthLog.Samples(network, since, now.Add(cell))
		if err != nil {
			row.Error = err.Error()
		}
		if earliest, err := a.healthLog.Earliest(network); err != nil {
			if row.Error == "" {
				row.Error = err.Error()
			}
		} else if !earliest.IsZero() {
			row.RecordingSince = earliest.Format(time.RFC3339)
		}
	}

	for _, b := range healthlog.Buckets(samples, now, cell, win.Cells) {
		row.Cells = append(row.Cells, HealthCell{
			Start:   b.Start.Format(time.RFC3339),
			State:   b.State,
			Samples: b.Samples,
			Healthy: b.Healthy,
		})
	}

	trs := healthlog.Transitions(samples, healthlog.GapTolerance, now)
	sum := healthlog.Summarize(trs)
	row.Samples, row.HealthySamples = sum.Samples, sum.HealthySamples
	row.UptimePercent = round2(sum.UptimePercent)
	row.Outages, row.DownSeconds, row.NoDataSeconds = sum.Outages, sum.DownSeconds, sum.NoDataSeconds
	if len(samples) > 0 {
		row.FirstSample = samples[0].At.Format(time.RFC3339)
		row.LastSample = samples[len(samples)-1].At.Format(time.RFC3339)
	}
	if sum.Current != nil {
		s := healthSpan(*sum.Current)
		row.Current = &s
	}
	if sum.LastOutage != nil {
		s := healthSpan(*sum.LastOutage)
		row.LastOutage = &s
	}
	// Newest first: the incident a reader wants is almost always the last one.
	for i := len(trs) - 1; i >= 0 && len(row.Incidents) < maxHealthIncidents; i-- {
		if trs[i].Healthy {
			continue
		}
		row.Incidents = append(row.Incidents, healthSpan(trs[i]))
	}
	row.Summary = healthSummary(a.healthLog != nil, win, now, row)
	return row
}

func healthSpan(t healthlog.Transition) HealthSpan {
	return HealthSpan{
		State:   t.State,
		Healthy: t.Healthy,
		Start:   t.Start.UTC().Format(time.RFC3339),
		End:     t.End.UTC().Format(time.RFC3339),
		Seconds: int(t.Duration().Seconds()),
		Samples: t.Samples,
		Ongoing: t.Ongoing,
		Detail:  t.Detail,
	}
}

// healthStateLabel names a state for prose. The constants are named for who
// has to act, which makes two of them compound words rather than adjectives.
func healthStateLabel(state string) string {
	switch state {
	case DiagAlive:
		return "alive"
	case DiagSyncing:
		return "syncing"
	case DiagStale:
		return "stale"
	case DiagIndexerDown:
		return "indexer down"
	case DiagIndexerBehind:
		return "indexer behind"
	case DiagNodeUnreachable:
		return "node unreachable"
	case DiagWedged:
		return "wedged"
	case healthlog.StateNoData:
		return "not sampled"
	default:
		return "unknown"
	}
}

// healthSummary writes the sentence under a network's strip.
//
// Every branch here is a different honest answer, and the one that matters
// most is the empty one. A fresh instance has no history, and it must say so:
// an empty strip rendered as "100% available" is a lie told by a page whose
// entire purpose is to be trusted about this.
func healthSummary(enabled bool, win healthWindow, now time.Time, row HealthHistoryRow) string {
	switch {
	case !enabled:
		return "not recorded on this instance"
	case row.Error != "":
		return "the availability history could not be read: " + row.Error
	case row.Samples == 0:
		if row.RecordingSince != "" {
			// Rows exist, just none in this window: a process started an hour
			// ago asked for 30 days, or retention has aged the window out.
			return fmt.Sprintf("no samples in the last %s; this instance has been recording since %s",
				win.Name, shortStamp(row.RecordingSince))
		}
		return "no availability history recorded yet: this instance has taken no sample for this network"
	}

	covered := fmt.Sprintf("%.2f%% of %d samples", row.UptimePercent, row.Samples)
	since := ""
	if row.RecordingSince != "" {
		if t, err := time.Parse(time.RFC3339, row.RecordingSince); err == nil && t.After(now.Add(-win.Span)) {
			// Shorter history than the window asked for. Say so rather than
			// letting the percentage imply a week it does not have.
			since = fmt.Sprintf(", recording only since %s", shortStamp(row.RecordingSince))
		}
	}

	cur := row.Current
	switch {
	case cur != nil && cur.State == healthlog.StateNoData:
		return fmt.Sprintf("not being sampled right now, for %s; %s available over the last %s%s",
			humanDuration(cur.Seconds), covered, win.Name, since)
	case cur != nil && !cur.Healthy:
		return fmt.Sprintf("%s right now, for %s so far; %d outage(s) and %s available over the last %s%s",
			healthStateLabel(cur.State), humanDuration(cur.Seconds), row.Outages, covered, win.Name, since)
	case row.LastOutage != nil:
		ago := ""
		if t, err := time.Parse(time.RFC3339, row.LastOutage.End); err == nil {
			ago = humanDuration(int(now.Sub(t).Seconds())) + " ago"
		}
		return fmt.Sprintf("last unreachable %s: %s for %s; %d outage(s) and %s available over the last %s%s",
			ago, healthStateLabel(row.LastOutage.State), humanDuration(row.LastOutage.Seconds),
			row.Outages, covered, win.Name, since)
	default:
		return fmt.Sprintf("no outage in the last %s; %s available%s", win.Name, covered, since)
	}
}

// shortStamp renders an RFC3339 instant for prose, to the minute. Seconds on a
// "recording since" stamp are noise.
func shortStamp(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
