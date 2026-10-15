package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gnoverse/gnoscope/pkg/traffic"
)

// The engagement beacon: the one endpoint on this server that anybody on the
// internet can write to.
//
// Three bounds, all enforced here rather than trusted from the page:
//
//   - the body is read through a MaxBytesReader, so a client cannot make this
//     process allocate by sending a large JSON array;
//   - the batch is capped, so one POST cannot become a thousand rows;
//   - every field is clamped in traffic.Event.normalize, so a dwell of a year
//     or a megabyte of label is stored as the bound, or not at all.
//
// What the client cannot send at any size is identity: the visitor hash is
// derived here from the connection and the daily salt, exactly as the request
// log does it, and a visitor field in the body is ignored because there is no
// field in the struct to receive it.
const (
	// maxEventBody is sized for the batch: 32 events whose free-text fields
	// are clipped at 80 bytes each cannot reach a third of this.
	maxEventBody = 16 << 10
	// maxEventBatch is what one beacon may carry. The page flushes on leaving
	// a page and on a hidden tab, so a batch is normally one to five events.
	maxEventBatch = 32
)

// eventPayload is the wire shape. Short names, because this body is sent on
// pagehide over a connection the browser is about to tear down, and because
// every byte here is one the reader paid for.
type eventPayload struct {
	Events []struct {
		Kind    string `json:"k"`
		Path    string `json:"p"`
		From    string `json:"f"`
		Action  string `json:"a"`
		Value   string `json:"v"`
		Network string `json:"n"`
		Dwell   int64  `json:"d"`
		Scroll  int    `json:"s"`
		Actions int    `json:"c"`
		Results int    `json:"r"`
	} `json:"e"`
}

// HandleEvent records what a reader did, as opposed to what they were served.
//
// Answers 204 whatever happens, including on a malformed body. This is
// instrumentation: it is behind a reader, never in front of one, and the page
// does not read the response. A 400 here would buy nothing and would print a
// console error on every navigation of anyone running a build whose
// instrumentation is newer than their server.
func (a *API) HandleEvent(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	if a.traffic == nil {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventBody))
	if err != nil || len(body) == 0 {
		return
	}
	var p eventPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return
	}
	if len(p.Events) > maxEventBatch {
		p.Events = p.Events[:maxEventBatch]
	}
	now := time.Now()
	ua := r.Header.Get("User-Agent")
	client := traffic.ClientClass(ua)
	visitor := a.traffic.Visitor(ClientIP(r), ua, now)
	host := requestHost(r)
	for _, e := range p.Events {
		ev := traffic.Event{
			At:        now,
			Visitor:   visitor,
			Host:      host,
			Kind:      e.Kind,
			Path:      e.Path,
			From:      e.From,
			Action:    e.Action,
			Value:     e.Value,
			Network:   e.Network,
			DwellMS:   e.Dwell,
			ScrollPct: e.Scroll,
			Actions:   e.Actions,
			Results:   e.Results,
			Client:    client,
			Robot:     client == "bot",
		}
		// EntityKind and PageKind are derived here, from the path, rather than
		// read from the body. The frontend knows them too, and sending them
		// would make the classification two implementations that must agree:
		// the request log already derives them this way, and a panel that
		// disagreed with the one beside it would be impossible to debug.
		ev.EntityKind, ev.Entity = traffic.Entity(e.Path)
		ev.PageKind = traffic.PageKind(e.Path)
		a.traffic.RecordEvent(ev)
	}
}

// HandleEngagement answers the behaviour half of the traffic dashboard.
//
// GET /api/traffic/engagement?window=&host=&who=&limit=
//
// Public and aggregates only, for the same reason /api/traffic is: the rows
// behind this carry a visitor id and a timestamp, and three of them together
// re-identify a reader the hashing went to some trouble not to keep. Nothing
// here returns a row, and adding something that did would undo the design.
func (a *API) HandleEngagement(w http.ResponseWriter, r *http.Request) {
	if a.traffic == nil {
		JSONResponse(w, traffic.Engagement{Empty: true})
		return
	}
	// Same reason HandleTraffic flushes: this endpoint's whole job is to say
	// what just happened, and the buffer is five seconds of it.
	a.traffic.Flush()

	qs := r.URL.Query()
	q := traffic.Query{
		Window:  traffic.ParseWindow(qs.Get("window")),
		Network: a.networkParam(r),
		Host:    strings.ToLower(strings.TrimSpace(qs.Get("host"))),
		Who:     traffic.ParseWho(qs.Get("who")),
		Now:     time.Now(),
	}
	if n, err := strconv.Atoi(qs.Get("limit")); err == nil {
		q.Limit = n
	}
	rep, err := a.traffic.Engagement(q)
	if err != nil {
		jsonError(w, "engagement: "+err.Error(), http.StatusInternalServerError)
		return
	}
	JSONResponse(w, rep)
}
