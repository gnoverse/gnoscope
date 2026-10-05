package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gnoverse/gnoscope/pkg/discover"
	"github.com/gnoverse/gnoscope/pkg/store"
	"github.com/gnoverse/gnoscope/pkg/tags"
)

// The code timeline: every publication on one chain, newest first, the way a
// GitHub dashboard lists what the people you follow pushed.
//
// It reads package_submissions, one row per MsgAddPackage, never overwritten,
// and classifies each row against the ones before it:
//
//	new       the first successful submission at a path
//	version   the first successful submission at a path whose
//	          discover.Generation family already had an older generation
//	redeploy  the same path again, after a success there
//	failed    a submission the chain refused; hidden unless asked for
//
// A row's kind depends only on the rows before it, so it never changes once
// written. That is what makes a page behind a cursor safe to cache for longer
// than the head: the one thing about an old row that can move is its lines and
// summary, which are only known for the submission that is still current and
// leave it when the path is redeployed.

const (
	// CodeTimelinePath and CodeTimelineHeatmapPath are the routes. The
	// response cache reads the first to give cursor pages a longer TTL.
	CodeTimelinePath        = "/api/code/timeline"
	CodeTimelineHeatmapPath = "/api/code/timeline/heatmap"

	codeTimelineDefaultLimit = 50
	codeTimelineMaxLimit     = 200
	// codeTimelineHeatmapDays is a GitHub contribution calendar's span.
	codeTimelineHeatmapDays = 365
	// codeTimelineSummaryMax caps the one-line summary, in characters.
	codeTimelineSummaryMax = 160
	// codeTimelineMemoTTL bounds how long a classified feed is reused when
	// no submission moved: the lines, imports and summaries it carries are
	// filled by later passes (the symbol indexer writes the doc comment), and
	// would otherwise wait for the next deploy to show.
	codeTimelineMemoTTL = 5 * time.Minute
)

// The timeline's kinds.
const (
	tlNew      = "new"
	tlVersion  = "version"
	tlRedeploy = "redeploy"
	tlFailed   = "failed"
)

// codeTimelineNow is the clock the heatmap's year is counted back from. A
// variable so a test can pin it.
var codeTimelineNow = time.Now

