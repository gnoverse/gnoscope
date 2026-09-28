package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moul/mygnoscan/pkg/discover"
	"github.com/moul/mygnoscan/pkg/glossary"
	"github.com/moul/mygnoscan/pkg/store"
)

// GET /api/discover: what is new on this chain, ranked, with a recommendation.
//
// Reads the rollup rather than the chain. Everything expensive happened on the
// tick; this handler filters, applies the two read-time score terms, decides a
// verdict and writes the envelope.
//
// Every filter is a query parameter and nothing lives in client state, because
// the URL is how a reader sends a view to somebody else, and because the feed
// URL will be this URL with a suffix swapped.

// discoverWindows are the periods this endpoint offers, using the same names
// the rest of the site uses for "recently".
var discoverWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// DiscoverDefaultWindow matches the builder's own window: asking for more than
// was built would report an empty stretch as a quiet chain.
const DiscoverDefaultWindow = "30d"

type discoverEvent struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	At        string          `json:"at"`
	Height    int64           `json:"height"`
	Network   string          `json:"network"`
	Facts     json.RawMessage `json:"facts"`
	Layers    json.RawMessage `json:"layers"`
	FirstEver bool            `json:"first_ever"`

	// The four flat fields a card reads. Derived here and nowhere else, so a
	// headline cannot say something its layers do not.
	Headline      string           `json:"headline"`
	Explanation   string           `json:"explanation"`
	Verdict       discover.Verdict `json:"verdict"`
	VerdictReason string           `json:"verdict_reason"`

	Judgement discover.Judgement `json:"judgement"`

	Actor     string `json:"actor,omitempty"`
	Target    string `json:"target,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Evidence  string `json:"evidence_tx,omitempty"`

	// Demoted marks a row the diversity cap moved down. It keeps its score and
	// stays in the response, so the ranking stays checkable.
	Demoted bool `json:"demoted"`
	// Image is null until realm screenshots land. Shipping the field now means
	// no contract change then.
	Image *string `json:"image"`

	Score      float64            `json:"_score"`
	ScoreTerms map[string]float64 `json:"_score_terms"`
}

type discoverResponse struct {
	Network             string            `json:"network"`
	Vocabulary          int               `json:"vocabulary"`
	GeneratedAt         string            `json:"generated_at"`
	BuiltAt             string            `json:"built_at"`
	Window              map[string]string `json:"window"`
	Counts              map[string]int    `json:"counts"`
	VerdictCounts       map[string]int    `json:"verdict_counts"`
	GlossaryVersion     string            `json:"glossary_version"`
	ClearanceConfigured bool              `json:"clearance_configured"`
	Total               int               `json:"total"`
	// Unranked is how many events in the window the bounded pool did not judge.
	// verdict_counts plus this equals total, always, so a reader can check the
	// envelope rather than trust it.
	Unranked   int             `json:"unranked"`
	NextCursor string          `json:"next_cursor,omitempty"`
	Events     []discoverEvent `json:"events"`
}

// DiscoverCandidatePool is how many stored events one request ranks over.
//
// It is the store's own cap, and asking for more is not a way to get more: the
// store clamps, which is how the first version of this ranked 50 events while
// the constant said 1000.
//
// A bounded pool is sound here only because it is ordered by score. The events
// outside it are the lowest-scoring in the window, and an event's final score
// never exceeds its stored base, so none of them can overtake something inside.
// Ordered by time the same bound was unsound, and hid ten of the twenty-one
// highest-scoring events on mainnet.
const DiscoverCandidatePool = store.DiscoverLimitMax

// DiscoverVocabulary is the event-vocabulary version. A consumer that stored
// picks against v1 ids can tell when the meaning of a kind changed.
const DiscoverVocabulary = 1

// HandleDiscover serves the feed.
func (a *API) HandleDiscover(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	if network == "" {
		// The same refusal the storage map makes, for the same reason: this is
		// one chain's feed, and merging two chains' events into one ranked list
		// would produce a page true of neither.
		jsonError(w, "discover is per-chain: add ?network=", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()

	since, windowLabel := discoverSince(q)
	limit := 50
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > store.DiscoverLimitMax {
		limit = store.DiscoverLimitMax
	}

	// highlights is the shorthand for "just tell me": a short window, a handful
	// of rows and a tight diversity cap. Five rows about five different people
	// is a week; five rows about one person is a changelog.
	highlights := 0
	if n, err := strconv.Atoi(q.Get("highlights")); err == nil && n > 0 {
		highlights = n
		limit = n
		since = time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
		windowLabel = "7d"
	}

	byScore := q.Get("order") != "time"
	filter := store.DiscoverQuery{
		Network:   network,
		Kinds:     repeated(q, "kind"),
		Namespace: q.Get("namespace"),
		Actor:     q.Get("actor"),
		Since:     since,
	}

	// The candidate pool. Ranking, the diversity cap and the verdict filter all
	// act on the whole set, so paging cannot be pushed into SQL without changing
	// the answer, and the pool has to be ordered by the thing being ranked.
	//
	// Ordering it by time and then sorting those by score gives "the best of
	// the most recent N", which is a different answer and a quietly wrong one:
	// measured on mainnet 2026-09-28, a 200-row time-ordered pool held 11 of
	// the 21 deployer.first events, so half of the highest-scoring kind on the
	// chain could not reach the page.
	//
	// The pool is larger than a page because recency can reorder within it: an
	// event's final score never exceeds its stored base, so anything outside
	// the top DiscoverCandidatePool by base cannot overtake something inside it
	// that is also recent. It is a bound, not a proof, and it is generous for
	// that reason.
	pool := filter
	pool.ByScore = byScore
	pool.Limit = DiscoverCandidatePool
	pool.Cursor = q.Get("cursor")

	rows, next, err := a.db.DiscoverEvents(pool)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	p90, p60, err := a.db.DiscoverScorePercentiles(network, since)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	cfg := a.clearance
	now := time.Now().UTC()
	scored := make([]discoverEvent, 0, len(rows))
	for _, row := range rows {
		scored = append(scored, a.scoreAndJudge(row, cfg, now, p90, p60))
	}

	// Undamped sort first, and it is total: score, then time, then id. Damping
	// is rank-dependent, so the score is not a pure function of the row, and a
	// non-total sort would let two readers in the same tick see different
	// pages.
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if scored[i].At != scored[j].At {
			return scored[i].At > scored[j].At
		}
		return scored[i].ID < scored[j].ID
	})
	applyDamping(scored)
	applyDiversityCap(scored, highlights)

	verdictCounts := map[string]int{"share": 0, "maybe": 0, "hold": 0}
	for _, e := range scored {
		verdictCounts[string(e.Verdict)]++
	}
	// The counts describe what was actually judged, and `unranked` is the rest.
	//
	// Three versions of this tried to say something about the unjudged
	// remainder, and all three were wrong. The page size reported as the total;
	// then the pool's holds reported as the window's holds, which was 0 against
	// 275; then "everything below the interest boundary is certainly held",
	// which double-counted the moment the pool covered most of the window,
	// producing 323 counted events in a window of 209.
	//
	// The last one is the instructive failure, because the reasoning was sound
	// and the guard was not: rows below the boundary are indeed always held,
	// but checking that the pool was *full* is not checking that it *excluded*
	// anything. With 209 events and a 200-row pool, nine were outside it and
	// the other 123 were counted twice.
	//
	// So: no inference. A verdict is counted when an event was judged, and
	// anything else is Unranked. It is a smaller claim and it is one a reader
	// can check by adding the numbers up.
	// The real count, over the filter and not over the fetched slice.
	total, err := a.db.DiscoverTotal(filter)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	unranked := total - len(scored)
	if unranked < 0 {
		unranked = 0
	}

	// After the counts, deliberately: a filter chip has to show what it would
	// find, not what the filter already applied left behind, which is the same
	// reason `counts` is over the unfiltered window.
	if wanted := verdictFilter(q); wanted != nil {
		kept := scored[:0]
		for _, e := range scored {
			if wanted[string(e.Verdict)] {
				kept = append(kept, e)
			}
		}
		scored = kept
	}

	if len(scored) > limit {
		scored = scored[:limit]
	} else {
		// Every candidate fitted, so there is nothing beyond this page and the
		// store's cursor would send a reader to an empty one.
		next = ""
	}

	counts, err := a.db.DiscoverCounts(network, since)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	builtAt, _ := a.db.DiscoverBuiltAt(network)

	version := ""
	if g := glossary.Get(); g != nil {
		version = g.Version
	}

	JSONResponse(w, discoverResponse{
		Network:     network,
		Vocabulary:  DiscoverVocabulary,
		GeneratedAt: now.Format(time.RFC3339),
		// The gap between built_at and generated_at is the page's real
		// staleness. Worth showing: a reader who refreshes and sees nothing new
		// deserves to know the tick has not run rather than concluding the
		// chain is quiet.
		BuiltAt:             builtAt,
		Window:              map[string]string{"from": since, "to": now.Format(time.RFC3339), "label": windowLabel},
		Counts:              counts,
		VerdictCounts:       verdictCounts,
		GlossaryVersion:     version,
		ClearanceConfigured: cfg.Configured(),
		Total:               total,
		Unranked:            unranked,
		NextCursor:          next,
		Events:              scored,
	})
}

func (a *API) scoreAndJudge(row store.DiscoverEvent, cfg discover.ClearanceConfig, now time.Time, p90, p60 float64) discoverEvent {
	ageHours := 0.0
	if at, err := time.Parse(time.RFC3339, row.At); err == nil {
		ageHours = now.Sub(at).Hours()
	}
	recency := discover.Recency(ageHours)
	score := row.ScoreBase * recency

	var layers discover.Layers
	_ = json.Unmarshal(row.Layers, &layers)

	interest := discover.Bucket(row.ScoreBase, p90, p60)
	clearance := cfg.Clear(discover.Subject{
		Kind:      row.Kind,
		Namespace: row.Namespace,
		Actor:     row.Actor,
		Parties:   []string{row.Actor, row.Target},
	})
	verdict, judgement := discover.Decide(interest, clearance, interestWhy(row))

	return discoverEvent{
		ID: row.ID, Kind: row.Kind, At: row.At, Height: row.Height, Network: row.Network,
		Facts: row.Facts, Layers: row.Layers, FirstEver: row.FirstEver,
		Headline:      layers.Headline(),
		Explanation:   layers.Explanation(),
		Verdict:       verdict,
		VerdictReason: discover.Reason(verdict, judgement),
		Judgement:     judgement,
		Actor:         row.Actor, Target: row.Target, Namespace: row.Namespace,
		Evidence: row.EvidenceTx,
		Image:    nil,
		Score:    score,
		ScoreTerms: map[string]float64{
			"base": row.ScoreBase, "recency": round2(recency), "actor_damp": 1,
		},
	}
}

// interestWhy names something real from the row rather than restating the
// score, because a reason a reader cannot check is not a reason.
func interestWhy(row store.DiscoverEvent) string {
	switch {
	case row.FirstEver:
		return "nobody had done this before"
	case row.Reach > 1:
		return "from " + strconv.FormatInt(row.Reach, 10) + " different people"
	case row.Magnitude > 0 && row.Kind == "chain.spike":
		return "far more than a normal day"
	default:
		return "it happened on this chain"
	}
}

// applyDamping is the second pass. rank_within_actor is this event's position
// among the same actor's events in the already-sorted set, so one deployer
// publishing 179 packages in a day keeps the top slot and loses the rest.
func applyDamping(events []discoverEvent) {
	seen := map[string]int{}
	for i := range events {
		actor := events[i].Actor
		if actor == "" {
			continue
		}
		seen[actor]++
		d := discover.Damp(seen[actor])
		events[i].Score *= d
		events[i].ScoreTerms["actor_damp"] = round2(d)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Score != events[j].Score {
			return events[i].Score > events[j].Score
		}
		if events[i].At != events[j].At {
			return events[i].At > events[j].At
		}
		return events[i].ID < events[j].ID
	})
}

// applyDiversityCap is a demotion and never a deletion. An over-quota row keeps
// its score, moves below the boundary and is marked, so nothing disappears and
// the ranking stays checkable.
func applyDiversityCap(events []discoverEvent, highlights int) {
	perActor, perNamespace, window := 3, 4, 20
	if highlights > 0 {
		perActor, perNamespace, window = 1, 2, highlights
	}

	actors, namespaces := map[string]int{}, map[string]int{}
	var kept, demoted []discoverEvent
	for _, e := range events {
		over := false
		if len(kept) < window {
			if e.Actor != "" && actors[e.Actor] >= perActor {
				over = true
			}
			if e.Namespace != "" && namespaces[e.Namespace] >= perNamespace {
				over = true
			}
		}
		if over {
			e.Demoted = true
			demoted = append(demoted, e)
			continue
		}
		actors[e.Actor]++
		namespaces[e.Namespace]++
		kept = append(kept, e)
	}
	copy(events, append(kept, demoted...))
}

// discoverSince resolves the window. An explicit from wins over a label,
// because a reader who typed a date means it.
func discoverSince(q map[string][]string) (since, label string) {
	get := func(k string) string {
		if v, ok := q[k]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if from := get("from"); from != "" {
		if _, err := time.Parse(time.RFC3339, from); err == nil {
			return from, "custom"
		}
	}
	label = get("window")
	if label == "all" {
		return "", "all"
	}
	d, ok := discoverWindows[label]
	if !ok {
		label = DiscoverDefaultWindow
		d = discoverWindows[label]
	}
	return time.Now().UTC().Add(-d).Format(time.RFC3339), label
}

// verdictFilter reads ?verdict=share,maybe. nil means no filter.
//
// An unrecognised name is dropped rather than erroring, and a filter naming
// nothing valid falls through to no filter: the query string is the way a
// reader shares a view, and a typo in a shared link should show the feed rather
// than an error page.
func verdictFilter(q map[string][]string) map[string]bool {
	raw := append([]string{}, q["verdict"]...)
	if len(raw) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, chunk := range raw {
		for _, name := range strings.Split(chunk, ",") {
			switch strings.TrimSpace(name) {
			case "all":
				return nil
			case "share", "maybe", "hold":
				want[strings.TrimSpace(name)] = true
			}
		}
	}
	if len(want) == 0 {
		return nil
	}
	return want
}

func repeated(q map[string][]string, key string) []string {
	var out []string
	for _, chunk := range q[key] {
		for _, v := range strings.Split(chunk, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
