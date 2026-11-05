package httpapi

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Making "it is slow" a number instead of an afternoon.
//
// Diagnosing the govdao page on 2026-09-24 took hours of curl because nothing
// the server sends says where time went. X-Cache says which path a response
// took but not what it cost, so a 47-second response carrying X-Cache: HIT
// looked like a broken cache when it was a busy process. Two headers and one
// endpoint would have answered it in a minute.

// WithServerTiming stamps every response with how long the handlers below it
// took, in the standard Server-Timing form that browser devtools already
// render in the network panel.
//
// It sits *inside* the response cache in the middleware chain, so the number is
// the cost of actually computing the answer. A cache hit never reaches it and
// therefore carries no app timing at all, which is exactly the signal wanted:
// Server-Timing present means somebody paid, absent means somebody did not.
func WithServerTiming(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &phaseRecorder{}
		r = r.WithContext(context.WithValue(r.Context(), phaseKey{}, rec))
		next.ServeHTTP(&timingWriter{ResponseWriter: w, start: start, phases: rec}, r)

		// A handler slower than this says so in the log, because the header it
		// would otherwise say it in never reaches anybody.
		//
		// http.Server runs with WriteTimeout 30s, so a handler past that has
		// its connection cut before WriteHeader is ever called: no status, no
		// Server-Timing, and Caddy answers the reader 502. Measured against
		// production on 2026-10-26, /api/holders after a quiet spell did that
		// three times in four, at 36.8s, 50.1s and 67.0s — reported by every
		// client-side measurement as "slow" rather than as the error page it
		// was. The one channel left for a request that dies is this one.
		// A stream is long by design, not slow: /api/live held open for 30
		// seconds is the feature working. Logging those drowns the requests
		// this exists to catch, which is what the first deploy of it did.
		if w.Header().Get("Content-Type") == "text/event-stream" {
			return
		}
		if d := time.Since(start); d >= slowHandlerThreshold {
			log.Printf("slow %s %s in %s%s", r.Method, r.URL.RequestURI(), d.Round(time.Millisecond), rec.header())
		}
	})
}

// slowHandlerThreshold is well under http.Server's 30s WriteTimeout, so a
// request is logged while it can still be correlated with the 502 a reader
// saw, and high enough that ordinary cold computes stay out of the log.
//
// A var rather than a const only so a test can lower it: asserting on this
// otherwise means a test that sleeps for five seconds.
var slowHandlerThreshold = 5 * time.Second

// Phase records how long one named step of a handler took, so an endpoint that
// is sometimes slow can say *which* part was slow rather than only that it was.
//
// Carried on the request context rather than on the ResponseWriter, which is
// the obvious place and the wrong one: WithCompression sits between this
// middleware and the mux, so by the time a handler has a writer it is no longer
// the timingWriter, and a type assertion for one would compile, never match,
// and record nothing. A context value survives every wrapper.
//
// A no-op when nothing is recording, so a handler can call it unconditionally
// and a test can call the handler directly.
func Phase(ctx context.Context, name string, started time.Time) {
	rec, _ := ctx.Value(phaseKey{}).(*phaseRecorder)
	if rec == nil {
		return
	}
	rec.add(name, time.Since(started))
}

type phaseKey struct{}

type phaseRecorder struct {
	mu    sync.Mutex
	parts []string
}

func (p *phaseRecorder) add(name string, d time.Duration) {
	ms := float64(d.Microseconds()) / 1000
	p.mu.Lock()
	defer p.mu.Unlock()
	p.parts = append(p.parts, name+";dur="+strconv.FormatFloat(ms, 'f', 1, 64))
}

func (p *phaseRecorder) header() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.parts) == 0 {
		return ""
	}
	return ", " + strings.Join(p.parts, ", ")
}

// timingWriter writes the header on the way out, at WriteHeader time, because
// that is the last moment it can still be set and the first moment the duration
// is meaningful.
type timingWriter struct {
	http.ResponseWriter
	start   time.Time
	written bool
	phases  *phaseRecorder
}

func (w *timingWriter) stamp() {
	if w.written {
		return
	}
	w.written = true
	ms := float64(time.Since(w.start).Microseconds()) / 1000
	// app first, and the phases after it: appDurationMS reads the app metric
	// out of this header and stops at the first delimiter, so everything added
	// here is invisible to it.
	v := "app;dur=" + strconv.FormatFloat(ms, 'f', 1, 64)
	if w.phases != nil {
		v += w.phases.header()
	}
	w.Header().Set("Server-Timing", v)
}

func (w *timingWriter) WriteHeader(status int) {
	w.stamp()
	w.ResponseWriter.WriteHeader(status)
}

func (w *timingWriter) Write(b []byte) (int, error) {
	w.stamp()
	return w.ResponseWriter.Write(b)
}

// Flush passes through, so an SSE stream is not held back by this wrapper.
func (w *timingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// SetResponseCache hands the API the cache wrapped around it, so it can report
// on it. The cache is built after the API in main, which is why this is a
// setter rather than a constructor argument.
func (a *API) SetResponseCache(c *responseCache) { a.responseCache = c }

// SetWarmer hands the API the warmer, for the same reason.
func (a *API) SetWarmer(w *Warmer) { a.warmer = w }

// CacheReport is what /api/cache/stats answers: the state of the two things
// that decide whether a reader waits.
type CacheReport struct {
	Cache  CacheStats  `json:"cache"`
	Warmer WarmerStats `json:"warmer"`
	// TTLSeconds and StaleGraceSeconds are echoed so a reader of this endpoint
	// can interpret the counters without reading the source.
	TTLSeconds        int `json:"ttl_seconds"`
	StaleGraceSeconds int `json:"stale_grace_seconds"`
}

// HandleCacheStats reports cache and warmer state.
//
// Read it when somebody says a page is slow. A large Misses against a small
// Entries means the warmer is not covering the keys people ask for; a Stale
// that dwarfs Hits means some endpoint costs more to recompute than its TTL
// allows, and belongs in endpointTTL; a Passes that has stopped climbing means
// the warmer is wedged on one target.
func (a *API) HandleCacheStats(w http.ResponseWriter, r *http.Request) {
	out := CacheReport{
		TTLSeconds:        int(CacheTTL / time.Second),
		StaleGraceSeconds: int(CacheStaleGrace / time.Second),
	}
	if a.responseCache != nil {
		out.Cache = a.responseCache.Stats()
	}
	if a.warmer != nil {
		out.Warmer = a.warmer.Stats()
	}
	JSONResponse(w, out)
}