// timelineRow is one submission as the feed draws it.
type timelineRow struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	NS      string `json:"ns"`
	K       string `json:"k"`
	Creator string `json:"creator"`
	// User is the creator's registered r/sys/users name, when it has one.
	User   string `json:"user,omitempty"`
	Height int    `json:"height"`
	Time   string `json:"time,omitempty"`
	Tx     string `json:"tx"`
	Msg    int    `json:"msg"`
	Files  int    `json:"files"`
	// Current is true when this submission's source is the one stored, which
	// is the only case lines, imports and summary are known in.
	Current bool   `json:"current,omitempty"`
	Lines   int    `json:"lines,omitempty"`
	Imports int    `json:"imports,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Family is the discover.Generation family key, when it differs from the
	// path. Prev is, for a version, the newest older generation's path.
	Family string `json:"family,omitempty"`
	Prev   string `json:"prev,omitempty"`
	// Nth counts successful submissions at this path, this one included, so
	// a redeploy's is 2 or more.
	Nth int `json:"nth,omitempty"`
	// Debut marks the creator's first successful submission on this chain.
	Debut bool `json:"debut,omitempty"`
	// Genesis marks a row at height 0, in the chain's starting state.
	Genesis bool `json:"genesis,omitempty"`
	// Tags are the code-derived tags of the stored source, so only a current
	// row carries them, like lines and summary.
	Tags []tags.Tag `json:"tags,omitempty"`

	day string
}

// timelineSnap is one network's classified feed, newest first.
type timelineSnap struct {
	count, maxHeight int
	at               time.Time
	rows             []timelineRow
	byName           map[string]string // registered name -> address
}

type timelineMemo struct {
	mu    sync.Mutex
	snaps map[string]*timelineSnap
}

// codeTimeline returns the network's feed, recomputing it only when its
// submissions moved or the memo aged out. Serialised per API, so two readers
// arriving at a cold memo compute it once.
func (a *API) codeTimeline(network string) (*timelineSnap, error) {
	count, maxHeight, err := a.db.CodeTimelineStamp(network)
	if err != nil {
		return nil, err
	}
	m := &a.timeline
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.snaps[network]; s != nil && s.count == count && s.maxHeight == maxHeight &&
		time.Since(s.at) < codeTimelineMemoTTL {
		return s, nil
	}
	src, err := a.db.CodeTimeline(network)
	if err != nil {
		return nil, err
	}
	s := classifyTimeline(src)
	s.count, s.maxHeight, s.at = count, maxHeight, time.Now()
	if m.snaps == nil {
		m.snaps = map[string]*timelineSnap{}
	}
	m.snaps[network] = s
	return s, nil
}

// classifyTimeline walks the submissions oldest first and gives each its
// kind. The family relation is discover.Generation's, the same one the
// directory, the version rail and the code tree use, so "v2 of boards" means
// the same thing on every page.
func classifyTimeline(src store.CodeTimelineSource) *timelineSnap {
	type famGen struct {
		gen  []int
		path string
	}
	okAt := map[string]int{}          // path -> successful submissions so far
	families := map[string][]famGen{} // family -> generations published so far
	rows := make([]timelineRow, 0, len(src.Submissions))
	for _, s := range src.Submissions {
		r := timelineRow{
			Path:    s.Path,
			NS:      store.NamespaceOf(s.Path),
			K:       "p",
			Creator: s.Creator,
			User:    src.Users[s.Creator],
			Height:  s.Height,
			Time:    s.Time,
			Tx:      s.TxHash,
			Msg:     s.MsgIndex,
			Files:   s.NumFiles,
			Genesis: s.Height == 0,
		}
		if s.IsRealm {
			r.K = "r"
		}
		if len(s.Time) >= 10 {
			r.day = s.Time[:10]
		}
		family, gen, famOK := discover.Generation(s.Path)
		if famOK && family != s.Path {
			r.Family = family
		}
		switch {
		case !s.Success:
			r.Kind = tlFailed
		case okAt[s.Path] > 0:
			r.Kind = tlRedeploy
		default:
			r.Kind = tlNew
			if famOK {
				var prev *famGen
				for i, g := range families[family] {
					if g.path != s.Path && discover.GenerationLess(g.gen, gen) &&
						(prev == nil || discover.GenerationLess(prev.gen, g.gen)) {
						prev = &families[family][i]
					}
				}
				if prev != nil {
					r.Kind, r.Prev = tlVersion, prev.path
				}
				families[family] = append(families[family], famGen{gen: gen, path: s.Path})
			}
		}
		if s.Success {
			okAt[s.Path]++
			r.Nth = okAt[s.Path]
			if h, ok := src.Debuts[s.Creator]; ok && h == s.Height && okAt[s.Path] == 1 {
				r.Debut = true
			}
			if p, ok := src.Current[s.Path]; ok && p.TxHash == s.TxHash {
				r.Current = true
				r.Lines, r.Imports = p.Lines, p.Imports
				r.Summary = timelineSummary(p.Doc)
				r.Tags = src.Tags[s.Path]
			}
		}
		rows = append(rows, r)
	}
	// A debut is one row: two packages published in the creator's first block
	// both match its height, and only the first of them is the debut.
	seen := map[string]bool{}
	for i := range rows {
		if rows[i].Debut {
			if seen[rows[i].Creator] {
				rows[i].Debut = false
			}
			seen[rows[i].Creator] = true
		}
	}
	// Newest first, which is the order every page is read in.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	byName := make(map[string]string, len(src.Users))
	for addr, name := range src.Users {
		byName[name] = addr
	}
	return &timelineSnap{rows: rows, byName: byName}
}

// timelineSummary cuts a doc comment or README lead to one plain line.
func timelineSummary(doc string) string {
	s := firstSentence(doc)
	if utf8.RuneCountInString(s) <= codeTimelineSummaryMax {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:codeTimelineSummaryMax-1])) + "…"
}

// timelineCursor is a position in the newest-first order: (height, tx, msg)
// descending. Written `<height>.<msg>.<tx>`, tx last because a gno hash is
// base64 and may hold any character but the dot.
type timelineCursor struct {
	height, msg int
	tx          string
}

func (c timelineCursor) String() string {
	return strconv.Itoa(c.height) + "." + strconv.Itoa(c.msg) + "." + c.tx
}

func parseTimelineCursor(s string) (timelineCursor, bool) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 || parts[2] == "" {
		return timelineCursor{}, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || m < 0 {
		return timelineCursor{}, false
	}
	return timelineCursor{height: h, msg: m, tx: parts[2]}, true
}

// before reports whether r comes after c in the newest-first order, which is
// "strictly older" in (height, tx, msg).
func (c timelineCursor) before(r timelineRow) bool {
	if r.Height != c.height {
		return r.Height < c.height
	}
	if r.Tx != c.tx {
		return r.Tx < c.tx
	}
	return r.Msg < c.msg
}

// timelineFilter is what a request narrows the feed to.
type timelineFilter struct {
	kinds   map[string]bool
	ns      string
	creator string
	day     string
	tag     string
}

func (f timelineFilter) match(r timelineRow) bool {
	return f.kinds[r.Kind] &&
		(f.ns == "" || r.NS == f.ns) &&
		(f.creator == "" || r.Creator == f.creator) &&
		(f.day == "" || r.day == f.day) &&
		(f.tag == "" || hasTag(r.Tags, f.tag))
}

func hasTag(ts []tags.Tag, tag string) bool {
	for _, t := range ts {
		if t.Tag == tag {
			return true
		}
	}
	return false
}

// parseTimelineFilter reads kind, failed, ns, creator, tag and day. A creator may
// be an address or a registered name; a name resolves on this network only.
func parseTimelineFilter(r *http.Request, snap *timelineSnap, withDay bool) (timelineFilter, string) {
	q := r.URL.Query()
	f := timelineFilter{kinds: map[string]bool{}, ns: strings.TrimSpace(q.Get("ns"))}
	for _, v := range q["kind"] {
		for _, k := range strings.Split(v, ",") {
			switch k = strings.TrimSpace(k); k {
			case "":
			case tlNew, tlVersion, tlRedeploy, tlFailed:
				f.kinds[k] = true
			default:
				return f, "unknown kind " + strconv.Quote(k) + ": want new, version, redeploy or failed"
			}
		}
	}
	if len(f.kinds) == 0 {
		f.kinds = map[string]bool{tlNew: true, tlVersion: true, tlRedeploy: true}
	}
	switch q.Get("failed") {
	case "", "0", "false":
	case "1", "true":
		f.kinds[tlFailed] = true
	default:
		return f, "failed is 1 or 0"
	}
	tag, bad := tagParam(r)
	if bad != "" {
		return f, bad
	}
	f.tag = tag
	if c := strings.TrimPrefix(strings.TrimSpace(q.Get("creator")), "@"); c != "" {
		if addr, ok := snap.byName[c]; ok && !strings.HasPrefix(c, "g1") {
			c = addr
		}
		f.creator = c
	}
	if withDay {
		if d := q.Get("day"); d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return f, "day is YYYY-MM-DD"
			}
			f.day = d
		}
	}
	return f, ""
}

type timelinePage struct {
	Network string `json:"network"`
	// Height is the newest submission on the network, whatever the filters.
	Height int `json:"height"`
	// Total counts the rows matching the filters, across every page.
	Total int           `json:"total"`
	Rows  []timelineRow `json:"rows"`
	// Next is the cursor of the following page, absent on the last one.
	Next string `json:"next,omitempty"`
}

// HandleCodeTimeline serves one page of the feed.
//
// network is required, like the code tree's: a feed mixing two chains would
// call one path "new" twice.
func (a *API) HandleCodeTimeline(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	if network == "" {
		jsonError(w, "the code timeline is per-chain: add ?network=", http.StatusBadRequest)
		return
	}
	snap, err := a.codeTimeline(network)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f, bad := parseTimelineFilter(r, snap, true)
	if bad != "" {
		jsonError(w, bad, http.StatusBadRequest)
		return
	}
	limit := codeTimelineDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			jsonError(w, "limit is a positive number", http.StatusBadRequest)
			return
		}
		limit = min(n, codeTimelineMaxLimit)
	}
	start := 0
	cursor := r.URL.Query().Get("before")
	if cursor != "" {
		c, ok := parseTimelineCursor(cursor)
		if !ok {
			jsonError(w, "before is a cursor from a previous page's next", http.StatusBadRequest)
			return
		}
		start = sort.Search(len(snap.rows), func(i int) bool { return c.before(snap.rows[i]) })
	}

	out := timelinePage{Network: network, Height: snap.maxHeight, Rows: []timelineRow{}}
	if out.Height < 0 {
		out.Height = 0
	}
	for i, row := range snap.rows {
		if !f.match(row) {
			continue
		}
		out.Total++
		if i < start {
			continue
		}
		if len(out.Rows) == limit {
			if out.Next == "" {
				last := out.Rows[len(out.Rows)-1]
				out.Next = timelineCursor{height: last.Height, msg: last.Msg, tx: last.Tx}.String()
			}
			continue
		}
		out.Rows = append(out.Rows, row)
	}

	body, err := json.Marshal(out)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The head moves with every deploy; a page behind a cursor holds rows
	// whose kind can never change (see the top of this file), so it is good
	// for longer, and its ETag still catches the lines and summary that can.
	cc := "public, max-age=15"
	if cursor != "" {
		cc = "public, max-age=300"
	}
	writeSourceJSON(w, r, timelineETag(snap, body), cc, body)
}

func timelineETag(snap *timelineSnap, body []byte) string {
	sum := sha256.Sum256(body)
	return `"tl-` + strconv.Itoa(snap.maxHeight) + "-" + strconv.Itoa(snap.count) + "-" +
		hex.EncodeToString(sum[:8]) + `"`
}

