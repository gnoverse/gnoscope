package httpapi

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gnoverse/gnoscope/pkg/traffic"
)

// Recording what this server was actually asked for.
//
// Until this existed the only analytics here was a third-party <script> tag,
// which is a browser-shaped instrument: it sees a person opening a page and it
// is blind to /api/*, to /mcp, to crawlers, to every 4xx and 5xx, and it drops
// the query string, so mainnet and a testnet were one row. Meanwhile the
// server itself wrote nothing about requests at all; log.Printf carries sync
// progress and the deploy banner, and journald holds no per-request line to
// grep. The question "what are people doing" had no data source.
//
// This middleware is the source. It sits outermost, which is deliberate in the
// same way WithRealmViews is: a cached answer never reaches a handler, so a
// counter placed deeper would count the first reader of a page and miss
// everyone who followed.

// trafficKey carries the per-request slot that inner layers annotate.
type trafficKeyType struct{}

var trafficKey trafficKeyType

// noteSlot is what an inner layer can add to, for facts the outer middleware
// cannot see. Today that is the MCP tool name, which lives in the POST body of
// a single /mcp path and would otherwise make every agent call indistinguishable.
type noteSlot struct {
	mu   sync.Mutex
	tool string
}

// NoteMCPTool records which tool an /mcp request called. A no-op outside a
// request that WithAccessLog wrapped, so the MCP server needs no branch.
func NoteMCPTool(ctx context.Context, name string) {
	slot, ok := ctx.Value(trafficKey).(*noteSlot)
	if !ok || slot == nil {
		return
	}
	slot.mu.Lock()
	// A batched JSON-RPC message can call several tools in one HTTP request.
	// The row records the first, and the "+" says the count is per request
	// rather than per call, so nobody reads the tools panel as a call count.
	if slot.tool == "" {
		slot.tool = name
	} else if !strings.HasSuffix(slot.tool, "+") {
		slot.tool += "+"
	}
	slot.mu.Unlock()
}

// logWriter records what actually went out: the status, and the bytes on the
// wire (so after compression, since this sits outside it).
type logWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *logWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *logWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush passes through so an SSE stream is not held back by this wrapper.
func (w *logWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// WithAccessLog records one row per served request.
//
// mux is passed in only to resolve the matched route *pattern*, which is what
// keeps the stored route bounded by the routing table rather than by whatever
// URLs a crawler invents. Reading the pattern from the mux instead of a
// hand-written prefix list is the version that cannot drift: a route added to
// RegisterRoutes classifies correctly here the moment it exists.
//
// selfHost is this deployment's own hostname, so a link from one page of the
// app to the next is not recorded as an inbound referer.
// It also logs every 5xx to stderr whether or not a store is configured, which
// is the one line that turns "the site was broken for an hour" into a timestamp
// and a route. Three call sites answer 500 through http.Error rather than
// jsonError and carry no message; this catches those too, by status.
func WithAccessLog(store *traffic.Store, mux *http.ServeMux, selfHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		slot := &noteSlot{}
		r = r.WithContext(context.WithValue(r.Context(), trafficKey, slot))

		lw := &logWriter{ResponseWriter: w}
		next.ServeHTTP(lw, r)

		status := lw.status
		if status == 0 {
			status = http.StatusOK
		}
		durMS := float64(time.Since(start).Microseconds()) / 1000
		if status >= 500 {
			log.Printf("http: %d %s %s (%.0fms)", status, r.Method, r.URL.Path, durMS)
		}
		if store == nil {
			return
		}

		// Resolved after serving rather than before, so a request that never
		// reached the mux (rejected network, rate limit) costs no lookup.
		pattern := ""
		if mux != nil {
			_, pattern = mux.Handler(r)
		}
		route, target := traffic.SplitPattern(pattern, r.URL.Path)
		kind := traffic.Kind(route, r.URL.Path, MCPPath)
		if route == traffic.SPARoute && kind == "page" {
			// Every non-API URL matches "GET /", so the pattern says nothing
			// about which page was opened. The app path is the target instead.
			target = traffic.PageTarget(r.URL.Path)
		}

		// An SSE connection is not a request that took four hours. next.ServeHTTP
		// returns when the client disconnects, so DurMS for /api/live is the
		// whole connection lifetime; left in the api bucket it would top the
		// slowest-routes panel forever and drag every percentile with it.
		// Recorded as its own kind, and excluded from the timing queries.
		if strings.HasPrefix(lw.Header().Get("Content-Type"), "text/event-stream") {
			kind = "stream"
		}

		ua := r.Header.Get("User-Agent")
		client := traffic.ClientClass(ua)

		store.Record(traffic.Record{
			At:      start,
			Visitor: store.Visitor(ClientIP(r), ua, start),
			Method:  r.Method,
			Route:   route,
			Target:  target,
			Network: r.URL.Query().Get("network"),
			Kind:    kind,
			Tool:    slot.tool,
			Status:  status,
			Bytes:   lw.bytes,
			DurMS:   durMS,
			AppMS:   appDurationMS(lw.Header().Get("Server-Timing")),
			Cache:   lw.Header().Get("X-Cache"),
			RefHost: traffic.RefererHost(r.Header.Get("Referer"), selfHost),
			Client:  client,
			Robot:   client == "bot",
		})
	})
}