// timelineDay is one day of the calendar. Only days with a publication are
// sent; the client lays out the empty ones.
type timelineDay struct {
	Day      string `json:"d"`
	New      int    `json:"n,omitempty"`
	Version  int    `json:"v,omitempty"`
	Redeploy int    `json:"r,omitempty"`
}

type timelineHeatmap struct {
	Network string `json:"network"`
	// From and To are the calendar's first and last day, UTC, inclusive.
	From  string        `json:"from"`
	To    string        `json:"to"`
	Total int           `json:"total"`
	Max   int           `json:"max"`
	Days  []timelineDay `json:"days"`
}

// HandleCodeTimelineHeatmap counts successful publications per UTC day over
// the last year, split by kind, for a contribution calendar. It takes the
// feed's ns and creator filters, so a namespace or a person gets their own
// calendar. Failed submissions are not publications and are never counted.
func (a *API) HandleCodeTimelineHeatmap(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	if network == "" {
		jsonError(w, "the code timeline is per-chain: add ?network=", http.StatusBadRequest)
		return
	}
	snap, err := a.codeTimeline(network)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f, bad := parseTimelineFilter(r, snap, false)
	if bad != "" {
		jsonError(w, bad, http.StatusBadRequest)
		return
	}
	f.kinds = map[string]bool{tlNew: true, tlVersion: true, tlRedeploy: true}

	to := codeTimelineNow().UTC().Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -(codeTimelineHeatmapDays - 1))
	out := timelineHeatmap{Network: network, From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), Days: []timelineDay{}}
	byDay := map[string]*timelineDay{}
	for _, row := range snap.rows {
		if row.day < out.From || row.day > out.To || !f.match(row) {
			continue
		}
		d := byDay[row.day]
		if d == nil {
			d = &timelineDay{Day: row.day}
			byDay[row.day] = d
		}
		switch row.Kind {
		case tlNew:
			d.New++
		case tlVersion:
			d.Version++
		case tlRedeploy:
			d.Redeploy++
		}
		out.Total++
	}
	for _, d := range byDay {
		out.Days = append(out.Days, *d)
		out.Max = max(out.Max, d.New+d.Version+d.Redeploy)
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Day < out.Days[j].Day })

	body, err := json.Marshal(out)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeSourceJSON(w, r, timelineETag(snap, body), "public, max-age=60", body)
}