// appDurationMS reads the handler cost back out of the header WithServerTiming
// already computes, rather than measuring it a second time.
//
// Absent on a cache hit, which is the signal rather than a gap: Server-Timing
// present means somebody paid for that answer, absent means somebody did not.
func appDurationMS(h string) float64 {
	const marker = "app;dur="
	i := strings.Index(h, marker)
	if i < 0 {
		return 0
	}
	rest := h[i+len(marker):]
	if j := strings.IndexAny(rest, ",; "); j >= 0 {
		rest = rest[:j]
	}
	v, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		return 0
	}
	return v
}

// SetTraffic hands the API its traffic store, for the same reason
// SetResponseCache exists: it is built after the API in main.
func (a *API) SetTraffic(t *traffic.Store) { a.traffic = t }

// HandleTraffic answers the public traffic dashboard.
//
// GET /api/traffic?window=24h|7d|30d|90d&network=&kind=&bots=1&limit=
//
// Public, and aggregates only. There is no endpoint here that returns a request
// row, and adding one would undo the whole privacy design: three rows carrying
// a visitor id, a timestamp and a referer re-identify a reader that the hashing
// in pkg/traffic went to some trouble not to keep.
func (a *API) HandleTraffic(w http.ResponseWriter, r *http.Request) {
	if a.traffic == nil {
		JSONResponse(w, traffic.Report{Empty: true})
		return
	}
	// Flush first, for the same reason HandleViews does: this is the endpoint
	// whose whole job is to say what just happened, and answering it from a
	// database knowingly five seconds behind a buffer in this same process is
	// a worse trade than one small write. It also makes the counts testable
	// without a sleep.
	a.traffic.Flush()

	q := traffic.Query{
		Window:   traffic.ParseWindow(r.URL.Query().Get("window")),
		Network:  a.networkParam(r),
		Kind:     r.URL.Query().Get("kind"),
		WithBots: r.URL.Query().Get("bots") == "1",
		Now:      time.Now(),
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		q.Limit = n
	}

	rep, err := a.traffic.Report(q)
	if err != nil {
		jsonError(w, "traffic report: "+err.Error(), http.StatusInternalServerError)
		return
	}
	JSONResponse(w, rep)
}

// HandleTrafficHealth reports the writer's own state, for whoever is on call.
//
// Counters only: how much is buffered, how much reached disk, how much was
// dropped under load, and what the retention is. Dropped climbing is the one
// number worth alerting on: it means the buffer cap is doing its job, which
// means the log is now incomplete and the dashboard is understating traffic.
func (a *API) HandleTrafficHealth(w http.ResponseWriter, r *http.Request) {
	if a.traffic == nil {
		JSONResponse(w, map[string]any{"enabled": false})
		return
	}
	s := a.traffic.Stats()
	JSONResponse(w, map[string]any{
		"enabled":        true,
		"buffered":       s.Buffered,
		"written":        s.Written,
		"dropped":        s.Dropped,
		"retention_days": s.RetentionDays,
	})
}
